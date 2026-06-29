package sandbox

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/internal/egress"
)

// queryGuest is the Tier-2 compose in one wasm-native module: it FETCHES a
// dataset through the governed host-fetch capability (Tier 1), runs a
// deterministic group-by aggregation over it IN the sandbox (Tier 0), and emits
// only a compact SUMMARY — the raw dataset never leaves the sandbox
// (pass-by-reference). This is the shape of the headline "cheap deterministic
// data work an agent delegates off-context": the expensive/risky parts (network,
// credentials) stay host-side and governed; the cheap deterministic compute runs
// in the poolable wasm shape; only a small summary returns to the agent context.
//
// It is intentionally written against stdlib only (no engine dependency): the
// substrate is engine-AGNOSTIC. A real consumer ships whatever query module it
// likes — this proves microagent runs+governs it; it does not bundle an engine.
const queryGuest = `package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unsafe"
)

//go:wasmimport microagency fetch
func hostFetch(reqPtr, reqLen uint32) uint64

//go:wasmimport microagency read_response
func hostReadResponse(handle, destPtr, destLen uint32) int32

type resp struct {
	Status int    ` + "`json:\"status\"`" + `
	Body   []byte ` + "`json:\"body,omitempty\"`" + `
	Denied bool   ` + "`json:\"denied,omitempty\"`" + `
}

func ptr(b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

func fetch(url string) (resp, bool) {
	rb, _ := json.Marshal(map[string]string{"method": "GET", "url": url})
	packed := hostFetch(ptr(rb), uint32(len(rb)))
	runtime.KeepAlive(rb)
	if packed == 0 {
		return resp{}, false
	}
	handle := uint32(packed >> 32)
	length := uint32(packed)
	buf := make([]byte, length)
	n := hostReadResponse(handle, ptr(buf), length)
	runtime.KeepAlive(buf)
	if n < 0 {
		return resp{}, false
	}
	var out resp
	if json.Unmarshal(buf[:n], &out) != nil {
		return resp{}, false
	}
	return out, true
}

func main() {
	r, ok := fetch(os.Getenv("QUERY_URL"))
	if !ok || r.Denied || r.Status != 200 {
		fmt.Print(` + "`" + `{"error":"fetch"}` + "`" + `)
		os.Exit(2)
	}
	recs, err := csv.NewReader(strings.NewReader(string(r.Body))).ReadAll()
	if err != nil || len(recs) < 2 {
		fmt.Print(` + "`" + `{"error":"parse"}` + "`" + `)
		os.Exit(3)
	}
	// header row is recs[0]; group by col 0, sum col 1.
	sums := map[string]float64{}
	for _, row := range recs[1:] {
		if len(row) < 2 {
			continue
		}
		v, _ := strconv.ParseFloat(strings.TrimSpace(row[1]), 64)
		sums[row[0]] += v
	}
	type g struct {
		Key string
		Sum float64
	}
	var gs []g
	for k, v := range sums {
		gs = append(gs, g{k, v})
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].Sum > gs[j].Sum })
	top := g{}
	if len(gs) > 0 {
		top = gs[0]
	}
	out, _ := json.Marshal(map[string]any{
		"rows_in": len(recs) - 1,
		"groups":  len(gs),
		"top":     top.Key,
		"top_sum": top.Sum,
	})
	fmt.Print(string(out))
}
`

// TestSandboxDataQueryCompose proves the Tier-2 headline end-to-end: a wasm
// module fetches an access-controlled dataset (the credential is injected
// host-side, cred-blind), aggregates it deterministically in the sandbox, and
// returns ONLY a summary — the raw data never reaches the caller's context.
func TestSandboxDataQueryCompose(t *testing.T) {
	t.Setenv("K", "DATA_API_KEY")
	const dataset = "region,sales\nwest,100\neast,50\nwest,200\neast,75\nnorth,25\n"

	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, dataset)
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	wasm := buildWASIBytes(t, queryGuest)
	log := &egress.BufferLogger{}
	res, err := Run(context.Background(), Config{
		Module: wasm,
		Env:    map[string]string{"QUERY_URL": srv.URL}, // dataset URL only; the API key (K) is NOT given to the guest
		Egress: &EgressConfig{
			Allow:         []string{"127.0.0.1"},
			Mode:          "strict",
			Swaps:         staticSwap(t, "127.0.0.1"),
			Logger:        log,
			UpstreamRoots: pool,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("guest exit %d stderr=%q stdout=%q", res.ExitCode, res.Stderr, res.Stdout)
	}

	// The deterministic query result: west=300, east=125, north=25.
	var summary struct {
		RowsIn int     `json:"rows_in"`
		Groups int     `json:"groups"`
		Top    string  `json:"top"`
		TopSum float64 `json:"top_sum"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &summary); err != nil {
		t.Fatalf("summary not JSON: %q (%v)", res.Stdout, err)
	}
	if summary.RowsIn != 5 || summary.Groups != 3 || summary.Top != "west" || summary.TopSum != 300 {
		t.Fatalf("wrong aggregation: %+v", summary)
	}

	// Cred-blind: the host injected the real key; the guest never saw it.
	if gotAuth != "Bearer DATA_API_KEY" {
		t.Fatalf("upstream did not receive the host-injected credential: %q", gotAuth)
	}
	if strings.Contains(res.Stdout, "DATA_API_KEY") {
		t.Fatalf("credential leaked into guest output: %q", res.Stdout)
	}

	// Pass-by-reference: only the summary leaves the sandbox — the raw dataset rows
	// never appear in what returns to the caller's context.
	if strings.Contains(res.Stdout, "west,100") || strings.Contains(res.Stdout, "sales") {
		t.Fatalf("raw dataset leaked into the returned summary: %q", res.Stdout)
	}

	// And the egress was audited (the agent's data access is observable).
	var sawSwap, sawAllow bool
	for _, e := range log.Snapshot() {
		switch e["event"] {
		case "egress_swap":
			sawSwap = true
		case "egress_allow":
			sawAllow = e["shape"] == "fetch"
		}
	}
	if !sawSwap || !sawAllow {
		t.Fatalf("data access not fully audited: swap=%v allow=%v", sawSwap, sawAllow)
	}
}
