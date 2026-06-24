package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
	execclient "github.com/geoffbelknap/microagent/pkg/workspace/exec/client"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestMCPInitializeAndToolsList(t *testing.T) {
	input := bytes.NewBuffer(nil)
	input.Write(encodeMCPTestMessage(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}))
	input.Write(encodeMCPTestMessage(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	if len(responses) != 2 {
		t.Fatalf("responses = %d, want 2", len(responses))
	}
	if responses[0]["result"] == nil {
		t.Fatalf("initialize response missing result: %#v", responses[0])
	}
	result, ok := responses[1]["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list result = %#v", responses[1]["result"])
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) < 19 {
		t.Fatalf("tools = %#v, want initial tool set", result["tools"])
	}
	names := map[string]bool{}
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("tool = %#v", raw)
		}
		names[tool["name"].(string)] = true
	}
	for _, name := range []string{
		"microagent.ping", "microagent.describe",
		"workspace.create", "workspace.start", "workspace.exec", "workspace.halt", "workspace.stop", "workspace.kill", "workspace.quarantine", "workspace.pause", "workspace.resume", "workspace.delete", "workspace.list", "workspace.inspect", "workspace.result", "workspace.stats", "workspace.logs", "workspace.events", "workspace.egress", "workspace.clone", "workspace.apply", "workspace.commit", "workspace.estimate_cost",
		"artifacts.list", "artifacts.get",
		"snapshot.create", "snapshot.list", "snapshot.delete",
		"network.inspect",
		"volume.create", "volume.list", "volume.inspect", "volume.delete",
		"images.pull", "images.list", "images.push", "images.tag", "images.delete", "images.prune",
		"models.pull", "models.list", "models.remove", "models.prune", "models.serve", "models.stop", "models.runners", "models.policy.validate", "models.policy.evaluate",
		"profiles.list", "host.inspect", "doctor.check", "contract.get", "kernel.verify", "kernel.install", "rootfs.build",
		"cp",
	} {
		if !names[name] {
			t.Fatalf("tools missing %s: %#v", name, names)
		}
	}
}

func TestMCPInitializeEchoesSupportedRequestedProtocolVersion(t *testing.T) {
	input := bytes.NewBuffer(nil)
	input.Write(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"protocolVersion": "2025-03-26"},
	}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	if len(responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(responses))
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result = %#v", responses[0]["result"])
	}
	if got := result["protocolVersion"]; got != "2025-03-26" {
		t.Fatalf("protocolVersion = %#v, want 2025-03-26", got)
	}
}

func TestRunServeMCPAllowsNonInteractiveStdio(t *testing.T) {
	input := bytes.NewBuffer(nil)
	input.Write(encodeMCPTestMessage(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}))
	var output bytes.Buffer
	if err := runServeMCP(context.Background(), nil, input, &output); err != nil {
		t.Fatalf("runServeMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	if len(responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(responses))
	}
	if responses[0]["result"] == nil {
		t.Fatalf("initialize response missing result: %#v", responses[0])
	}
}

