package broker

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const liveSecret = "LIVE-SECRET-do-not-leak-0xDEADBEEF"

func resolver(m map[string]string) SecretResolver {
	return func(name string) (string, bool) { s, ok := m[name]; return s, ok }
}

// TestTerminateSwapsReferenceAndTapsPreSwap is the core credential-isolation
// proof: the workload sends only a reference, the upstream receives the live
// secret, the response is relayed, and the tap shows the reference — never the
// secret.
func TestTerminateSwapsReferenceAndTapsPreSwap(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	var tapped TapRecord
	term, err := NewTerminate(upstream.URL, resolver(map[string]string{"anthropic-key": liveSecret}), func(r TapRecord) { tapped = r })
	if err != nil {
		t.Fatal(err)
	}
	term.Client = upstream.Client()
	broker := httptest.NewServer(term)
	defer broker.Close()

	req, _ := http.NewRequest("GET", broker.URL+"/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+RefPrefix+"anthropic-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "ok" || resp.StatusCode != 200 {
		t.Fatalf("response not relayed: %d %q", resp.StatusCode, body)
	}
	// Upstream got the LIVE secret.
	if gotAuth != "Bearer "+liveSecret {
		t.Fatalf("upstream Authorization = %q, want the live secret", gotAuth)
	}
	// Tap got the REFERENCE, pre-swap.
	if want := "Bearer " + RefPrefix + "anthropic-key"; tapped.Headers.Get("Authorization") != want {
		t.Fatalf("tap Authorization = %q, want the reference %q", tapped.Headers.Get("Authorization"), want)
	}
	if tapped.Mode != "terminate" || tapped.Method != "GET" || tapped.Path != "/v1/messages" {
		t.Fatalf("tap metadata = %+v", tapped)
	}
}

// TestPreSwapTapNeverContainsLiveSecret is the invariant, checked against the
// whole serialized tap record: the live secret string appears nowhere in it.
func TestPreSwapTapNeverContainsLiveSecret(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer upstream.Close()

	var tapped TapRecord
	term, _ := NewTerminate(upstream.URL, resolver(map[string]string{"k": liveSecret}), func(r TapRecord) { tapped = r })
	term.Client = upstream.Client()
	broker := httptest.NewServer(term)
	defer broker.Close()

	req, _ := http.NewRequest("POST", broker.URL+"/x", strings.NewReader("body"))
	req.Header.Set("Authorization", "Bearer "+RefPrefix+"k")
	req.Header.Set("X-Api-Key", RefPrefix+"k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	blob, _ := json.Marshal(tapped)
	if strings.Contains(string(blob), liveSecret) {
		t.Fatalf("live secret leaked into the tap record: %s", blob)
	}
}

// TestTerminateFailsClosedOnUnknownRef: an unresolved reference aborts before
// anything is sent upstream.
func TestTerminateFailsClosedOnUnknownRef(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer upstream.Close()

	term, _ := NewTerminate(upstream.URL, resolver(map[string]string{"known": liveSecret}), nil)
	term.Client = upstream.Client()
	broker := httptest.NewServer(term)
	defer broker.Close()

	req, _ := http.NewRequest("GET", broker.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer "+RefPrefix+"unknown")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 fail-closed", resp.StatusCode)
	}
	if called {
		t.Fatal("upstream was called despite an unresolved reference")
	}
}

// TestTerminatePassesNonReferenceHeaders: headers without a reference are
// untouched, and multi-token values swap only the reference.
func TestTerminateSwapMechanics(t *testing.T) {
	term, _ := NewTerminate("https://x.invalid", resolver(map[string]string{"k": "S"}), nil)
	cases := []struct{ in, want string }{
		{"Bearer " + RefPrefix + "k", "Bearer S"},
		{"plain-value", "plain-value"},
		{RefPrefix + "k", "S"},
		{"a " + RefPrefix + "k b", "a S b"},
	}
	for _, c := range cases {
		got, err := term.swap(c.in)
		if err != nil || got != c.want {
			t.Fatalf("swap(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := term.swap("Bearer " + RefPrefix + "missing"); err == nil {
		t.Fatal("unknown reference must error")
	}
}

// TestConnectTunnelsAndTapsDestinationOnly: CONNECT tunnels bytes end-to-end
// and the tap records the destination but no content.
func TestConnectTunnelsAndTapsDestinationOnly(t *testing.T) {
	// A plain TCP echo server stands in for the upstream.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()

	var tapped TapRecord
	broker := httptest.NewServer(Handler(nil, &Connect{OnTap: func(r TapRecord) { tapped = r }}))
	defer broker.Close()

	// Speak CONNECT to the broker at the protocol level.
	brokerHost := strings.TrimPrefix(broker.URL, "http://")
	conn, err := net.DialTimeout("tcp", brokerHost, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "CONNECT "+ln.Addr().String()+" HTTP/1.1\r\nHost: "+ln.Addr().String()+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "200") {
		t.Fatalf("CONNECT status = %q, %v", status, err)
	}
	// consume the blank line terminating the CONNECT response headers
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	// Tunnel is open: echo round-trips through it.
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("tunnel echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want ping", buf)
	}
	if tapped.Mode != "connect" || tapped.Host != ln.Addr().String() {
		t.Fatalf("tap = %+v", tapped)
	}
	if tapped.Path != "" || len(tapped.Headers) != 0 {
		t.Fatalf("CONNECT tap leaked content-ish fields: %+v", tapped)
	}
}
