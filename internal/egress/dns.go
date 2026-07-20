package egress

import (
	"fmt"
	"net/netip"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// dns.go implements the mediator-as-resolver: parse a guest DNS query, decide
// whether the queried name may be resolved at all (strict mode is a hostname
// allowlist that defeats DNS tunneling), forward the permitted query to the
// resolver the guest targeted, and record the answer's name->IP mappings in the
// NameCache so later flows to those IPs can be policed by hostname.
//
// We use golang.org/x/net/dns/dnsmessage — the well-tested, allocation-light
// parser the Go stdlib resolver itself uses — rather than hand-rolling wire
// parsing (name compression pointers in particular are bug-prone).
//
// handleDNS takes an injected forward func so the filtering core is unit-testable
// without a real resolver or socket; the production UDP round-trip
// (defaultDNSForward) is wired through serveUDP -> serveDNS, which drives this
// same core.

// parseDNSQuestion parses the header and the FIRST question of a DNS message and
// returns the query id, the normalized QNAME (lowercased, trailing-dot stripped,
// via normalizeHost — so it compares equal to allowlist/NameCache entries), and
// the question type. A message with no question, or malformed wire bytes, is an
// error: the caller drops/audits it (it cannot be a query we forward).
func parseDNSQuestion(payload []byte) (id uint16, qname string, qtype dnsmessage.Type, err error) {
	var p dnsmessage.Parser
	hdr, err := p.Start(payload)
	if err != nil {
		return 0, "", 0, fmt.Errorf("dns: parse header: %w", err)
	}
	q, err := p.Question()
	if err != nil {
		return 0, "", 0, fmt.Errorf("dns: parse question: %w", err)
	}
	return hdr.ID, normalizeHost(q.Name.String()), q.Type, nil
}

// synthesizeRefused builds a response to query with QR=1 and RCODE=REFUSED and
// no answers. It echoes the query's id and first question (so a stub resolver
// matching the reply to its outstanding query accepts it) and sets
// RecursionAvailable, mirroring what a real recursive resolver returns. This is
// the strict-mode answer for a non-allowlisted name: the query is never
// forwarded, so the guest learns no IP and DNS tunneling is defeated.
func synthesizeRefused(query []byte) ([]byte, error) {
	var p dnsmessage.Parser
	hdr, err := p.Start(query)
	if err != nil {
		return nil, fmt.Errorf("dns: parse query for refusal: %w", err)
	}
	q, err := p.Question()
	if err != nil {
		return nil, fmt.Errorf("dns: parse question for refusal: %w", err)
	}

	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 hdr.ID,
		Response:           true,
		OpCode:             hdr.OpCode,
		RecursionDesired:   hdr.RecursionDesired,
		RecursionAvailable: true,
		RCode:              dnsmessage.RCodeRefused,
	})
	if err := b.StartQuestions(); err != nil {
		return nil, fmt.Errorf("dns: build refusal questions: %w", err)
	}
	if err := b.Question(q); err != nil {
		return nil, fmt.Errorf("dns: echo refusal question: %w", err)
	}
	out, err := b.Finish()
	if err != nil {
		return nil, fmt.Errorf("dns: finish refusal: %w", err)
	}
	return out, nil
}

// cacheDNSAnswers parses response's answer section and records each A (and AAAA)
// record's IP under qname (the originally queried, normalized name) with the
// record's TTL. Caching under qname — rather than under the answer's own owner
// name — is deliberate: for a CNAME chain (api.example.com -> cdn.example.net ->
// A) the guest asked for api.example.com and the allowlist is written against
// what the guest asks for, so reverse-resolving the flow's destination IP must
// yield api.example.com to match. This is the simplest correct behavior for
// allowlist matching. Non-positive TTLs are skipped by NameCache.Put.
//
// Parse failures and unexpected record types are ignored: a malformed or
// surprising answer simply yields no cache entries (the forwarded bytes are
// still relayed to the guest verbatim by the caller); caching is best-effort.
// DNS resource record types for the service-binding records that carry ECH
// configs (SvcParamKey 5). dnsmessage has no native type for these, so they
// parse as UnknownResource; we match on the numeric type.
const (
	dnsTypeSVCB  dnsmessage.Type = 64
	dnsTypeHTTPS dnsmessage.Type = 65
)

