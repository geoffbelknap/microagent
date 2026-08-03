package main

import (
	"os"
	"testing"
)

func TestCapabilityRiskAcknowledgementAdapters(t *testing.T) {
	const reason = "reviewed trusted import"
	cli, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"--name", "composition", "--acknowledge-capability-risk", reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cli.CapabilityRiskAcknowledgement != reason {
		t.Fatalf("CLI acknowledgement = %q", cli.CapabilityRiskAcknowledgement)
	}
	mcp, err := mcpWorkspaceCreateOptions(map[string]any{
		"name": "composition", "acknowledge_capability_risk": reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mcp.CapabilityRiskAcknowledgement != reason {
		t.Fatalf("MCP acknowledgement = %q", mcp.CapabilityRiskAcknowledgement)
	}
}

func TestCapabilityRiskAcknowledgementAgentfile(t *testing.T) {
	path := writeAgentfile(t, "name: composition\nacknowledgeCapabilityRisk: reviewed trusted import\n")
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{"--file", path})
	if err != nil {
		t.Fatal(err)
	}
	if opts.CapabilityRiskAcknowledgement != "reviewed trusted import" {
		t.Fatalf("Agentfile acknowledgement = %q", opts.CapabilityRiskAcknowledgement)
	}
}
