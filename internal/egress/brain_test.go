package egress

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func brainPolicy(t *testing.T, allow ...string) *Policy {
	t.Helper()
	p, err := NewPolicy(allow)
	if err != nil {
		t.Fatalf("NewPolicy(%v): %v", allow, err)
	}
	return p
}

// Evaluate is the single decision sequence every transport shares. These cases
// pin the allowlist + guarded inside-deny + candidate-fallback semantics that
// Handler.Handle, the UDP datapath, and the wasm sandbox all rely on.
func TestBrainEvaluate(t *testing.T) {
	public := netip.MustParseAddr("93.184.216.34") // example.com, public
	inside := netip.MustParseAddr("10.0.0.5")      // RFC1918, inside/infra

	t.Run("strict_allowlisted", func(t *testing.T) {
		b := &Brain{Mode: "strict", Policy: brainPolicy(t, "allowed.example")}
		v := b.Evaluate("allowed.example", nil, public, false)
		if !v.Allowed || v.Unlisted {
			t.Fatalf("allowlisted strict: got %+v, want Allowed && !Unlisted", v)
		}
	})

	t.Run("strict_denies_non_allowlisted", func(t *testing.T) {
		b := &Brain{Mode: "strict", Policy: brainPolicy(t, "allowed.example")}
		v := b.Evaluate("blocked.example", nil, public, false)
		if v.Allowed {
			t.Fatalf("non-allowlisted strict: got Allowed, want denied (%+v)", v)
		}
	})

	t.Run("guarded_grants_public_as_unlisted", func(t *testing.T) {
		b := &Brain{Mode: "guarded", Policy: brainPolicy(t)}
		v := b.Evaluate("blocked.example", nil, public, false)
		if !v.Allowed || !v.Unlisted || v.Inside {
			t.Fatalf("guarded public: got %+v, want Allowed && Unlisted && !Inside", v)
		}
	})

	t.Run("guarded_denies_inside", func(t *testing.T) {
		b := &Brain{Mode: "guarded", Policy: brainPolicy(t)}
		v := b.Evaluate("metadata.local", nil, inside, false)
		if v.Allowed || !v.Inside {
			t.Fatalf("guarded inside: got %+v, want !Allowed && Inside", v)
		}
	})

	t.Run("allowlist_overrides_inside_deny", func(t *testing.T) {
		// An operator who allowlists an internal name/IP keeps reaching it; the
		// inside flag is still set but the allowlist wins (no deny).
		b := &Brain{Mode: "guarded", Policy: brainPolicy(t, "internal.svc")}
		v := b.Evaluate("internal.svc", nil, inside, false)
		if !v.Allowed || v.Unlisted {
			t.Fatalf("allowlisted inside: got %+v, want Allowed && !Unlisted", v)
		}
	})

	t.Run("candidate_peer_name_permits", func(t *testing.T) {
		// Host denied, but the east-west peer name is allowlisted → allowed.
		b := &Brain{Mode: "strict", Policy: brainPolicy(t, "builder")}
		v := b.Evaluate("10.1.2.3", []string{"builder", "10.1.2.3"}, public, false)
		if !v.Allowed {
			t.Fatalf("candidate peer name: got %+v, want Allowed via candidate", v)
		}
	})

	t.Run("passthrough_excluded_from_unlisted", func(t *testing.T) {
		// A passthrough host gets no guarded "unlisted" tag (it is explicitly listed).
		b := &Brain{Mode: "guarded", Policy: brainPolicy(t)}
		v := b.Evaluate("pass.example", nil, public, true)
		if v.Unlisted {
			t.Fatalf("passthrough: got Unlisted, want false (%+v)", v)
		}
	})
}