func TestMCPRawJSONStdioInitializeAndToolsList(t *testing.T) {
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}
`)
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	decoder := json.NewDecoder(&output)
	var initResp map[string]any
	if err := decoder.Decode(&initResp); err != nil {
		t.Fatalf("decode initialize response: %v\n%s", err, output.String())
	}
	result, ok := initResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result = %#v", initResp["result"])
	}
	if got := result["protocolVersion"]; got != "2025-06-18" {
		t.Fatalf("protocolVersion = %#v, want 2025-06-18", got)
	}
	var toolsResp map[string]any
	if err := decoder.Decode(&toolsResp); err != nil {
		t.Fatalf("decode tools/list response: %v\n%s", err, output.String())
	}
	result, ok = toolsResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list result = %#v", toolsResp["result"])
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools = %#v, want non-empty list", result["tools"])
	}
	if strings.Contains(output.String(), "Content-Length:") {
		t.Fatalf("raw JSON response used Content-Length framing:\n%s", output.String())
	}
}

func TestMCPToolSchemasDoNotEmitNullRequired(t *testing.T) {
	for _, tool := range mcpTools() {
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("%s inputSchema = %#v", tool["name"], tool["inputSchema"])
		}
		required, ok := schema["required"]
		if !ok {
			continue
		}
		if _, ok := required.([]string); !ok {
			t.Fatalf("%s required = %#v, want string slice or omitted", tool["name"], required)
		}
	}
}

func TestPrintServeMCPHelpPointsToClientSetup(t *testing.T) {
	var output bytes.Buffer
	printServeMCPHelp(&output)
	got := output.String()
	for _, want := range []string{
		"not an interactive",
		"Codex: codex mcp add microagent -- microagent serve mcp",
		"Claude Code: claude mcp add --transport stdio --scope user microagent -- microagent serve mcp",
		`stdio server named "microagent"`,
		"command: microagent",
		`args: ["serve", "mcp"]`,
		"docs/cli/serve.md#configure-mcp-clients",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("help missing %q:\n%s", want, got)
		}
	}
}

func TestPrintServeHelpTreatsMCPAsClientIntegration(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	printServeHelp(writeFile)
	if err := writeFile.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(readFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "mcp                 Serve the MCP stdio endpoint") {
		t.Fatalf("serve help missing mcp command:\n%s", got)
	}
	if strings.Contains(got, "model               ") {
		t.Fatalf("serve help advertises model command:\n%s", got)
	}
	if !strings.Contains(got, "MCP clients can launch") {
		t.Fatalf("serve help missing MCP integration note:\n%s", got)
	}
}

func TestRunServeRejectsModelServeAlias(t *testing.T) {
	err := runServe(context.Background(), []string{"model"}, nil)
	if err == nil {
		t.Fatal("runServe model succeeded, want unknown command error")
	}
	if !strings.Contains(err.Error(), "unknown serve command: model") {
		t.Fatalf("runServe model error = %q", err)
	}
}

func TestMCPManagementToolCLIArgs(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want []string
	}{
		{
			name: "workspace.create",
			args: map[string]any{"name": "demo", "image": "docker.io/library/python:3.13-slim", "model": "unsloth/Qwen3-4B-Instruct-2507-GGUF/Qwen3-4B-Instruct-2507-Q4_K_M.gguf", "model_token": "hf_test", "dry_run": true},
			want: []string{"--mode=ax", "create", "demo", "-image", "docker.io/library/python:3.13-slim", "-model", "unsloth/Qwen3-4B-Instruct-2507-GGUF/Qwen3-4B-Instruct-2507-Q4_K_M.gguf", "-model-token", "hf_test", "-dry-run"},
		},
		{
			// network mode maps through to create (t.Run disambiguates with #01)
			name: "workspace.create",
			args: map[string]any{"name": "demo", "image": "docker.io/library/busybox:1.36", "network": "isolated"},
			want: []string{"--mode=ax", "create", "demo", "-image", "docker.io/library/busybox:1.36", "-network", "isolated"},
		},
		{
			name: "workspace.create",
			args: map[string]any{
				"name":                      "demo",
				"model":                     "org/repo/model.gguf",
				"model_runner":              "vllm",
				"model_gpu":                 "auto",
				"model_runner_model":        "Qwen/Qwen2.5-0.5B-Instruct",
				"model_runner_served_model": "local-chat",
				"model_runner_args":         []any{"--max-model-len", "2048"},
				"model_runner_env":          []any{"CUDA_VISIBLE_DEVICES=0"},
				"model_mediation":           "policy",
				"model_policy_file":         "/tmp/model-policy.json",
				"model_policy_timeout":      "250ms",
			},
			want: []string{"--mode=ax", "create", "demo", "-model", "org/repo/model.gguf", "-model-runner", "vllm", "-model-gpu", "auto", "-model-runner-model", "Qwen/Qwen2.5-0.5B-Instruct", "-model-runner-served-model", "local-chat", "-model-runner-arg", "--max-model-len", "-model-runner-arg", "2048", "-model-runner-env", "CUDA_VISIBLE_DEVICES=0", "-model-mediation", "policy", "-model-policy-file", "/tmp/model-policy.json", "-model-policy-timeout", "250ms"},
		},
		{
			// egress + secret config maps through to create (t.Run disambiguates duplicate names)
			name: "workspace.create",
			args: map[string]any{
				"name":               "demo",
				"egress":             "strict",
				"egress_allow":       []any{"api.anthropic.com", ".pypi.org"},
				"egress_passthrough": []any{"pinned.example.com"},
				"egress_policy":      "/tmp/egress.yaml",
				"egress_swap_config": "/tmp/swaps.yaml",
				"cred_swap":          []any{"anthropic", "openai=env:MY_OPENAI"},
				"secret":             []any{"ANTHROPIC_API_KEY=env:KEY"},
				"secret_on_demand":   []any{"DB=dotenv:/tmp/app.env#DB"},
				"secrets_env_file":   "/tmp/app.env",
				"secrets_audit":      true,
			},
			want: []string{"--mode=ax", "create", "demo", "-egress", "strict", "-egress-policy", "/tmp/egress.yaml", "-egress-swap-config", "/tmp/swaps.yaml", "-secrets-env-file", "/tmp/app.env", "-secrets-audit", "-egress-allow", "api.anthropic.com", "-egress-allow", ".pypi.org", "-egress-passthrough", "pinned.example.com", "-cred-swap", "anthropic", "-cred-swap", "openai=env:MY_OPENAI", "-secret", "ANTHROPIC_API_KEY=env:KEY", "-secret-on-demand", "DB=dotenv:/tmp/app.env#DB"},
		},
		{
			name: "workspace.start",
			args: map[string]any{
				"name":            "demo",
				"state_dir":       "/tmp/state",
				"model_runner":    "llamacpp",
				"model_gpu":       "on",
				"model_mediation": "local-allow",
			},
			want: []string{"--mode=ax", "start", "demo", "-model-runner", "llamacpp", "-model-gpu", "on", "-model-mediation", "local-allow", "-state-dir", "/tmp/state"},
		},
		{
			name: "workspace.logs",
			args: map[string]any{"name": "demo", "state_dir": "/tmp/state"},
			want: []string{"--mode=ax", "logs", "demo", "-state-dir", "/tmp/state"},
		},
		{
			name: "workspace.events",
			args: map[string]any{"name": "demo"},
			want: []string{"--mode=ax", "events", "demo"},
		},
		{
			name: "workspace.egress",
			args: map[string]any{"name": "demo", "state_dir": "/tmp/state"},
			want: []string{"--mode=ax", "egress", "demo", "-state-dir", "/tmp/state"},
		},
		{
			name: "workspace.result",
			args: map[string]any{"name": "demo"},
			want: []string{"--mode=ax", "result", "demo"},
		},
		{
			name: "workspace.stats",
			args: map[string]any{"name": "demo"},
			want: []string{"--mode=ax", "stats", "demo"},
		},
		{
			name: "workspace.clone",
			args: map[string]any{"source": "demo", "target": "copy"},
			want: []string{"--mode=ax", "clone", "demo", "copy"},
		},
		{
			name: "workspace.apply",
			args: map[string]any{"file": "/tmp/microagent.yaml", "state_dir": "/tmp/state", "backend": "applevf", "arch": "arm64", "supervisor": "/tmp/helper"},
			want: []string{"--mode=ax", "apply", "-file", "/tmp/microagent.yaml", "-state-dir", "/tmp/state", "-backend", "applevf", "-arch", "arm64", "-supervisor", "/tmp/helper"},
		},
		{
			name: "workspace.commit",
			args: map[string]any{"name": "demo", "image": "example.com/acme/demo:rc", "state_dir": "/tmp/state", "arch": "arm64", "push": true},
			want: []string{"--mode=ax", "commit", "demo", "example.com/acme/demo:rc", "-state-dir", "/tmp/state", "-arch", "arm64", "-push"},
		},
		{
			name: "workspace.quarantine",
			args: map[string]any{"name": "demo", "state_dir": "/tmp/state"},
			want: []string{"--mode=ax", "quarantine", "demo", "-state-dir", "/tmp/state"},
		},
		{
			name: "artifacts.list",
			args: map[string]any{"name": "demo", "state_dir": "/tmp/state"},
			want: []string{"--mode=ax", "artifact", "demo", "-state-dir", "/tmp/state"},
		},
		{
			name: "models.serve",
			args: map[string]any{
				"model":               "org/repo/model.gguf",
				"dedicated":           true,
				"runner":              "custom",
				"runner_gpu":          "auto",
				"runner_model":        "ignored/custom-model",
				"runner_served_model": "local-chat",
				"runner_command":      "runner serve {model} --listen {addr}",
				"runner_name":         "runner",
				"runner_health_path":  "/ready",
				"runner_args":         []any{"--gpu", "auto"},
				"runner_env":          []any{"CUDA_VISIBLE_DEVICES=0"},
				"state_dir":           "/tmp/state",
			},
			want: []string{"--mode=ax", "model", "serve", "org/repo/model.gguf", "-dedicated", "-runner", "custom", "-runner-gpu", "auto", "-runner-model", "ignored/custom-model", "-runner-served-model", "local-chat", "-runner-command", "runner serve {model} --listen {addr}", "-runner-name", "runner", "-runner-health-path", "/ready", "-runner-arg", "--gpu", "-runner-arg", "auto", "-runner-env", "CUDA_VISIBLE_DEVICES=0", "-state-dir", "/tmp/state"},
		},
		{
			name: "models.policy.evaluate",
			args: map[string]any{
				"policy_file":   "/tmp/policy.json",
				"method":        "POST",
				"request_path":  "/v1/chat/completions",
				"workspace_id":  "ws",
				"capability":    "model.openai",
				"worker_id":     "worker",
				"model":         "tiny",
				"request_bytes": float64(512),
				"text_bytes":    float64(128),
				"messages":      float64(1),
				"max_tokens":    float64(32),
				"stream":        false,
				"tools":         []any{"shell"},
				"expect":        "allow",
			},
			want: []string{"--mode=ax", "model", "policy", "evaluate", "/tmp/policy.json", "-method", "POST", "-path", "/v1/chat/completions", "-workspace-id", "ws", "-capability", "model.openai", "-worker-id", "worker", "-model", "tiny", "-request-bytes", "512", "-text-bytes", "128", "-messages", "1", "-max-tokens", "32", "-stream", "false", "-tool", "shell", "-expect", "allow"},
		},
		{
			name: "snapshot.create",
			args: map[string]any{"name": "demo", "tag": "before-upgrade"},
			want: []string{"--mode=ax", "snapshot", "create", "demo", "-tag", "before-upgrade"},
		},
		{
			name: "snapshot.delete",
			args: map[string]any{"name": "demo", "tag": "before-upgrade", "state_dir": "/tmp/state"},
			want: []string{"--mode=ax", "snapshot", "delete", "demo", "before-upgrade", "-state-dir", "/tmp/state"},
		},
		{
			name: "volume.create",
			args: map[string]any{"name": "data", "size_mib": float64(2048)},
			want: []string{"--mode=ax", "volume", "create", "data", "-size-mib", "2048"},
		},
		{
			name: "volume.delete",
			args: map[string]any{"name": "data", "force": true},
			want: []string{"--mode=ax", "volume", "delete", "data", "-force"},
		},
		{
			name: "images.push",
			args: map[string]any{"image": "example.com/acme/demo:rc", "state_dir": "/tmp/state"},
			want: []string{"--mode=ax", "image", "push", "example.com/acme/demo:rc", "-state-dir", "/tmp/state"},
		},
		{
			name: "images.tag",
			args: map[string]any{"source": "example.com/acme/demo:rc", "target": "example.com/acme/demo:stable"},
			want: []string{"--mode=ax", "image", "tag", "example.com/acme/demo:rc", "example.com/acme/demo:stable"},
		},
		{
			name: "images.delete",
			args: map[string]any{"image": "example.com/acme/demo:old", "delete_files": true},
			want: []string{"--mode=ax", "image", "delete", "example.com/acme/demo:old", "-delete", "-yes"},
		},
		{
			name: "images.prune",
			args: map[string]any{"state_dir": "/tmp/state", "delete_files": true},
			want: []string{"--mode=ax", "image", "prune", "-state-dir", "/tmp/state", "-delete", "-yes"},
		},
		{
			name: "profiles.list",
			args: map[string]any{},
			want: []string{"--mode=ax", "profiles"},
		},
		{
			name: "host.inspect",
			args: map[string]any{"backend": "applevf", "arch": "arm64", "supervisor": "/tmp/helper"},
			want: []string{"--mode=ax", "host", "-backend", "applevf", "-arch", "arm64", "-supervisor", "/tmp/helper"},
		},
		{
			name: "doctor.check",
			args: map[string]any{"backend": "linux-kvm"},
			want: []string{"--mode=ax", "doctor", "-backend", "linux-kvm"},
		},
		{
			name: "contract.get",
			args: map[string]any{},
			want: []string{"--mode=ax", "contract"},
		},
		{
			name: "kernel.verify",
			args: map[string]any{"path": "/tmp/vmlinux", "sha256": "abc", "backend": "linux-kvm", "arch": "amd64"},
			want: []string{"--mode=ax", "kernel", "verify", "-path", "/tmp/vmlinux", "-sha256", "abc", "-backend", "linux-kvm", "-arch", "amd64"},
		},
		{
			name: "kernel.install",
			args: map[string]any{"url": "https://example.test/vmlinux", "sha256": "abc", "out": "/tmp/vmlinux", "backend": "linux-kvm", "arch": "amd64"},
			want: []string{"--mode=ax", "kernel", "install", "-url", "https://example.test/vmlinux", "-sha256", "abc", "-out", "/tmp/vmlinux", "-backend", "linux-kvm", "-arch", "amd64"},
		},
		{
			name: "rootfs.build",
			args: map[string]any{"image": "alpine:3.20", "os": "linux", "arch": "amd64", "out": "/tmp/rootfs.ext4", "state_dir": "/tmp/state", "size_mib": float64(2048), "allow_mutable": true},
			want: []string{"--mode=ax", "rootfs", "build", "-image", "alpine:3.20", "-os", "linux", "-arch", "amd64", "-out", "/tmp/rootfs.ext4", "-state-dir", "/tmp/state", "-size-mib", "2048", "-allow-mutable"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mcpCLIArgs(tt.name, tt.args)
			if err != nil {
				t.Fatalf("mcpCLIArgs: %v", err)
			}
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Fatalf("args = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMCPWorkspaceEstimateCost(t *testing.T) {
	result := estimateWorkspaceCost(map[string]any{"profile": "tiny", "price_per_hour": 0.25})
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "estimated_cost_hour") || !strings.Contains(string(data), "memory_mib") {
		t.Fatalf("estimate = %s", data)
	}
}

func TestMCPDeletePreview(t *testing.T) {
	result, err := runMCPTool(context.Background(), "workspace.delete", map[string]any{"name": "demo", "preview": true})
	if err != nil {
		t.Fatalf("runMCPTool preview: %v", err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"preview":true`) || !strings.Contains(string(data), "remove workspace disk and state") {
		t.Fatalf("preview = %s", data)
	}
}

