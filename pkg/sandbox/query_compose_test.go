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

// queryGuest is the Tier-2 compute half: a pure, deterministic wasm module that
// reads a dataset from STDIN, runs a group-by aggregation, and emits only a
// compact SUMMARY. It has no network and no credentials — it never makes a
// request and never sees a key. It is stdlib-only on purpose: the substrate is
// engine-AGNOSTIC (a real consumer ships whatever query module it likes).
const queryGuest = `package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

func main() {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Print(` + "`" + `{"error":"read"}` + "`" + `)
		os.Exit(2)
	}
	recs, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
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

// TestSandboxDataQueryCompose proves the Tier-2 headline with the work split the
// way the shapes actually want it: the HOST/orchestrator does the governed,
// cred-blind fetch through the brain (allowlist + swap + audit; the credential is
// injected host-side and never leaves it), then pipes the bytes into a PURE
// COMPUTE sandbox that aggregates them and returns only a summary. The sandbox
// has no network and no credentials; the raw dataset never leaves it. The
// governance guarantee is preserved — it is enforced host-side by the same brain
// — without attaching network or secrets to the weaker software sandbox.
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

	// HOST-SIDE: the orchestrator performs the governed, cred-blind fetch. The real
	// credential is resolved from the host env and injected by the brain; it is in
	// neither the request the orchestrator wrote nor the sandbox.
	policy, err := egress.NewPolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	log := &egress.BufferLogger{}
	brain := egress.NewBrain("mitm", policy, staticSwap(t, "127.0.0.1"), log, egress.Limits{})
	brain.UpstreamRoots = pool
	resp, err := brain.Fetch(context.Background(), egress.FetchRequest{Method: "GET", URL: srv.URL})
	if err != nil {
		t.Fatalf("Brain.Fetch: %v", err)
	}
	if resp.Status != 200 || resp.Denied {
		t.Fatalf("governed fetch failed: status=%d denied=%v", resp.Status, resp.Denied)
	}
	if gotAuth != "Bearer DATA_API_KEY" {
		t.Fatalf("credential was not injected host-side: %q", gotAuth)
	}

	// SANDBOX: pure compute over the fetched bytes — no network, no Egress field, no
	// credential. Only a summary comes back.
	wasm := buildWASIBytes(t, queryGuest)
	res, err := Run(context.Background(), Config{
		Module: wasm,
		Stdin:  strings.NewReader(string(resp.Body)),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("guest exit %d stderr=%q stdout=%q", res.ExitCode, res.Stderr, res.Stdout)
	}

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

	// The credential never reached the sandbox output, and the raw dataset never
	// leaves the sandbox — only the summary does (pass-by-reference).
	if strings.Contains(res.Stdout, "DATA_API_KEY") {
		t.Fatalf("credential leaked into guest output: %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "west,100") || strings.Contains(res.Stdout, "sales") {
		t.Fatalf("raw dataset leaked into the returned summary: %q", res.Stdout)
	}

	// The host-side data access was governed and audited by the brain.
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

func staticSwap(t *testing.T, host string) *egress.SwapTable {
	t.Helper()
	yaml := fmt.Sprintf("swaps:\n  k:\n    type: static\n    domains: [%s]\n    header: Authorization\n    format: 'Bearer {key}'\n    key_ref: env:K\n", host)
	tbl, err := egress.LoadSwapTable([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadSwapTable: %v", err)
	}
	return tbl
}
