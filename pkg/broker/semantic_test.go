package broker

import (
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func semanticGrant() *vmkit.BrokerGrant {
	return &vmkit.BrokerGrant{Operations: []vmkit.BrokerOperationGrant{
		{
			Name: "read-repository", Effect: vmkit.BrokerEffectRead,
			Method: http.MethodGet, Route: "/repos/{owner}/{repo}",
			PathParameters: map[string][]string{"owner": {"acme"}, "repo": {"widgets"}},
			Query:          []vmkit.BrokerValueGrant{{Name: "view", Required: true, Values: []string{"summary"}}},
			Headers:        []vmkit.BrokerValueGrant{{Name: "Authorization", Required: true, Pattern: `Bearer @secret:[A-Za-z0-9._/-]+`, MaxBytes: 128}},
			Response: vmkit.BrokerResponseGrant{
				Statuses: []int{200}, ContentTypes: []string{"application/json"}, MaxBytes: 128,
				CredentialDisclosure: "deny-exact",
				JSON:                 &vmkit.BrokerJSONSchema{Type: "object", Properties: map[string]string{"name": "string"}, Required: []string{"name"}},
			},
		},
		{
			Name: "write-repository", Effect: vmkit.BrokerEffectWrite,
			Method: http.MethodPost, Route: "/repos/{owner}/{repo}/issues",
			PathParameters: map[string][]string{"owner": {"acme"}, "repo": {"widgets"}},
			Headers: []vmkit.BrokerValueGrant{
				{Name: "Authorization", Required: true, Pattern: `Bearer @secret:[A-Za-z0-9._/-]+`, MaxBytes: 128},
				{Name: "Content-Type", Required: true, Values: []string{"application/json"}},
			},
			Body: &vmkit.BrokerBodyGrant{MaxBytes: 64, ContentTypes: []string{"application/json"}, JSON: &vmkit.BrokerJSONSchema{
				Type: "object", Properties: map[string]string{"title": "string"}, Required: []string{"title"},
			}},
			Response: vmkit.BrokerResponseGrant{Statuses: []int{200}, ContentTypes: []string{"application/json"}, MaxBytes: 128, CredentialDisclosure: "deny-exact", JSON: &vmkit.BrokerJSONSchema{Type: "object", Properties: map[string]string{"name": "string"}, Required: []string{"name"}}},
		},
	}}
}

func semanticHandler(t *testing.T, upstream *httptest.Server, grant *vmkit.BrokerGrant) *Terminate {
	t.Helper()
	term, err := NewSemanticTerminate(upstream.URL, resolver(map[string]string{"api": liveSecret}), nil, grant)
	if err != nil {
		t.Fatal(err)
	}
	term.Client = upstream.Client()
	return term
}

func sendSemantic(t *testing.T, term http.Handler, method, target, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+RefPrefix+"api")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	term.ServeHTTP(rr, req)
	return rr.Code, rr.Body.String()
}

