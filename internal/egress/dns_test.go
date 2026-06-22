package egress

import (
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// buildQuery constructs a minimal DNS query (one question) for testing the
// parse/forward path. Returns the wire bytes.
func buildQuery(t *testing.T, id uint16, name string, typ dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:               id,
		RecursionDesired: true,
	})
	if err := b.StartQuestions(); err != nil {
		t.Fatalf("StartQuestions: %v", err)
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name),
		Type:  typ,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatalf("Question: %v", err)
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return msg
}

// buildResponseWithA constructs a DNS response echoing one question and
// carrying a single A answer record (name, ip, ttl). Returns the wire bytes.
func buildResponseWithA(t *testing.T, id uint16, qname, aname string, ip [4]byte, ttl uint32) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 id,
		Response:           true,
		RecursionAvailable: true,
	})
	if err := b.StartQuestions(); err != nil {
		t.Fatalf("StartQuestions: %v", err)
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(qname),
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatalf("Question: %v", err)
	}
	if err := b.StartAnswers(); err != nil {
		t.Fatalf("StartAnswers: %v", err)
	}
	if err := b.AResource(dnsmessage.ResourceHeader{
		Name:  dnsmessage.MustNewName(aname),
		Class: dnsmessage.ClassINET,
		TTL:   ttl,
	}, dnsmessage.AResource{A: ip}); err != nil {
		t.Fatalf("AResource: %v", err)
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return msg
}

func TestParseDNSQuestion(t *testing.T) {
	query := buildQuery(t, 0x1234, "API.example.com.", dnsmessage.TypeA)

	id, qname, qtype, err := parseDNSQuestion(query)
	if err != nil {
		t.Fatalf("parseDNSQuestion: %v", err)
	}
	if id != 0x1234 {
		t.Errorf("id = %#x, want %#x", id, 0x1234)
	}
	if qname != "api.example.com" {
		t.Errorf("qname = %q, want normalized %q", qname, "api.example.com")
	}
	if qtype != dnsmessage.TypeA {
		t.Errorf("qtype = %v, want %v", qtype, dnsmessage.TypeA)
	}
}

func TestParseDNSQuestionRejectsGarbage(t *testing.T) {
	if _, _, _, err := parseDNSQuestion([]byte{0x00, 0x01, 0x02}); err == nil {
		t.Fatal("parseDNSQuestion(garbage) err=nil, want error")
	}
}

func TestSynthesizeRefused(t *testing.T) {
	query := buildQuery(t, 0xABCD, "blocked.example.com.", dnsmessage.TypeA)

	resp, err := synthesizeRefused(query)
	if err != nil {
		t.Fatalf("synthesizeRefused: %v", err)
	}

	var p dnsmessage.Parser
	hdr, err := p.Start(resp)
	if err != nil {
		t.Fatalf("parse refused response: %v", err)
	}
	if !hdr.Response {
		t.Error("Response (QR) = false, want true")
	}
	if hdr.RCode != dnsmessage.RCodeRefused {
		t.Errorf("RCode = %v, want %v", hdr.RCode, dnsmessage.RCodeRefused)
	}
	if hdr.ID != 0xABCD {
		t.Errorf("ID = %#x, want %#x", hdr.ID, 0xABCD)
	}
	// The question must be echoed back so a stub resolver matches it.
	q, err := p.Question()
	if err != nil {
		t.Fatalf("parse echoed question: %v", err)
	}
	if got := normalizeHost(q.Name.String()); got != "blocked.example.com" {
		t.Errorf("echoed question name = %q, want %q", got, "blocked.example.com")
	}
	if q.Type != dnsmessage.TypeA {
		t.Errorf("echoed question type = %v, want %v", q.Type, dnsmessage.TypeA)
	}
	// No answers in a REFUSED response.
	if err := p.SkipAllQuestions(); err != nil {
		t.Fatalf("SkipAllQuestions: %v", err)
	}
	answers, err := p.AllAnswers()
	if err != nil {
		t.Fatalf("AllAnswers: %v", err)
	}
	if len(answers) != 0 {
		t.Errorf("answers = %d, want 0", len(answers))
	}
}

func TestCacheDNSAnswers(t *testing.T) {
	cache := NewNameCache()
	// Response with a CNAME-style different answer name; we cache under qname.
	resp := buildResponseWithA(t, 1, "api.example.com.", "cdn.example.net.",
		[4]byte{203, 0, 113, 7}, 300)

	cacheDNSAnswers(cache, "api.example.com", resp)

	host, ok := cache.HostForIP(netip.AddrFrom4([4]byte{203, 0, 113, 7}))
	if !ok {
		t.Fatal("HostForIP ok=false, want true")
	}
	if host != "api.example.com" {
		t.Errorf("HostForIP = %q, want %q (cached under qname)", host, "api.example.com")
	}
}

