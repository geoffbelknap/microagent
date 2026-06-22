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
// without a real resolver or socket; the production UDP round-trip is wired in a
// later task (4.3), which also calls handleDNS from the serveUDP receive loop.

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
// Returns the bytes the caller relays to the guest. On query parse failure or
// forward failure it returns an error; the caller drops the datagram (and the
// audit already records why).
func (h *Handler) handleDNS(query []byte, resolver netip.AddrPort, forward func(resolver netip.AddrPort, query []byte) ([]byte, error)) ([]byte, error) {
	id, qname, qtype, err := parseDNSQuestion(query)
	if err != nil {
		// Unparseable: cannot enforce or forward. Caller drops; audit the reason.
		h.Logger.Log("egress_dns_error", map[string]any{"error": err.Error()})
		return nil, err
	}

	listed := h.Policy != nil && h.Policy.AllowHost(qname).Allow
	passthrough := h.Passthrough != nil && h.Passthrough.AllowHost(qname).Allow
	mediated := h.Mode == egressModeMediated || h.Mode == egressModeGuarded
	allowed := mediated || listed || passthrough
	// unlisted marks a name permitted only because of mediated mode (it is on no
	// allowlist), so the audit trail records the looser grant — mirroring the TCP
	// and UDP paths.
	unlisted := allowed && !listed && !passthrough

	dnsFields := func() map[string]any {
		return map[string]any{"qname": qname, "qtype": qtype.String(), "id": id}
	}

	if !allowed {
		// strict + non-allowlisted: refuse without forwarding. The guest learns no
		// IP — this is what makes strict authoritative and kills DNS tunneling.
		refused, rerr := synthesizeRefused(query)
		if rerr != nil {
			h.Logger.Log("egress_dns_error", map[string]any{"qname": qname, "error": rerr.Error()})
			return nil, rerr
		}
		h.Logger.Log("egress_dns_deny", dnsFields())
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
