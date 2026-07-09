package broker

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInjectedCredentialAbsentFromAllEmissions is the S-invariant for the
// whole package: drive every datapath — swap success, unresolved reference,
// policy deny, upstream failure — with every sink wired (tap, decision,
// capture) and capture enabled, then scan every byte each sink received and
// every response the workload got. The operator-provisioned live secret must
// appear nowhere: it is absent by construction (all emissions are pre-swap),
// not by redaction. The reference name is what must appear instead.
func TestInjectedCredentialAbsentFromAllEmissions(t *testing.T) {
	var upstreamSawLive bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), liveSecret) {
			upstreamSawLive = true
		}
		// Worst-case upstream: echoes the auth header back in the response.
		w.WriteHeader(400)
		_, _ = io.WriteString(w, "invalid key: "+r.Header.Get("Authorization"))
	}))
	defer upstream.Close()

	// Everything any sink ever receives is appended here, JSON-serialized —
	// the haystack the live secret must not be found in.
	var emissions []string
	collect := func(v any) {
		blob, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal emission: %v", err)
		}
		emissions = append(emissions, string(blob))
	}

	newTerm := func(upstreamURL string, denyRule string) *Terminate {
		term, err := NewTerminate(upstreamURL, resolver(map[string]string{"api-key": liveSecret}), func(r TapRecord) { collect(r) })
		if err != nil {
			t.Fatal(err)
		}
		term.Client = upstream.Client()
		term.OnDecision = func(r DecisionRecord) { collect(r) }
		term.OnCapture = func(r CaptureRecord) { collect(r) }
		if denyRule != "" {
			term.Policy = func(TapRecord) (Verdict, error) { return Verdict{Allow: false, Rule: denyRule}, nil }
		}
		return term
	}

	// brokerText: when the response is broker-generated (deny/error paths),
	// its text is a broker emission and joins the haystack. A RELAYED
	// response is the upstream's answer to the workload — the workload is
	// entitled to it (here it deliberately echoes the live secret), and it is
	// exactly why responses are never captured; it is not a broker sink.
	send := func(h http.Handler, hdr map[string]string, body string, brokerText bool) {
		srv := httptest.NewServer(h)
		defer srv.Close()
		req, _ := http.NewRequest("POST", srv.URL+"/v1/messages", strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if brokerText {
			collect(resp.Header)
			emissions = append(emissions, string(respBody))
		}
	}

	ref := "Bearer " + RefPrefix + "api-key"
	// 1. Successful swap — upstream sees the live secret and even echoes it
	//    back at the workload; no broker sink may hold it.
	send(newTerm(upstream.URL, ""), map[string]string{"Authorization": ref, "X-Api-Key": RefPrefix + "api-key"}, "request body", false)
	// 2. Unresolved reference (fail-closed deny; broker-generated 502).
	send(newTerm(upstream.URL, ""), map[string]string{"Authorization": "Bearer " + RefPrefix + "nope"}, "", true)
	// 3. Policy deny (broker-generated 403).
	send(newTerm(upstream.URL, "blocked"), map[string]string{"Authorization": ref}, "denied body", true)
	// 4. Upstream failure (broker-generated 502).
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close()
	send(newTerm(dead.URL, ""), map[string]string{"Authorization": ref}, "", true)

	if !upstreamSawLive {
		t.Fatal("sanity: the upstream never received the live secret — the swap path was not exercised")
	}
	all := strings.Join(emissions, "\n")
	if strings.Contains(all, liveSecret) {
		t.Fatalf("the live secret appeared in a broker emission:\n%s", all)
	}
	if !strings.Contains(all, RefPrefix+"api-key") {
		t.Fatal("the reference never appeared in any emission — capture/tap did not run pre-swap")
	}
}