func TestMCPManagementDeletePreview(t *testing.T) {
	for _, tool := range []string{"volume.delete", "snapshot.delete", "images.delete", "images.prune"} {
		t.Run(tool, func(t *testing.T) {
			args := map[string]any{"name": "demo", "tag": "snap", "image": "example.com/acme/demo:old", "preview": true, "force": true, "delete_files": true}
			result, err := runMCPTool(context.Background(), tool, args)
			if err != nil {
				t.Fatalf("runMCPTool preview: %v", err)
			}
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"preview":true`) {
				t.Fatalf("preview = %s", data)
			}
		})
	}
}

func TestMCPHostMutationPreviewAndConfirmation(t *testing.T) {
	args := map[string]any{"url": "https://example.test/vmlinux", "sha256": "abc", "preview": true}
	preview, err := runMCPTool(context.Background(), "kernel.install", args)
	if err != nil {
		t.Fatalf("runMCPTool preview: %v", err)
	}
	result, ok := preview["result"].(map[string]any)
	if !ok {
		t.Fatalf("preview result type = %T", preview["result"])
	}
	token, ok := result["confirmation_token"].(string)
	if !ok || token == "" {
		t.Fatalf("confirmation_token = %#v", result["confirmation_token"])
	}
	if _, err := mcpCLIArgs("kernel.install", map[string]any{"url": "https://example.test/vmlinux", "sha256": "abc", "confirm_token": token}); err != nil {
		t.Fatalf("mcpCLIArgs confirmed: %v", err)
	}
	if _, err := runMCPTool(context.Background(), "kernel.install", map[string]any{"url": "https://example.test/vmlinux", "sha256": "abc"}); err == nil {
		t.Fatal("runMCPTool without confirm_token err = nil, want confirmation error")
	}
}

func TestMCPSummarizeWorkspaceInspect(t *testing.T) {
	// No egress audit log under this state dir, so egress_summary is omitted.
	summary, ok := summarizeWorkspaceInspect(map[string]any{
		"ok":      true,
		"backend": "linux-kvm",
		"event": map[string]any{
			"state":    "running",
			"identity": map[string]any{"runtimeID": "demo"},
		},
	}, t.TempDir(), "demo").(map[string]any)
	if !ok {
		t.Fatalf("summary type = %T", summary)
	}
	if summary["format"] != "summary" || summary["workspace"] != "demo" || summary["state"] != "running" {
		t.Fatalf("summary = %#v", summary)
	}
	if _, present := summary["egress_summary"]; present {
		t.Fatalf("egress_summary should be omitted when no audit log exists: %#v", summary)
	}
	points, ok := summary["next_decision_points"].([]string)
	if !ok || len(points) == 0 {
		t.Fatalf("next_decision_points = %#v", summary["next_decision_points"])
	}
}

func TestMCPSummarizeWorkspaceInspectIncludesEgressSummary(t *testing.T) {
	stateDir := t.TempDir()
	dir := filepath.Join(stateDir, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"event":"egress_allow","ts":"2026-06-16T00:00:00Z","host":"api.github.com"}` + "\n" +
		`{"event":"egress_allow","ts":"2026-06-16T00:00:01Z","host":"api.github.com"}` + "\n" +
		`{"event":"egress_deny","ts":"2026-06-16T00:00:02Z","host":"evil.example","reason":"blocked"}` + "\n" +
		`{"event":"egress_dns_deny","ts":"2026-06-16T00:00:03Z","qname":"tracker.example"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "egress-access.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, ok := summarizeWorkspaceInspect(map[string]any{
		"ok":      true,
		"backend": "linux-kvm",
		"event": map[string]any{
			"state":    "running",
			"identity": map[string]any{"runtimeID": "demo"},
		},
	}, stateDir, "demo").(map[string]any)
	if !ok {
		t.Fatalf("summary type = %T", summary)
	}
	eg, ok := summary["egress_summary"].(map[string]any)
	if !ok {
		t.Fatalf("egress_summary missing or wrong type: %#v", summary["egress_summary"])
	}
	if eg["decision_count"] != 4 {
		t.Fatalf("decision_count = %#v, want 4", eg["decision_count"])
	}
	byEvent, ok := eg["by_event"].(map[string]int)
	if !ok || byEvent["egress_allow"] != 2 || byEvent["egress_deny"] != 1 || byEvent["egress_dns_deny"] != 1 {
		t.Fatalf("by_event = %#v", eg["by_event"])
	}
	allow, ok := eg["allow_by_host"].(map[string]int)
	if !ok || allow["api.github.com"] != 2 {
		t.Fatalf("allow_by_host = %#v", eg["allow_by_host"])
	}
	deny, ok := eg["deny_by_host"].(map[string]int)
	if !ok || deny["evil.example"] != 1 {
		t.Fatalf("deny_by_host = %#v", eg["deny_by_host"])
	}
}

func TestMCPSummarizeWorkspaceCreateLifecycle(t *testing.T) {
	summary, ok := summarizeWorkspaceLifecycle(map[string]any{
		"workspace":   "demo",
		"rootfs_path": "/tmp/rootfs.ext4",
		"response": map[string]any{
			"ok":      true,
			"backend": "linux-kvm",
			"event": map[string]any{
				"state":  "stopped",
				"detail": "workspace created",
			},
		},
	}, "created").(map[string]any)
	if !ok {
		t.Fatalf("summary type = %T", summary)
	}
	if summary["format"] != "summary" || summary["outcome"] != "created" || summary["workspace"] != "demo" || summary["ready"] != true {
		t.Fatalf("summary = %#v", summary)
	}
	if summary["state_meaning"] != "created and ready to start" {
		t.Fatalf("state_meaning = %#v", summary["state_meaning"])
	}
	points, ok := summary["next_decision_points"].([]string)
	if !ok || len(points) == 0 || points[0] != "workspace.start" {
		t.Fatalf("next_decision_points = %#v", summary["next_decision_points"])
	}
}

func TestMCPSummarizeWorkspaceLogs(t *testing.T) {
	summary, ok := summarizeWorkspaceLogs(map[string]any{
		"workspace": "demo",
		"logs":      "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n",
	}, 3).(map[string]any)
	if !ok {
		t.Fatalf("summary type = %T", summary)
	}
	if summary["format"] != "summary" || summary["workspace"] != "demo" || summary["line_count"] != 9 {
		t.Fatalf("summary = %#v", summary)
	}
	tail, ok := summary["tail_lines"].([]string)
	if !ok || len(tail) != 3 || tail[0] != "seven" || tail[2] != "nine" {
		t.Fatalf("tail_lines = %#v", summary["tail_lines"])
	}
}

func TestMCPSummarizeWorkspaceEvents(t *testing.T) {
	summary, ok := summarizeWorkspaceEvents(map[string]any{
		"workspace": "demo",
		"events": []any{
			map[string]any{"state": "prepared", "observedAt": "2026-06-04T00:00:00Z"},
			map[string]any{"state": "running", "observedAt": "2026-06-04T00:00:01Z", "detail": "serial=serial.log"},
			map[string]any{"state": "halted", "observedAt": "2026-06-04T00:00:02Z"},
		},
	}, 2, 1).(map[string]any)
	if !ok {
		t.Fatalf("summary type = %T", summary)
	}
	if summary["format"] != "summary" || summary["workspace"] != "demo" || summary["event_count"] != 3 || summary["latest_state"] != "halted" {
		t.Fatalf("summary = %#v", summary)
	}
	recent, ok := summary["recent"].([]any)
	if !ok || len(recent) != 2 {
		t.Fatalf("recent = %#v", summary["recent"])
	}
	if summary["next_after_index"] != 3 || summary["after_index"] != 1 {
		t.Fatalf("cursor fields = %#v", summary)
	}
}

func TestMCPDescribeTool(t *testing.T) {
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params":  map[string]any{"name": "microagent.describe"},
	}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	data, err := json.Marshal(responses[0]["result"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "schema_version") || !strings.Contains(string(data), "workspace.create") {
		t.Fatalf("describe response = %s", data)
	}
}

func TestMCPIdempotencyCache(t *testing.T) {
	t.Cleanup(func() { mcpIdempotencyCache = sync.Map{} })
	key := "workspace.create:test-key"
	mcpIdempotencyCache.Store(key, map[string]any{"result": map[string]any{"workspace": "cached"}})
	result, err := runMCPTool(context.Background(), "workspace.create", map[string]any{"name": "demo", "idempotency_key": "test-key"})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if result["idempotency_replay"] != true {
		t.Fatalf("idempotency_replay = %#v", result["idempotency_replay"])
	}
}

func TestMCPWorkspaceExecReturnsStructuredResult(t *testing.T) {
	seen := make(chan execprotocol.ExecRequest, 1)
	_, port, stop := startCommandExecServer(t, func(req execprotocol.ExecRequest) execprotocol.ExecResult {
		seen <- req
		code := 0
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		result.Stdout = []byte("Linux demo\n")
		return result
	})
	defer stop()
	stateDir := writeCommandExecRuntimeState(t, "research", vmkit.StateRunning, port)
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name":      "research",
		"state_dir": stateDir,
		"argv":      []any{"uname", "-a"},
		"env":       map[string]any{"TEST_VAR": "hello"},
		"cwd":       "/tmp",
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	result, ok := envelope["result"].(execprotocol.ExecResult)
	if !ok {
		t.Fatalf("result type = %T", envelope["result"])
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || string(result.Stdout) != "Linux demo\n" || result.Status != execprotocol.ExecStatusExited {
		t.Fatalf("result = %#v", result)
	}
	req := <-seen
	if strings.Join(req.Argv, " ") != "uname -a" || req.Env["TEST_VAR"] != "hello" || req.Cwd != "/tmp" {
		t.Fatalf("request = %#v", req)
	}
	if envelope["retry_count"] != 0 {
		t.Fatalf("retry_count = %#v, want 0", envelope["retry_count"])
	}
	if envelope["retry_wall_clock_ms"] != int64(0) {
		t.Fatalf("retry_wall_clock_ms = %#v, want 0", envelope["retry_wall_clock_ms"])
	}
}

func TestMCPWorkspaceExecRetriesTCPReset(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		return successfulMCPExecResult("Linux demo\n"), workspace.ExecRetryMetadata{Count: 1, WallClock: time.Millisecond}, nil
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"uname", "-a"},
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if envelope["retry_count"] != 1 {
		t.Fatalf("retry_count = %#v, want 1", envelope["retry_count"])
	}
	result := envelope["result"].(execprotocol.ExecResult)
	if string(result.Stdout) != "Linux demo\n" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
}

func TestMCPWorkspaceExecRetriesConnectionRefused(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		return successfulMCPExecResult("ok\n"), workspace.ExecRetryMetadata{Count: 1, WallClock: time.Millisecond}, nil
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"true"},
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if envelope["retry_count"] != 1 {
		t.Fatalf("retry_count = %#v, want 1", envelope["retry_count"])
	}
}

func TestMCPWorkspaceExecRetriesConnectionTimeout(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		return successfulMCPExecResult("ok\n"), workspace.ExecRetryMetadata{Count: 1, WallClock: time.Millisecond}, nil
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"true"},
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if envelope["retry_count"] != 1 {
		t.Fatalf("retry_count = %#v, want 1", envelope["retry_count"])
	}
}

func TestMCPWorkspaceExecRetryExhaustionReturnsStructuredError(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		err := workspace.ExecRetryExhaustedError{Retries: 3, WallClock: time.Millisecond, LastErr: execclient.UnreachableError{Addr: "127.0.0.1:45000", Err: syscall.ECONNREFUSED}}
		return execprotocol.ExecResult{}, workspace.ExecRetryMetadata{Count: 3, WallClock: time.Millisecond, Exhausted: true}, err
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"true"},
	})
	if err == nil {
		t.Fatal("runMCPTool err = nil, want retry-exhausted error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if envelope["retry_count"] != 3 {
		t.Fatalf("retry_count = %#v, want 3", envelope["retry_count"])
	}
	if envelope["retry_exhausted"] != true {
		t.Fatalf("retry_exhausted = %#v, want true", envelope["retry_exhausted"])
	}
	structured, ok := envelope["error"].(mcpStructuredError)
	if !ok {
		t.Fatalf("error type = %T", envelope["error"])
	}
	if structured.Kind != errorKindTransient {
		t.Fatalf("kind = %q, want transient", structured.Kind)
	}
	if !structured.Retryable {
		t.Fatalf("retryable = false, want true")
	}
	if !strings.Contains(structured.Message, "persisted after 3 retries") {
		t.Fatalf("message = %q, want retry exhaustion detail", structured.Message)
	}
}

func TestMCPWorkspaceExecRetryExhaustionIncludesErrorEnvelopeMetadata(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		err := workspace.ExecRetryExhaustedError{Retries: 3, WallClock: time.Millisecond, LastErr: execclient.UnreachableError{Addr: "127.0.0.1:45000", Err: syscall.ECONNREFUSED}}
		return execprotocol.ExecResult{}, workspace.ExecRetryMetadata{Count: 3, WallClock: time.Millisecond, Exhausted: true}, err
	})
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params": map[string]any{
			"name": "workspace.exec",
			"arguments": map[string]any{
				"name": "research",
				"argv": []string{"true"},
			},
		},
	}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	errObj, ok := responses[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("response = %#v, want JSON-RPC error", responses[0])
	}
	data, ok := errObj["data"].(map[string]any)
	if !ok {
		t.Fatalf("error data = %#v", errObj["data"])
	}
	if data["kind"] != string(errorKindTransient) || data["retryable"] != true {
		t.Fatalf("error data classification = %#v", data)
	}
	if data["retry_count"] != float64(3) {
		t.Fatalf("retry_count = %#v, want 3", data["retry_count"])
	}
	if data["retry_exhausted"] != true {
		t.Fatalf("retry_exhausted = %#v, want true", data["retry_exhausted"])
	}
	if _, ok := data["retry_wall_clock_ms"].(float64); !ok {
		t.Fatalf("retry_wall_clock_ms missing or non-number: %#v", data)
	}
}

func TestMCPWorkspaceExecDoesNotRetryExecCompletedErrors(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		code := 127
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		result.ExitCode = &code
		result.Stderr = []byte("command not found\n")
		return result, workspace.ExecRetryMetadata{}, nil
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"missing-command"},
	})
	if err != nil {
		t.Fatalf("runMCPTool: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if envelope["retry_count"] != 0 {
		t.Fatalf("retry_count = %#v, want 0", envelope["retry_count"])
	}
}

func TestMCPWorkspaceExecDoesNotRetryWorkspaceNotRunning(t *testing.T) {
	attempts := 0
	stubMCPWorkspaceExec(t, func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error) {
		attempts++
		return execprotocol.ExecResult{}, workspace.ExecRetryMetadata{}, fmt.Errorf("workspace research is not running; structured exec is unavailable in state stopped")
	})
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name": "research",
		"argv": []any{"true"},
	})
	if err == nil {
		t.Fatal("runMCPTool err = nil, want workspace-not-running error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if envelope["retry_count"] != 0 {
		t.Fatalf("retry_count = %#v, want 0", envelope["retry_count"])
	}
}

func stubMCPWorkspaceExec(t *testing.T, fn func(context.Context, workspace.Options, execprotocol.ExecRequest) (execprotocol.ExecResult, workspace.ExecRetryMetadata, error)) {
	t.Helper()
	originalExec := mcpWorkspaceExec
	mcpWorkspaceExec = fn
	t.Cleanup(func() {
		mcpWorkspaceExec = originalExec
	})
}

func successfulMCPExecResult(stdout string) execprotocol.ExecResult {
	code := 0
	result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
	result.ExitCode = &code
	result.Stdout = []byte(stdout)
	return result
}

func TestMCPStructuredErrorRetryableClassification(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		kind      structuredErrorKind
		retryable bool
	}{
		{name: "permanent", err: fmt.Errorf("usage: workspace.exec requires name"), kind: errorKindPermanent, retryable: false},
		{name: "conflict", err: fmt.Errorf("workspace demo is already running"), kind: errorKindConflict, retryable: false},
		{name: "not found", err: workspace.WorkspaceNotFoundError{Name: "missing"}, kind: errorKindNotFound, retryable: false},
		{name: "resource exhausted", err: fmt.Errorf("requested disk size exceeds limit"), kind: errorKindResourceExhausted, retryable: true},
		{name: "unsupported", err: fmt.Errorf("microagent exec is not supported"), kind: errorKindUnsupported, retryable: false},
		{name: "policy denied", err: fmt.Errorf("permission denied"), kind: errorKindPolicyDenied, retryable: false},
		{name: "transient", err: fmt.Errorf("connection refused"), kind: errorKindTransient, retryable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapMCPStructuredError(tt.err, "req-test")
			if got.Kind != tt.kind {
				t.Fatalf("Kind = %q, want %q", got.Kind, tt.kind)
			}
			if got.Retryable != tt.retryable {
				t.Fatalf("Retryable = %v, want %v", got.Retryable, tt.retryable)
			}
			if got.Message != tt.err.Error() {
				t.Fatalf("Message = %q, want %q", got.Message, tt.err.Error())
			}
			if got.CorrelationID != "req-test" {
				t.Fatalf("CorrelationID = %q, want req-test", got.CorrelationID)
			}
			if strings.TrimSpace(got.Remediation) == "" {
				t.Fatalf("Remediation is empty")
			}
		})
	}
}

func TestMCPStructuredErrorRetryableAlwaysMarshaled(t *testing.T) {
	for _, err := range []error{
		workspace.WorkspaceNotFoundError{Name: "missing"},
		fmt.Errorf("connection refused"),
	} {
		data, marshalErr := json.Marshal(mapMCPStructuredError(err, "req-test"))
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"kind", "message", "remediation", "correlation_id", "retryable"} {
			if _, ok := decoded[field]; !ok {
				t.Fatalf("error %q missing field %q after marshal: %s", err, field, data)
			}
		}
	}
}

func TestMCPWorkspaceExecErrorKinds(t *testing.T) {
	envelope, err := runMCPTool(context.Background(), "workspace.exec", map[string]any{
		"name":      "missing",
		"state_dir": t.TempDir(),
		"argv":      []any{"true"},
	})
	if err == nil {
		t.Fatal("runMCPTool err = nil, want not found")
	}
	structured, ok := envelope["error"].(mcpStructuredError)
	if !ok {
		t.Fatalf("error type = %T", envelope["error"])
	}
	if structured.Kind != errorKindNotFound {
		t.Fatalf("kind = %q, want not_found", structured.Kind)
	}
	if structured.Retryable {
		t.Fatalf("retryable = true, want false")
	}
}

func TestMCPWorkspaceExecToolCallErrorMapsKind(t *testing.T) {
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params": map[string]any{
			"name": "workspace.exec",
			"arguments": map[string]any{
				"name":      "missing",
				"state_dir": t.TempDir(),
				"argv":      []string{"true"},
			},
		},
	}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	errObj, ok := responses[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("response = %#v, want JSON-RPC error", responses[0])
	}
	data, ok := errObj["data"].(map[string]any)
	if !ok || data["kind"] != string(errorKindNotFound) {
		t.Fatalf("error data = %#v", errObj["data"])
	}
	if data["message"] == "" || data["remediation"] == "" || data["correlation_id"] == "" {
		t.Fatalf("error data missing required fields: %#v", data)
	}
	retryable, ok := data["retryable"].(bool)
	if !ok {
		t.Fatalf("retryable missing or non-boolean: %#v", data)
	}
	if retryable {
		t.Fatalf("retryable = true, want false: %#v", data)
	}
}

func TestMCPDescribeWorkspaceExecStructuredSchema(t *testing.T) {
	manifest := microagentCapabilityManifest()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"workspace.exec", "argv", "exit_code", "stdout_truncated", "contentEncoding"} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q: %s", want, text)
		}
	}
}

func TestMCPWorkspaceExecLegacyCommandStringUsesExplicitShell(t *testing.T) {
	req, err := mcpExecRequest(map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(req.Argv, " ") != "sh -lc echo hello" {
		t.Fatalf("argv = %#v", req.Argv)
	}
}

func TestMCPPrincipalContext(t *testing.T) {
	got := principalContextArg(map[string]any{"principal": map[string]any{
		"workload_identity":   "agent-1",
		"delegated_authority": "user",
		"purpose":             "test",
		"correlation_id":      "corr-1",
		"ignored":             "value",
	}})
	if got["workload_identity"] != "agent-1" || got["correlation_id"] != "corr-1" {
		t.Fatalf("principal = %#v", got)
	}
	if _, ok := got["ignored"]; ok {
		t.Fatalf("principal retained unexpected field: %#v", got)
	}
}

func TestMCPPingTool(t *testing.T) {
	input := bytes.NewBuffer(encodeMCPTestMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      "call-1",
		"method":  "tools/call",
		"params":  map[string]any{"name": "microagent.ping"},
	}))
	var output bytes.Buffer
	if err := serveMCP(context.Background(), input, &output); err != nil {
		t.Fatalf("serveMCP: %v", err)
	}
	responses := decodeMCPTestResponses(t, output.Bytes())
	if len(responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(responses))
	}
	data, err := json.Marshal(responses[0]["result"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "pong") {
		t.Fatalf("tool call response = %s, want pong", data)
	}
}

func decodeMCPTestResponses(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	reader := mcpStdioServer{in: bufio.NewReader(bytes.NewReader(data))}
	var responses []map[string]any
	for {
		msg, err := reader.readMessage()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var response map[string]any
		if err := json.Unmarshal(msg, &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		responses = append(responses, response)
	}
	return responses
}
