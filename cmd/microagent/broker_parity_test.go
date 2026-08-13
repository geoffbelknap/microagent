package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// TestBrokerEndpointParityAcrossSurfaces is the multi-endpoint no-split-brain
// proof: one truth set of two broker endpoints, declared on each of the CLI
// (--broker-endpoint), the Agentfile (agent.brokers), and MCP (a "brokers"
// array, which threads through the same --broker-endpoint CLI flags) surface,
// must all resolve to an identical []*vmkit.BrokerConfig. Every surface routes
// through ParseBrokerConfig per endpoint (directly, or via ParseBrokerEndpoints
// for the string-grammar surfaces) — there is exactly one validator.
func TestBrokerEndpointParityAcrossSurfaces(t *testing.T) {
	const (
		specA = "upstream=https://a.example.com;secret=a=env:A_TOKEN;assurance=trusted-upstream;base-url-env=A_BASE_URL;ca=/etc/ssl/a.pem"
		specB = "upstream=https://b.example.com;secret=b=env:B_TOKEN;assurance=trusted-upstream;base-url-env=B_BASE_URL;proxy;capture"
	)

	// CLI: repeatable --broker-endpoint.
	cliOpts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"--name", "demo",
		"--image", "docker.io/library/alpine:3.20",
		"--broker-endpoint", specA,
		"--broker-endpoint", specB,
	})
	if err != nil {
		t.Fatalf("CLI parseWorkspaceOptions: %v", err)
	}
	if len(cliOpts.Brokers) != 2 {
		t.Fatalf("CLI Brokers = %+v, want 2 endpoints", cliOpts.Brokers)
	}

	// MCP: a "brokers" array of the same spec strings, resolved through the
	// typed create options builder.
	mcpOpts, err := mcpWorkspaceCreateOptions(map[string]any{
		"name":    "demo",
		"image":   "docker.io/library/alpine:3.20",
		"brokers": []any{specA, specB},
	})
	if err != nil {
		t.Fatalf("MCP create options: %v", err)
	}
	if len(mcpOpts.Brokers) != 2 {
		t.Fatalf("MCP Brokers = %+v, want 2 endpoints", mcpOpts.Brokers)
	}

	// Agentfile: the same two endpoints as a structured agent.brokers block.
	agentSpec := workspace.Spec{
		Name:     "demo",
		ImageRef: "docker.io/library/alpine:3.20",
		Agent: workspace.AgentSpec{
			Brokers: []workspace.AgentBrokerSpec{
				{Upstream: "https://a.example.com", Secret: "a=env:A_TOKEN", Env: []string{"A_BASE_URL"}, CA: "/etc/ssl/a.pem", Assurance: "trusted-upstream"},
				{Upstream: "https://b.example.com", Secret: "b=env:B_TOKEN", Env: []string{"B_BASE_URL"}, Proxy: true, Capture: true, Assurance: "trusted-upstream"},
			},
		},
	}
	agentOpts := workspace.DefaultOptions()
	if err := workspace.ApplySpec(&agentOpts, agentSpec, t.TempDir(), workspace.SpecApplyOptions{}); err != nil {
		t.Fatalf("ApplySpec: %v", err)
	}
	if len(agentOpts.Brokers) != 2 {
		t.Fatalf("Agentfile Brokers = %+v, want 2 endpoints", agentOpts.Brokers)
	}

	if !reflect.DeepEqual(cliOpts.Brokers, mcpOpts.Brokers) {
		t.Fatalf("CLI and MCP produced different broker sets:\nCLI: %+v\nMCP: %+v", cliOpts.Brokers, mcpOpts.Brokers)
	}
	if !reflect.DeepEqual(cliOpts.Brokers, agentOpts.Brokers) {
		t.Fatalf("CLI and Agentfile produced different broker sets:\nCLI:       %+v\nAgentfile: %+v", cliOpts.Brokers, agentOpts.Brokers)
	}
}

func TestSemanticBrokerParityAcrossCLIAXMCPAndAgentfile(t *testing.T) {
	dir := t.TempDir()
	grantPath := filepath.Join(dir, "grant.yaml")
	grant := `
operations:
  - name: read
    effect: read
    method: GET
    route: /v1/items
    headers:
      - name: Authorization
        pattern: 'Bearer @secret:.+'
        maxBytes: 128
    response:
      statuses: [200]
      contentTypes: [application/json]
      maxBytes: 4096
      credentialDisclosure: deny-exact
      json:
        type: object
`
	if err := os.WriteFile(grantPath, []byte(grant), 0o600); err != nil {
		t.Fatal(err)
	}
	cli, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"--name", "demo", "--image", "docker.io/library/alpine:3.20",
		"--broker-upstream", "https://api.example.com", "--broker-secret", "api=env:TOKEN",
		"--broker-assurance", "semantic", "--broker-grant", grantPath,
	})
	if err != nil {
		t.Fatalf("CLI/AX options: %v", err)
	}
	mcp, err := mcpWorkspaceCreateOptions(map[string]any{
		"name": "demo", "image": "docker.io/library/alpine:3.20",
		"broker_upstream": "https://api.example.com", "broker_secret": "api=env:TOKEN",
		"broker_assurance": "semantic", "broker_grant": grantPath,
	})
	if err != nil {
		t.Fatalf("MCP options: %v", err)
	}
	agent := workspace.DefaultOptions()
	err = workspace.ApplySpec(&agent, workspace.Spec{
		Name: "demo", ImageRef: "docker.io/library/alpine:3.20",
		Agent: workspace.AgentSpec{Broker: &workspace.AgentBrokerSpec{
			Upstream: "https://api.example.com", Secret: "api=env:TOKEN",
			Assurance: "semantic", Grant: "grant.yaml",
		}},
	}, dir, workspace.SpecApplyOptions{})
	if err != nil {
		t.Fatalf("Agentfile options: %v", err)
	}
	if !reflect.DeepEqual(cli.Broker, mcp.Broker) || !reflect.DeepEqual(cli.Broker, agent.Broker) {
		t.Fatalf("semantic broker split-brain:\nCLI/AX: %+v\nMCP: %+v\nAgentfile: %+v", cli.Broker, mcp.Broker, agent.Broker)
	}
}