func TestCacheDNSAnswersZeroTTLSkipped(t *testing.T) {
	cache := NewNameCache()
	resp := buildResponseWithA(t, 1, "api.example.com.", "api.example.com.",
		[4]byte{203, 0, 113, 9}, 0)
	cacheDNSAnswers(cache, "api.example.com", resp)
	if _, ok := cache.HostForIP(netip.AddrFrom4([4]byte{203, 0, 113, 9})); ok {
		t.Error("zero-TTL A record cached; want skipped")
	}
}

func TestHandleDNSStrictDeniesNonAllowlisted(t *testing.T) {
	pol, _ := NewPolicy([]string{"allowed.example.com"})
	log := &BufferLogger{}
	h := &Handler{Mode: "strict", Policy: pol, Logger: log, NameCache: NewNameCache()}

	forwardCalled := false
	forward := func(netip.AddrPort, []byte) ([]byte, error) {
		forwardCalled = true
		return nil, nil
	}

	query := buildQuery(t, 0x0001, "blocked.example.com.", dnsmessage.TypeA)
	resolver := netip.MustParseAddrPort("1.1.1.1:53")

	resp, err := h.handleDNS(query, resolver, forward)
	if err != nil {
		t.Fatalf("handleDNS: %v", err)
	}
	if forwardCalled {
		t.Error("forward was called for a denied query; want NOT forwarded")
	}
	// Response must be a synthesized REFUSED.
	var p dnsmessage.Parser
	hdr, err := p.Start(resp)
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if hdr.RCode != dnsmessage.RCodeRefused {
		t.Errorf("RCode = %v, want %v", hdr.RCode, dnsmessage.RCodeRefused)
	}
	assertEvent(t, log, "egress_dns_deny")
}

func TestHandleDNSStrictAllowsAndCaches(t *testing.T) {
	pol, _ := NewPolicy([]string{"allowed.example.com"})
	log := &BufferLogger{}
	h := &Handler{Mode: "strict", Policy: pol, Logger: log, NameCache: NewNameCache()}

	want := buildResponseWithA(t, 0x0002, "allowed.example.com.", "allowed.example.com.",
		[4]byte{203, 0, 113, 7}, 300)
	forwardCalled := false
	forward := func(r netip.AddrPort, q []byte) ([]byte, error) {
		forwardCalled = true
		return want, nil
	}

	query := buildQuery(t, 0x0002, "allowed.example.com.", dnsmessage.TypeA)
	resolver := netip.MustParseAddrPort("1.1.1.1:53")

	resp, err := h.handleDNS(query, resolver, forward)
	if err != nil {
		t.Fatalf("handleDNS: %v", err)
	}
	if !forwardCalled {
		t.Error("forward was NOT called for an allowlisted query; want forwarded")
	}
	if string(resp) != string(want) {
		t.Error("handleDNS did not return the forwarded response verbatim")
	}
	host, ok := h.NameCache.HostForIP(netip.AddrFrom4([4]byte{203, 0, 113, 7}))
	if !ok || host != "allowed.example.com" {
		t.Errorf("HostForIP = (%q,%v), want (%q,true)", host, ok, "allowed.example.com")
	}
	assertEvent(t, log, "egress_dns_allow")
	// Allowlisted (not mediated-only): must NOT be marked unlisted.
	assertEventFieldAbsent(t, log, "egress_dns_allow", "unlisted")
}

func TestHandleDNSMediatedForwardsAndCachesUnlisted(t *testing.T) {
	pol, _ := NewPolicy([]string{"unrelated.example.com"})
	log := &BufferLogger{}
	h := &Handler{Mode: "mediated", Policy: pol, Logger: log, NameCache: NewNameCache()}

	want := buildResponseWithA(t, 0x0003, "whatever.example.com.", "whatever.example.com.",
		[4]byte{198, 51, 100, 4}, 120)
	forwardCalled := false
	forward := func(r netip.AddrPort, q []byte) ([]byte, error) {
		forwardCalled = true
		return want, nil
	}

	query := buildQuery(t, 0x0003, "whatever.example.com.", dnsmessage.TypeA)
	resolver := netip.MustParseAddrPort("8.8.8.8:53")

	resp, err := h.handleDNS(query, resolver, forward)
	if err != nil {
		t.Fatalf("handleDNS: %v", err)
	}
	if !forwardCalled {
		t.Error("forward was NOT called in mediated mode; want forwarded")
	}
	if string(resp) != string(want) {
		t.Error("handleDNS did not return the forwarded response verbatim")
	}
	host, ok := h.NameCache.HostForIP(netip.AddrFrom4([4]byte{198, 51, 100, 4}))
	if !ok || host != "whatever.example.com" {
		t.Errorf("HostForIP = (%q,%v), want (%q,true)", host, ok, "whatever.example.com")
	}
	// Allowed only via mediated mode (not on the allowlist): unlisted:true.
	assertEventWithField(t, log, "egress_dns_allow", "unlisted", true)
}

