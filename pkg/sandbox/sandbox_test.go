package sandbox

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
)

// buildWASIModule compiles a Go program to wasm32-wasip1 (stdlib only → offline)
// and returns the .wasm path. Skips if the toolchain can't target wasip1.
func buildWASIModule(t *testing.T, mainSrc string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(mainSrc), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module wasitest\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	out := filepath.Join(dir, "module.wasm")
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "GOFLAGS=-mod=mod")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build wasip1 module (toolchain unavailable?): %v\n%s", err, combined)
	}
	return out
}

// ---- Proof A: the egress brain serves a HOST-SUPPLIED destination ----
// The Handler is the brain (allowlist + audit + forward). It's transport-
// agnostic via an injectable OrigDst, so we drive it directly over an in-memory
// conn with the destination supplied by the host — no netns, no TPROXY, no
// SO_ORIGINAL_DST. This is what makes the brain reusable by a wasm host-dial.
func TestEgressBrainServesHostSuppliedDst(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "UPSTREAM-OK")
	}))
	defer upstream.Close()
	upAddr := netip.MustParseAddrPort(strings.TrimPrefix(upstream.URL, "http://"))

	logger := &egress.BufferLogger{}
	policy, err := egress.NewPolicy([]string{"allowed.example"})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	newHandler := func(dst netip.AddrPort) *egress.Handler {
		return &egress.Handler{
			Mode:    "strict",
			Policy:  policy,
			Logger:  logger,
			OrigDst: func(net.Conn) (netip.AddrPort, error) { return dst, nil },
			Dial:    net.Dial,
		}
	}

	t.Run("allow_forwards_to_upstream", func(t *testing.T) {
		client, server := net.Pipe()
		go newHandler(upAddr).Handle(server)
		_ = client.SetDeadline(time.Now().Add(5 * time.Second))
		go func() {
			_, _ = client.Write([]byte("GET / HTTP/1.1\r\nHost: allowed.example\r\nConnection: close\r\n\r\n"))
		}()
		body, _ := io.ReadAll(client)
		if !strings.Contains(string(body), "UPSTREAM-OK") {
			t.Fatalf("allowlisted request did not reach upstream over host-supplied dst: %q", string(body))
		}
	})

	t.Run("deny_blocks_and_audits", func(t *testing.T) {
		client, server := net.Pipe()
		go newHandler(upAddr).Handle(server) // dst irrelevant: denied on host
		_ = client.SetDeadline(time.Now().Add(5 * time.Second))
		go func() {
			_, _ = client.Write([]byte("GET / HTTP/1.1\r\nHost: blocked.example\r\nConnection: close\r\n\r\n"))
		}()
		_, _ = io.ReadAll(client) // denied → conn closed, no upstream dial
		found := false
		for _, e := range logger.Snapshot() {
			if e["event"] == "egress_deny" && e["host"] == "blocked.example" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected an egress_deny audit for blocked.example; got %v", logger.Snapshot())
		}
	})
}

// ---- Proof B: a wasm module's egress is governed by that same brain ----
// The sandbox primitive gives the guest a single privileged import,
// microagency.egress_allowed, backed by internal/egress.Policy + audit. The
// guest has no sockets; its only way to reach the network is to ask the brain.
func TestSandboxEgressGovernedByBrain(t *testing.T) {
	wasm := buildWASIModule(t, `package main

import (
	"os"
	"runtime"
	"unsafe"
)

//go:wasmimport microagency egress_allowed
func egressAllowed(ptr uint32, length uint32) int32

func allowed(host string) int32 {
	b := []byte(host)
	r := egressAllowed(uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b)))
	runtime.KeepAlive(b)
	return r
}

func main() {
	// Exit 0 only if the brain allows the allowlisted host and denies the other.
	if allowed("allowed.example") == 1 && allowed("blocked.example") == 0 {
		os.Exit(0)
	}
	os.Exit(3)
}
`)

	logger := &egress.BufferLogger{}
	res, err := Run(context.Background(), Config{
		WasmPath: wasm,
		Egress:   &EgressConfig{Allow: []string{"allowed.example"}, Logger: logger},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("guest disagreed with the brain (exit %d): the allowlist decision did not reach the module", res.ExitCode)
	}

	// The brain must have recorded BOTH decisions (audit completeness).
	var sawAllow, sawDeny bool
	for _, e := range logger.Snapshot() {
		if e["event"] != "sandbox_egress_decision" {
			continue
		}
		switch e["host"] {
		case "allowed.example":
			sawAllow = e["allow"] == true
		case "blocked.example":
			sawDeny = e["allow"] == false
		}
	}
	if !sawAllow || !sawDeny {
		t.Fatalf("audit incomplete: sawAllow=%v sawDeny=%v events=%v", sawAllow, sawDeny, logger.Snapshot())
	}
}

// TestSandboxNoEgressCapabilityByDefault: with no EgressConfig the module has no
// egress import at all — a guest that tries to call it fails to instantiate
// (fail-closed by absence), never silently reaching the network.
func TestSandboxNoEgressCapabilityByDefault(t *testing.T) {
	wasm := buildWASIModule(t, `package main

//go:wasmimport microagency egress_allowed
func egressAllowed(ptr uint32, length uint32) int32

func main() { _ = egressAllowed(0, 0) }
`)
	_, err := Run(context.Background(), Config{WasmPath: wasm}) // no Egress
	if err == nil {
		t.Fatal("expected instantiation to fail with no egress capability provided")
	}
}