func TestSemanticGrantAllowsDeclaredGET(t *testing.T) {
	var sawCredential bool
	var sawImplicitAgentHeader bool
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawCredential = r.Header.Get("Authorization") == "Bearer "+liveSecret
		sawImplicitAgentHeader = r.Header.Get("User-Agent") != "" || r.Header.Get("Accept-Encoding") != ""
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"widgets"}`)
	}))
	defer upstream.Close()
	term := semanticHandler(t, upstream, semanticGrant())
	var decision DecisionRecord
	term.OnDecision = func(record DecisionRecord) { decision = record }
	status, body := sendSemantic(t, term, http.MethodGet, "http://broker/repos/acme/widgets?view=summary", "")
	if status != http.StatusOK || body != `{"name":"widgets"}` || !sawCredential || sawImplicitAgentHeader || decision.Assurance != "semantic" || decision.Operation != "read-repository" || decision.Effect != "read" {
		t.Fatalf("declared GET = %d %q, credential=%v implicit_header=%v decision=%+v", status, body, sawCredential, sawImplicitAgentHeader, decision)
	}
}

func TestSemanticGrantRejectsBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"unexpected"}`)
	}))
	defer upstream.Close()
	term := semanticHandler(t, upstream, semanticGrant())
	cases := []struct{ name, method, target, body string }{
		{"method", http.MethodDelete, "http://broker/repos/acme/widgets", ""},
		{"route", http.MethodGet, "http://broker/admin", ""},
		{"namespace", http.MethodGet, "http://broker/repos/attacker/widgets", ""},
		{"query key", http.MethodGet, "http://broker/repos/acme/widgets?url=x", ""},
		{"missing required query", http.MethodGet, "http://broker/repos/acme/widgets", ""},
		{"repeated query", http.MethodGet, "http://broker/repos/acme/widgets?view=summary&view=summary", ""},
		{"query URL fetch", http.MethodGet, "http://broker/repos/acme/widgets?view=https://169.254.169.254/latest", ""},
		{"encoded query URL fetch", http.MethodGet, "http://broker/repos/acme/widgets?view=https%253A%252F%252F169.254.169.254%252Flatest", ""},
		{"write namespace", http.MethodPost, "http://broker/repos/attacker/widgets/issues", `{"title":"x"}`},
		{"oversized body", http.MethodPost, "http://broker/repos/acme/widgets/issues", `{"title":"` + strings.Repeat("x", 80) + `"}`},
		{"body schema", http.MethodPost, "http://broker/repos/acme/widgets/issues", `{"body":"missing title"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := sendSemantic(t, term, tc.method, tc.target, tc.body)
			if status != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", status)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream received %d denied requests", calls.Load())
	}
}

func TestSemanticRedirectReauthorizedBeforeNextHop(t *testing.T) {
	var escaped atomic.Bool
	escape := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escaped.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"escaped"}`)
	}))
	defer escape.Close()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, escape.URL+"/repos/acme/widgets?view=summary", http.StatusFound)
	}))
	defer upstream.Close()
	grant := semanticGrant()
	grant.Redirects = vmkit.BrokerRedirectGrant{Allow: true, MaxHops: 2}
	term := semanticHandler(t, upstream, grant)
	var decision DecisionRecord
	term.OnDecision = func(record DecisionRecord) { decision = record }
	status, _ := sendSemantic(t, term, http.MethodGet, "http://broker/repos/acme/widgets?view=summary", "")
	if status != http.StatusForbidden || escaped.Load() || decision.Rule != "semantic-redirect-deny" || decision.Operation != "read-repository" || decision.Effect != "read" {
		t.Fatalf("redirect escape status=%d reached=%v decision=%+v, want blocked before hop", status, escaped.Load(), decision)
	}
}

func TestSemanticRedirectUsesFinalOperationAndResponseContract(t *testing.T) {
	grant := semanticGrant()
	final := grant.Operations[0]
	final.Name = "read-final"
	final.Route = "/repos/{owner}/{repo}/final"
	final.Response.JSON = &vmkit.BrokerJSONSchema{Type: "object", Properties: map[string]string{"final": "boolean"}, Required: []string{"final"}}
	grant.Operations = append(grant.Operations, final)
	grant.Redirects = vmkit.BrokerRedirectGrant{Allow: true, MaxHops: 1}
	var upstream *httptest.Server
	upstream = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/final") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"final":true}`)
			return
		}
		http.Redirect(w, r, upstream.URL+"/repos/acme/widgets/final?view=summary", http.StatusFound)
	}))
	defer upstream.Close()
	term := semanticHandler(t, upstream, grant)
	var decision DecisionRecord
	term.OnDecision = func(record DecisionRecord) { decision = record }
	status, body := sendSemantic(t, term, http.MethodGet, "http://broker/repos/acme/widgets?view=summary", "")
	if status != http.StatusOK || body != `{"final":true}` || decision.Operation != "read-repository" || decision.RedirectHops != 1 || decision.FinalHost == "" || decision.FinalOperation != "read-final" || decision.FinalEffect != "read" {
		t.Fatalf("authorized redirect = %d %q decision=%+v", status, body, decision)
	}
}

func TestSemanticResponseRejectedBeforeAnyByteReachesGuest(t *testing.T) {
	cases := []struct {
		name  string
		serve func(http.ResponseWriter)
	}{
		{"size", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"`+strings.Repeat("x", 150)+`"}`)
		}},
		{"content type", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "no")
		}},
		{"schema", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"unexpected":true}`)
		}},
		{"status", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"name":"x"}`)
		}},
		{"credential header", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Credential-Echo", liveSecret)
			_, _ = io.WriteString(w, `{"name":"x"}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tc.serve(w) }))
			defer upstream.Close()
			term := semanticHandler(t, upstream, semanticGrant())
			var decision DecisionRecord
			term.OnDecision = func(record DecisionRecord) { decision = record }
			status, body := sendSemantic(t, term, http.MethodGet, "http://broker/repos/acme/widgets?view=summary", "")
			if status != http.StatusBadGateway || strings.Contains(body, "unexpected") || strings.Contains(body, strings.Repeat("x", 20)) {
				t.Fatalf("unapproved response reached guest: %d %q", status, body)
			}
			if decision.Event != EventRequestDeny || decision.Operation != "read-repository" || decision.Effect != "read" || !strings.HasPrefix(decision.Rule, "semantic-response-") {
				t.Fatalf("response denial lost semantic audit metadata: %+v", decision)
			}
		})
	}
}