// stripECHRecords rewrites a DNS response to drop every HTTPS (type 65) and SVCB
// (type 64) resource record from all sections. Those records carry the ECH
// config (SvcParamKey 5) and the HTTP/3 advertisement; removing them keeps the
// guest's TLS SNI visible to the mediator (enforcement stays ECH-durable) and,
// since broker mode denies QUIC anyway, costs nothing — cooperative clients fall
// back to A/AAAA and standard TLS-over-TCP.
//
// Best-effort by design: the strip is hardening, not the enforcement boundary
// (deny-by-path and the IP allowlist do not depend on it). A response that does
// not parse, or that contains a resource type this rebuild does not handle, is
// returned UNCHANGED rather than corrupted. A response with no SVCB/HTTPS
// records is returned byte-identical (fast path, no rebuild).
func stripECHRecords(response []byte) []byte {
	var p dnsmessage.Parser
	hdr, err := p.Start(response)
	if err != nil {
		return response
	}
	questions, err := p.AllQuestions()
	if err != nil {
		return response
	}
	answers, err := p.AllAnswers()
	if err != nil {
		return response
	}
	authorities, err := p.AllAuthorities()
	if err != nil {
		return response
	}
	additionals, err := p.AllAdditionals()
	if err != nil {
		return response
	}
	if !containsServiceBinding(answers) && !containsServiceBinding(authorities) && !containsServiceBinding(additionals) {
		return response // nothing to strip; leave the wire bytes untouched
	}

	b := dnsmessage.NewBuilder(nil, hdr)
	b.EnableCompression()
	if b.StartQuestions() != nil {
		return response
	}
	for _, q := range questions {
		if b.Question(q) != nil {
			return response
		}
	}
	if !rebuildSection(&b, (*dnsmessage.Builder).StartAnswers, answers) ||
		!rebuildSection(&b, (*dnsmessage.Builder).StartAuthorities, authorities) ||
		!rebuildSection(&b, (*dnsmessage.Builder).StartAdditionals, additionals) {
		return response
	}
	out, err := b.Finish()
	if err != nil {
		return response
	}
	return out
}

func containsServiceBinding(rs []dnsmessage.Resource) bool {
	for _, r := range rs {
		if r.Header.Type == dnsTypeHTTPS || r.Header.Type == dnsTypeSVCB {
			return true
		}
	}
	return false
}

// rebuildSection starts a section and re-adds every resource except HTTPS/SVCB.
// It returns false on any unhandled resource type or builder error, so the
// caller falls back to the original response rather than emitting a partial one.
func rebuildSection(b *dnsmessage.Builder, start func(*dnsmessage.Builder) error, rs []dnsmessage.Resource) bool {
	if start(b) != nil {
		return false
	}
	for _, r := range rs {
		if r.Header.Type == dnsTypeHTTPS || r.Header.Type == dnsTypeSVCB {
			continue // drop the service-binding record entirely
		}
		if !readdResource(b, r) {
			return false
		}
	}
	return true
}

// readdResource re-emits one parsed resource into the builder. It handles the
// record types that realistically appear in the responses clients receive for
// A/AAAA/HTTPS queries; anything else returns false so the strip is abandoned
// (original response returned) rather than silently dropping data.
func readdResource(b *dnsmessage.Builder, r dnsmessage.Resource) bool {
	switch body := r.Body.(type) {
	case *dnsmessage.AResource:
		return b.AResource(r.Header, *body) == nil
	case *dnsmessage.AAAAResource:
		return b.AAAAResource(r.Header, *body) == nil
	case *dnsmessage.CNAMEResource:
		return b.CNAMEResource(r.Header, *body) == nil
	case *dnsmessage.OPTResource:
		return b.OPTResource(r.Header, *body) == nil
	case *dnsmessage.UnknownResource:
		return b.UnknownResource(r.Header, *body) == nil
	default:
		return false
	}
}

func cacheDNSAnswers(cache *NameCache, qname string, response []byte) {
	if cache == nil {
		return
	}
	var p dnsmessage.Parser
	if _, err := p.Start(response); err != nil {
		return
	}
	if err := p.SkipAllQuestions(); err != nil {
		return
	}
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			return // ErrSectionDone (normal end) or a parse error: stop.
		}
		ttl := time.Duration(h.TTL) * time.Second
		switch h.Type {
		case dnsmessage.TypeA:
			r, err := p.AResource()
			if err != nil {
				return
			}
			cache.Put(qname, netip.AddrFrom4(r.A), ttl)
		case dnsmessage.TypeAAAA:
			r, err := p.AAAAResource()
			if err != nil {
				return
			}
			cache.Put(qname, netip.AddrFrom16(r.AAAA), ttl)
		default:
			if err := p.SkipAnswer(); err != nil {
				return
			}
		}
	}
}

// handleDNS is the filtering DNS forwarder at the heart of the
// mediator-as-resolver. It parses the guest's query, decides whether the queried
// name may be resolved, and either forwards+caches+relays the real answer or
// returns a synthesized REFUSED (strict mode, non-allowlisted name — never
// forwarded). forward is injected (a UDP round-trip to resolver in production,
// wired in task 4.3) so this core is unit-testable without a real resolver.
//
// forward is injected (a UDP round-trip to resolver in production via
// defaultDNSForward) so this core is unit-testable without a real resolver.
//
// Returns the bytes the caller relays to the guest. On query parse failure or
// forward failure it returns an error; the caller drops the datagram (and the
// audit already records why).
// resolverAllowed reports whether the mediator may forward a guest DNS query to
// the given resolver address. With a configured resolver set (Resolvers), only
// those addresses are permitted. Without one, any non-internal (public) resolver
// is permitted and internal/loopback/link-local/metadata targets are refused.
// Ports are irrelevant; the comparison is address-only. This is the
// confused-deputy guard: the mediator opens the upstream resolver socket, so it
// must not relay to a resolver the guest chose but the workspace never
// sanctioned.
func (h *Handler) resolverAllowed(addr netip.Addr) bool {
	a := addr.Unmap()
	if len(h.Resolvers) > 0 {
		for _, r := range h.Resolvers {
			if r.Unmap() == a {
				return true
			}
		}
		return false
	}
	return !isInsideAddr(a)
}