func TestHandleDNSForwardErrorAudited(t *testing.T) {
	pol, _ := NewPolicy([]string{"allowed.example.com"})
	log := &BufferLogger{}
	h := &Handler{Mode: "strict", Policy: pol, Logger: log, NameCache: NewNameCache()}

	forward := func(netip.AddrPort, []byte) ([]byte, error) {
		return nil, errTestForward
	}
	query := buildQuery(t, 0x0004, "allowed.example.com.", dnsmessage.TypeA)
	resolver := netip.MustParseAddrPort("1.1.1.1:53")

	if _, err := h.handleDNS(query, resolver, forward); err == nil {
		t.Fatal("handleDNS err=nil on forward failure, want error")
	}
	assertEvent(t, log, "egress_dns_error")
}

func TestHandleDNSParseErrorReturnsError(t *testing.T) {
	log := &BufferLogger{}
	h := &Handler{Mode: "mediated", Logger: log, NameCache: NewNameCache()}
	forward := func(netip.AddrPort, []byte) ([]byte, error) {
		t.Fatal("forward must not be called on a parse error")
		return nil, nil
	}
	resolver := netip.MustParseAddrPort("1.1.1.1:53")
	if _, err := h.handleDNS([]byte{0x00, 0x01}, resolver, forward); err == nil {
		t.Fatal("handleDNS err=nil on parse failure, want error")
	}
}

// TestGuardedDNSRebind asserts that in guarded mode a query for a name that
// resolves to an internal IP (169.254.169.254, a link-local/metadata address)
// is still forwarded and answered — no egress_dns_deny is emitted. The
// connect-time IP deny (Task 3/4) is the authoritative rebinding protection;
// the DNS layer must not duplicate it.
func TestGuardedDNSRebind(t *testing.T) {
	log := &BufferLogger{}
	h := &Handler{Mode: egressModeGuarded, Logger: log, NameCache: NewNameCache()}

	// Simulate a response where metadata.internal. resolves to 169.254.169.254.
	internalIP := [4]byte{169, 254, 169, 254}
	want := buildResponseWithA(t, 0x0010, "metadata.internal.", "metadata.internal.",
		internalIP, 60)

	forwardCalled := false
	forward := func(r netip.AddrPort, q []byte) ([]byte, error) {
		forwardCalled = true
		return want, nil
	}

	query := buildQuery(t, 0x0010, "metadata.internal.", dnsmessage.TypeA)
	resolver := netip.MustParseAddrPort("1.1.1.1:53")

	resp, err := h.handleDNS(query, resolver, forward)
	if err != nil {
		t.Fatalf("handleDNS: %v", err)
	}
	if !forwardCalled {
		t.Error("forward was NOT called in guarded mode; want forwarded (rebinding protection is connect-time, not DNS)")
	}
	if string(resp) != string(want) {
		t.Error("handleDNS did not return the forwarded response verbatim")
	}
	// Must NOT emit egress_dns_deny — guarded allows all name resolution.
	for _, ev := range log.Snapshot() {
		if ev["event"] == "egress_dns_deny" {
			t.Error("egress_dns_deny emitted in guarded mode; want no DNS deny")
		}
	}
	assertEvent(t, log, "egress_dns_allow")
}

// TestGuardedDNSPublicName asserts that guarded mode resolves ordinary public
// names freely (non-allowlisted names are forwarded, not refused).
func TestGuardedDNSPublicName(t *testing.T) {
	log := &BufferLogger{}
	h := &Handler{Mode: egressModeGuarded, Logger: log, NameCache: NewNameCache()}

	want := buildResponseWithA(t, 0x0011, "example.com.", "example.com.",
		[4]byte{93, 184, 216, 34}, 300)

	forwardCalled := false
	forward := func(r netip.AddrPort, q []byte) ([]byte, error) {
		forwardCalled = true
		return want, nil
	}

	query := buildQuery(t, 0x0011, "example.com.", dnsmessage.TypeA)
	resolver := netip.MustParseAddrPort("8.8.8.8:53")

	resp, err := h.handleDNS(query, resolver, forward)
	if err != nil {
		t.Fatalf("handleDNS: %v", err)
	}
	if !forwardCalled {
		t.Error("forward was NOT called in guarded mode for public name; want forwarded")
	}
	if string(resp) != string(want) {
		t.Error("handleDNS did not return the forwarded response verbatim")
	}
	assertEvent(t, log, "egress_dns_allow")
}

var errTestForward = errTestForwardErr{}

type errTestForwardErr struct{}

func (errTestForwardErr) Error() string { return "test forward failure" }