func TestSemanticResponseDeniesCredentialSplitAcrossChunks(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"`+liveSecret[:len(liveSecret)/2])
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, liveSecret[len(liveSecret)/2:]+`"}`)
	}))
	defer upstream.Close()
	status, body := sendSemantic(t, semanticHandler(t, upstream, semanticGrant()), http.MethodGet, "http://broker/repos/acme/widgets?view=summary", "")
	if status != http.StatusBadGateway || strings.Contains(body, liveSecret) {
		t.Fatalf("chunked credential echo reached guest: %d %q", status, body)
	}
}

func TestSemanticCredentialContractIsExactNotTransformationProof(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(liveSecret))
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"`+encoded+`"}`)
	}))
	defer upstream.Close()
	status, body := sendSemantic(t, semanticHandler(t, upstream, semanticGrant()), http.MethodGet, "http://broker/repos/acme/widgets?view=summary", "")
	if status != http.StatusOK || !strings.Contains(body, encoded) {
		t.Fatalf("deny-exact unexpectedly claimed transformed-secret detection: %d %q", status, body)
	}
}

func TestEndpointServerRefusesImplicitBroadOrIncompleteSemanticEndpoint(t *testing.T) {
	for _, endpoint := range []*vmkit.BrokerConfig{
		{Upstream: "https://api.example.com"},
		{Upstream: "https://api.example.com", Assurance: vmkit.BrokerAssuranceSemantic},
	} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		err = StartEndpointServer(listener, EndpointServerOptions{Endpoint: endpoint})
		_ = listener.Close()
		if err == nil {
			t.Fatalf("StartEndpointServer accepted endpoint %+v", endpoint)
		}
	}
}

func TestSemanticResponseChecksTheExactResolvedValue(t *testing.T) {
	var resolves atomic.Int32
	first := "first-live-secret"
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"`+first+`"}`)
	}))
	defer upstream.Close()
	term, err := NewSemanticTerminate(upstream.URL, func(string) (string, bool) {
		if resolves.Add(1) == 1 {
			return first, true
		}
		return "rotated-secret", true
	}, nil, semanticGrant())
	if err != nil {
		t.Fatal(err)
	}
	term.Client = upstream.Client()
	status, body := sendSemantic(t, term, http.MethodGet, "http://broker/repos/acme/widgets?view=summary", "")
	if status != http.StatusBadGateway || strings.Contains(body, first) || resolves.Load() != 1 {
		t.Fatalf("resolved credential response check = status %d body %q resolves %d", status, body, resolves.Load())
	}
}

func TestSemanticEndpointHandlerRejectsMismatchedLowerAssuranceHandler(t *testing.T) {
	term, err := NewTerminate("https://api.example.com", resolver(map[string]string{"api": liveSecret}), nil)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	EndpointHandler(&vmkit.BrokerConfig{Assurance: vmkit.BrokerAssuranceSemantic}, term, nil, nil).
		ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://broker/", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("mismatched semantic handler status = %d, want 503", rr.Code)
	}
}

func TestSemanticConstructorNeverCarriesNilAllowAllPolicy(t *testing.T) {
	term, err := NewSemanticTerminate("https://api.example.com", resolver(map[string]string{"api": liveSecret}), nil, semanticGrant())
	if err != nil {
		t.Fatal(err)
	}
	if term.Policy == nil || evaluate(term.Policy, TapRecord{}).Allow {
		t.Fatal("semantic endpoint retained the legacy nil/allow policy seam")
	}
}

func TestTrustedUpstreamAssuranceIsReported(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	term, _ := NewTerminate(upstream.URL, resolver(map[string]string{"api": liveSecret}), nil)
	term.Client = upstream.Client()
	term.Assurance = vmkit.BrokerAssuranceTrustedUpstream
	var decision DecisionRecord
	term.OnDecision = func(record DecisionRecord) { decision = record }
	status, _ := sendSemantic(t, term, http.MethodGet, "http://broker/anything", "")
	if status != http.StatusOK || decision.Assurance != "trusted-upstream" {
		t.Fatalf("status=%d assurance=%q labels=%v, lower assurance was not reported", status, decision.Assurance, decision.Labels)
	}
}
