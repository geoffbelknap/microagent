package workspace

import "testing"

func TestSummarizeEgressAudit(t *testing.T) {
	events := []EgressEvent{
		{Event: "egress_allow", Host: "api.openai.com"},
		{Event: "egress_allow", Host: "api.openai.com"},
		{Event: "egress_udp_allow", Host: "8.8.8.8"},               // _allow suffix folds in
		{Event: "egress_internal_deny", Dst: "169.254.169.254:80"}, // no host -> dst is the key
		{Event: "egress_dns_deny", QName: "evil.example.com"},      // DNS records use qname
		{Event: "egress_listen"},                                   // no host/dst -> by_event only
	}
	s := SummarizeEgressAudit(events)

	if s.DecisionCount != 6 {
		t.Fatalf("DecisionCount = %d, want 6", s.DecisionCount)
	}
	if got := s.AllowByHost["api.openai.com"]; got != 2 {
		t.Errorf("allow api.openai.com = %d, want 2", got)
	}
	if got := s.AllowByHost["8.8.8.8"]; got != 1 {
		t.Errorf("allow 8.8.8.8 = %d, want 1 (udp_allow folds in)", got)
	}
	if got := s.DenyByHost["169.254.169.254:80"]; got != 1 {
		t.Errorf("deny IMDS dst = %d, want 1 (dst used when host empty)", got)
	}
	if got := s.DenyByHost["evil.example.com"]; got != 1 {
		t.Errorf("deny evil.example.com = %d, want 1 (dns_deny folds in)", got)
	}
	if s.ByEvent["egress_allow"] != 2 || s.ByEvent["egress_listen"] != 1 {
		t.Errorf("by_event wrong: %+v", s.ByEvent)
	}
}

func TestSummarizeEgressAuditEmpty(t *testing.T) {
	s := SummarizeEgressAudit(nil)
	if s.DecisionCount != 0 {
		t.Fatalf("DecisionCount = %d, want 0", s.DecisionCount)
	}
	// Empty audit must not carry empty host maps (omitempty keeps JSON clean).
	if s.AllowByHost != nil || s.DenyByHost != nil {
		t.Errorf("empty audit should have nil host maps, got allow=%v deny=%v", s.AllowByHost, s.DenyByHost)
	}
}