func (h *Handler) handleDNS(query []byte, resolver netip.AddrPort, forward func(resolver netip.AddrPort, query []byte) ([]byte, error)) ([]byte, error) {
	id, qname, qtype, err := parseDNSQuestion(query)
	if err != nil {
		// Unparseable: cannot enforce or forward. Caller drops; audit the reason.
		h.Logger.Log("egress_dns_error", map[string]any{"error": err.Error()})
		return nil, err
	}

	listed := h.Policy != nil && h.Policy.AllowHost(qname).Allow
	passthrough := h.Passthrough != nil && h.Passthrough.AllowHost(qname).Allow
	// A locked allowlist drops the allow-broad grant, so DNS resolves only
	// allowlisted names — mirroring the Brain's TCP decision. Without this,
	// broker/mitm + --egress-lock-allowlist would still resolve any name.
	permissive := allowsBroad(h.Mode) && !h.AllowlistLocked
	allowed := permissive || listed || passthrough
	// unlisted marks a name permitted only because an allow-broad mode resolves
	// names freely (it is on no allowlist), so the audit trail records the looser
	// grant — mirroring the TCP and UDP paths.
	unlisted := allowed && !listed && !passthrough

	// A cooperative guest resolves through the mediator's own (inside/gateway)
	// resolver; a query aimed at a PUBLIC resolver address is an attempt to use a
	// foreign resolver — the guest cannot actually reach it (all DNS is TPROXY'd
	// here), but the attempt is a non-cooperation tell that outranks the generic
	// denied signal.
	foreignResolver := resolver.Addr().IsValid() && !isInsideAddr(resolver.Addr())
	dnsFields := func() map[string]any {
		f := map[string]any{"qname": qname, "qtype": qtype.String(), "id": id}
		if foreignResolver {
			f["signal"] = SignalForeignResolver
		}
		return f
	}

	if !allowed {
		// allowlist-only + non-allowlisted: refuse without forwarding. The guest
		// learns no IP — this is what makes a locked allowlist authoritative and
		// kills DNS tunneling.
		refused, rerr := synthesizeRefused(query)
		if rerr != nil {
			h.Logger.Log("egress_dns_error", map[string]any{"qname": qname, "error": rerr.Error()})
			return nil, rerr
		}
		denyFields := dnsFields()
		if _, ok := denyFields["signal"]; !ok {
			denyFields["signal"] = SignalDenied
		}
		h.Logger.Log("egress_dns_deny", denyFields)
		return refused, nil
	}

	// Confused-deputy guard: the mediator, not the guest, opens the upstream
	// resolver socket, so it can reach addresses the guest cannot (loopback,
	// metadata, internal :53). Refuse to forward to a resolver the workspace was
	// not configured to use — and, absent a configured set, to any internal
	// address — so a guest cannot drive the mediator to relay DNS to an internal
	// service. The qname allowlist above does not cover this: it gates the NAME,
	// not the resolver the query is aimed at.
	if !h.resolverAllowed(resolver.Addr()) {
		refused, rerr := synthesizeRefused(query)
		if rerr != nil {
			h.Logger.Log("egress_dns_error", map[string]any{"qname": qname, "error": rerr.Error()})
			return nil, rerr
		}
		denyFields := dnsFields()
		denyFields["resolver"] = resolver.String()
		denyFields["reason"] = "resolver not permitted"
		denyFields["signal"] = SignalResolverDenied
		h.Logger.Log("egress_dns_deny", denyFields)
		return refused, nil
	}

	resp, ferr := forward(resolver, query)
	if ferr != nil {
		fields := dnsFields()
		fields["resolver"] = resolver.String()
		fields["error"] = ferr.Error()
		h.Logger.Log("egress_dns_error", fields)
		return nil, ferr
	}

	// Strip HTTPS/SVCB records (and thus any ECH config) before the answer
	// reaches the guest, so cooperative clients keep their TLS SNI visible to the
	// mediator. This is the mediator acting as the sole ECH-stripping resolver.
	resp = stripECHRecords(resp)

	// Record name->IP mappings so later flows to those IPs can be policed by
	// hostname. Best-effort: a malformed answer simply caches nothing.
	cacheDNSAnswers(h.NameCache, qname, resp)

	fields := dnsFields()
	fields["resolver"] = resolver.String()
	if unlisted {
		fields["unlisted"] = true
	}
	h.Logger.Log("egress_dns_allow", fields)
	return resp, nil
}