func TestBrainAuditDeny(t *testing.T) {
	t.Run("non_inside_logs_egress_deny", func(t *testing.T) {
		log := &BufferLogger{}
		b := &Brain{Logger: log}
		b.AuditDeny(Verdict{Reason: "not allowlisted"}, map[string]any{"host": "blocked.example", "dst": "1.2.3.4:443"})
		ev := log.Snapshot()
		if len(ev) != 1 || ev[0]["event"] != "egress_deny" || ev[0]["reason"] != "not allowlisted" {
			t.Fatalf("expected one egress_deny with reason; got %v", ev)
		}
		if ev[0]["host"] != "blocked.example" {
			t.Fatalf("deny audit lost transport fields: %v", ev[0])
		}
	})

	t.Run("inside_logs_internal_deny", func(t *testing.T) {
		log := &BufferLogger{}
		b := &Brain{Logger: log}
		b.AuditDeny(Verdict{Inside: true, Reason: "not allowlisted"}, map[string]any{"host": "10.0.0.1", "dst": "10.0.0.1:443"})
		ev := log.Snapshot()
		if len(ev) != 1 || ev[0]["event"] != "egress_internal_deny" {
			t.Fatalf("expected egress_internal_deny; got %v", ev)
		}
		if ev[0]["internal"] != true || ev[0]["reason"] != "guarded: internal destination denied" {
			t.Fatalf("internal deny fields wrong: %v", ev[0])
		}
	})
}

func TestBrainSwapFor(t *testing.T) {
	staticTable := func(t *testing.T) *SwapTable {
		t.Helper()
		tbl, err := LoadSwapTable([]byte("swaps:\n  k:\n    type: static\n    domains: [api.example.com]\n    header: Authorization\n    format: 'Bearer {key}'\n    key_ref: env:K\n"))
		if err != nil {
			t.Fatalf("LoadSwapTable: %v", err)
		}
		return tbl
	}

	t.Run("no_table_no_match", func(t *testing.T) {
		b := &Brain{Logger: &BufferLogger{}}
		_, _, matched, err := b.SwapFor(context.Background(), "api.example.com")
		if matched || err != nil {
			t.Fatalf("nil table: matched=%v err=%v, want false/nil", matched, err)
		}
	})

	t.Run("host_not_in_table", func(t *testing.T) {
		b := &Brain{Swaps: staticTable(t), Logger: &BufferLogger{}}
		_, _, matched, err := b.SwapFor(context.Background(), "other.example")
		if matched || err != nil {
			t.Fatalf("non-matching host: matched=%v err=%v, want false/nil", matched, err)
		}
	})

	t.Run("match_resolves_host_side_and_audits", func(t *testing.T) {
		log := &BufferLogger{}
		b := &Brain{Swaps: staticTable(t), Resolver: fakeResolver{"env:K": "REALSECRET"}, Cache: newTokenCache(), Logger: log}
		hdr, val, matched, err := b.SwapFor(context.Background(), "api.example.com")
		if !matched || err != nil {
			t.Fatalf("match: matched=%v err=%v, want true/nil", matched, err)
		}
		if hdr != "Authorization" || val != "Bearer REALSECRET" {
			t.Fatalf("rendered credential wrong: hdr=%q val=%q", hdr, val)
		}
		var sawSwap bool
		for _, e := range log.Snapshot() {
			if e["event"] == "egress_swap" && e["host"] == "api.example.com" {
				sawSwap = true
			}
			// The audit must never carry the resolved secret.
			for _, v := range e {
				if s, ok := v.(string); ok && s == "REALSECRET" {
					t.Fatalf("secret leaked into audit event: %v", e)
				}
			}
		}
		if !sawSwap {
			t.Fatalf("expected egress_swap audit; got %v", log.Snapshot())
		}
	})

	t.Run("acquire_error_fails_closed_and_audits", func(t *testing.T) {
		log := &BufferLogger{}
		// Empty resolver → secret resolves empty → acquire fails closed.
		b := &Brain{Swaps: staticTable(t), Resolver: fakeResolver{}, Cache: newTokenCache(), Logger: log}
		_, _, matched, err := b.SwapFor(context.Background(), "api.example.com")
		if !matched || err == nil {
			t.Fatalf("acquire error: matched=%v err=%v, want true/non-nil", matched, err)
		}
		if !errors.Is(err, errNoSecret) {
			t.Fatalf("want errNoSecret, got %v", err)
		}
		var sawErr bool
		for _, e := range log.Snapshot() {
			if e["event"] == "egress_swap_error" {
				sawErr = true
			}
		}
		if !sawErr {
			t.Fatalf("expected egress_swap_error audit; got %v", log.Snapshot())
		}
	})
}
