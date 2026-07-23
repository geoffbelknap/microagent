package main

import (
	"os"
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
		specA = "upstream=https://a.example.com;secret=a=env:A_TOKEN;base-url-env=A_BASE_URL;ca=/etc/ssl/a.pem"
		specB = "upstream=https://b.example.com;secret=b=env:B_TOKEN;base-url-env=B_BASE_URL;proxy;capture"
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

	// MCP: a "brokers" array of the same spec strings, threaded through the
	// same --broker-endpoint CLI flags mcpCLIArgs builds.
	mcpArgs, err := mcpCLIArgs("workspace.create", map[string]any{
		"name":    "demo",
		"image":   "docker.io/library/alpine:3.20",
		"brokers": []any{specA, specB},
	})
	if err != nil {
		t.Fatalf("mcpCLIArgs: %v", err)
	}
	if len(mcpArgs) < 2 || mcpArgs[0] != "--mode=ax" || mcpArgs[1] != "create" {
		t.Fatalf("mcpCLIArgs = %v, want a --mode=ax create prefix", mcpArgs)
	}
	mcpOpts, err := parseWorkspaceOptions("create", os.Stdout, mcpArgs[2:])
	if err != nil {
		t.Fatalf("MCP parseWorkspaceOptions: %v", err)
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
				{Upstream: "https://a.example.com", Secret: "a=env:A_TOKEN", Env: []string{"A_BASE_URL"}, CA: "/etc/ssl/a.pem"},
				{Upstream: "https://b.example.com", Secret: "b=env:B_TOKEN", Env: []string{"B_BASE_URL"}, Proxy: true, Capture: true},
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
