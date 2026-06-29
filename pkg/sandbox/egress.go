package sandbox

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// EgressConfig is the per-run network policy for a sandbox's governed host-fetch
// capability. It is turned into a stand-alone egress brain (NewBrain) so a wasm
// module's outbound HTTP is mediated by the SAME default-deny allowlist, guarded
// inside-deny, cred-blind credential swap, and audit as the microVM path. The
// real secret for a swap is resolved host-side and never enters the guest.
type EgressConfig struct {
	// Allow is the default-deny destination allowlist.
	Allow []string
	// Mode is "guarded" (deny inside/infra, allow public) or "strict" (deny any
	// non-allowlisted destination). Empty normalizes to guarded.
	Mode string
	// Swaps is the optional host-indexed credential-swap table. A request to a
	// matching host has the real credential injected host-side (cred-blind).
	Swaps *egress.SwapTable
	// Logger is the required audit sink: every egress decision is recorded here and
	// the guest cannot touch it (ASK tenet 2). The caller owns its lifecycle.
	Logger egress.Logger
	// Limits bounds the governed egress (e.g. MaxTotalBytes caps a response body).
	Limits egress.Limits
	// UpstreamRoots optionally overrides the roots used to verify the real upstream
	// certificate. Nil uses the system pool; it never disables verification.
	UpstreamRoots *x509.CertPool
}

// brain builds the governance brain for this run's egress policy. It fails closed
// on a bad allowlist or a missing audit sink — a sandbox must never run governed
// egress without somewhere to record it.
func (e *EgressConfig) brain() (*egress.Brain, error) {
	if e.Logger == nil {
		return nil, errors.New("sandbox: EgressConfig.Logger is required (egress must be audited)")
	}
	policy, err := egress.NewPolicy(e.Allow)
	if err != nil {
		return nil, err
	}
	b := egress.NewBrain(e.Mode, policy, e.Swaps, e.Logger, e.Limits)
	b.UpstreamRoots = e.UpstreamRoots
	return b, nil
}

// wireRequest / wireResponse are the JSON envelope the guest and host exchange
// over linear memory. Body is base64 in JSON (encoding/json's []byte handling),
// so arbitrary payloads survive the round-trip.
type wireRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

type wireResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
	Denied  bool              `json:"denied,omitempty"`
	Reason  string            `json:"reason,omitempty"`
}

// brainCtxKey carries the per-run brain into the host functions (which are
// installed once per runtime but serve runs with different policies).
type brainCtxKey struct{}

func withBrain(ctx context.Context, b *egress.Brain) context.Context {
	return context.WithValue(ctx, brainCtxKey{}, b)
}

func brainFromContext(ctx context.Context) *egress.Brain {
	b, _ := ctx.Value(brainCtxKey{}).(*egress.Brain)
	return b
}

// egressState is the per-runtime host-fetch state: a stash of completed response
// envelopes keyed by an opaque handle. A two-call protocol (fetch → read_response)
// lets the guest learn the response length before allocating, without the host
// needing a guest-exported allocator.
type egressState struct {
	mu   sync.Mutex
	next atomic.Uint64
	resp map[uint32][]byte
}

func newEgressState() *egressState {
	return &egressState{resp: map[uint32][]byte{}}
}

// install registers the "microagency" host module exporting fetch + read_response.
func (s *egressState) install(ctx context.Context, rt wazero.Runtime) error {
	_, err := rt.NewHostModuleBuilder("microagency").
		NewFunctionBuilder().WithFunc(s.fetch).Export("fetch").
		NewFunctionBuilder().WithFunc(s.readResponse).Export("read_response").
		Instantiate(ctx)
	return err
}

// fetch reads a JSON wireRequest from guest memory, performs the governed
// round-trip through the brain bound to ctx, stashes the JSON wireResponse, and
// returns (handle<<32)|len so the guest can read it back. A zero return is a
// host-side failure (no brain bound, unreadable/!JSON request, marshal failure) —
// fail-closed: the guest gets no response. Handles start at 1, so a successful
// response (even empty-bodied) is always non-zero.
func (s *egressState) fetch(ctx context.Context, mod api.Module, reqPtr, reqLen uint32) uint64 {
	b := brainFromContext(ctx)
	if b == nil {
		return 0 // no governed egress for this run: fail closed
	}
	raw, ok := mod.Memory().Read(reqPtr, reqLen)
	if !ok {
		return 0
	}
	var wreq wireRequest
	if err := json.Unmarshal(raw, &wreq); err != nil {
		return 0
	}
	resp, _ := b.Fetch(ctx, egress.FetchRequest{
		Method: wreq.Method,
		URL:    wreq.URL,
		Header: wreq.Headers,
		Body:   wreq.Body,
	})
	out, err := json.Marshal(wireResponse{
		Status:  resp.Status,
		Headers: resp.Header,
		Body:    resp.Body,
		Denied:  resp.Denied,
		Reason:  resp.Reason,
	})
	if err != nil {
		return 0
	}
	h := s.store(out)
	return (uint64(h) << 32) | uint64(uint32(len(out)))
}

// readResponse copies up to destLen bytes of the stashed response for handle into
// guest memory at destPtr, frees the stash entry, and returns the number of bytes
// copied (or -1 on an unknown handle or a bad memory range). The guest is expected
// to allocate exactly the length fetch returned.
func (s *egressState) readResponse(_ context.Context, mod api.Module, handle, destPtr, destLen uint32) int32 {
	s.mu.Lock()
	data, ok := s.resp[handle]
	if ok {
		delete(s.resp, handle)
	}
	s.mu.Unlock()
	if !ok {
		return -1
	}
	n := uint32(len(data))
	if n > destLen {
		n = destLen
	}
	if !mod.Memory().Write(destPtr, data[:n]) {
		return -1
	}
	return int32(n)
}

func (s *egressState) store(b []byte) uint32 {
	h := uint32(s.next.Add(1))
	s.mu.Lock()
	s.resp[h] = b
	s.mu.Unlock()
	return h
}
