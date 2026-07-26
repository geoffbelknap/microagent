package main

import (
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func mcpTools() []map[string]any {
	return []map[string]any{
		mcpTool("microagent.ping", "Test tool for validating the microagent MCP transport.", nil, nil),
		mcpTool("microagent.describe", "Return the machine-readable microagent MCP capability manifest.", nil, nil),
		mcpTool("workspace.create", "Create or dry-run a workspace, including snapshot forks.", []string{"name"}, map[string]any{
			"name": map[string]any{"type": "string"}, "image": map[string]any{"type": "string"}, "exec": map[string]any{"type": "string"},
			"state_dir": map[string]any{"type": "string"}, "profile": map[string]any{"type": "string"}, "dry_run": map[string]any{"type": "boolean"},
			"from_snapshot": map[string]any{"type": "string", "description": "Fork from <workspace>:<tag> instead of creating from an image"},
			"model":         map[string]any{"type": "string"}, "model_token": map[string]any{"type": "string"},
			"model_runner":              map[string]any{"type": "string", "enum": []string{"llamacpp", "vllm", "custom"}},
			"model_gpu":                 map[string]any{"type": "string", "enum": []string{"off", "on", "auto"}},
			"model_runner_model":        map[string]any{"type": "string"},
			"model_runner_served_model": map[string]any{"type": "string"},
			"model_runner_command":      map[string]any{"type": "string"},
			"model_runner_name":         map[string]any{"type": "string"},
			"model_runner_health_path":  map[string]any{"type": "string"},
			"model_runner_args":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"model_runner_env":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"model_mediation":           map[string]any{"type": "string", "enum": []string{"off", "local-allow", "policy"}},
			"model_policy_url":          map[string]any{"type": "string"},
			"model_policy_file":         map[string]any{"type": "string"},
			"model_policy_timeout":      map[string]any{"type": "string"},
			"network":                   map[string]any{"type": "string", "enum": []string{"user", "nat", "isolated"}},
			"egress":                    map[string]any{"type": "string", "enum": []string{"broker", "mitm", "off"}, "description": "Egress mediation mode (default broker)"},
			"egress_allow":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Allowlisted egress hosts; .suffix matches subdomains. In broker/mitm this ADDS to the allow-broad default — set egress_lock_allowlist to restrict egress to only these"},
			"egress_lock_allowlist":     map[string]any{"type": "boolean", "description": "Restrict egress to the allowlisted hosts only (drop the allow-broad default) in broker/mitm mode"},
			"egress_passthrough":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Egress hosts allowed without TLS interception"},
			"egress_policy":             map[string]any{"type": "string", "description": "Path to an egress policy file (.yaml/.yml/.json)"},
			"egress_swap_config":        map[string]any{"type": "string", "description": "Credential-swap config path; mediator injects the real secret host-side so the guest never holds it"},
			"cred_swap":                 map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Inject a built-in provider's API key host-side: PROVIDER[=env:NAME|file:PATH|vault:PATH] (e.g. anthropic, openai). Guest never holds the key; reference only, never a literal"},
			"broker_upstream":           map[string]any{"type": "string", "description": "Egress broker upstream base URL; the broker injects the credential host-side and originates its own TLS, so the guest never holds the key"},
			"broker_secret":             map[string]any{"type": "string", "description": "Broker credential NAME=scheme:ref; held host-side only, the guest sends @secret:NAME references. Requires broker_upstream"},
			"broker_env":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Guest env vars pointed at the broker, each KEY[=VALUE] (empty VALUE = broker URL)"},
			"broker_proxy":              map[string]any{"type": "boolean", "description": "Also set HTTPS_PROXY/HTTP_PROXY in the guest to the broker (CONNECT tunneling)"},
			"broker_capture":            map[string]any{"type": "boolean", "description": "Opt in to raw capture of pre-swap broker requests to an owner-only file; default is the minimized decision stream"},
			"broker_ca":                 map[string]any{"type": "string", "description": "PEM bundle path the broker upstream TLS client trusts; empty = system roots"},
			"brokers":                   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Declare multiple broker endpoints instead of a single broker_*; each item is ;-separated key=value pairs: upstream=<url>;secret=NAME=<scheme>:<ref>;base-url-env=KEY[=VALUE];ca=<path>;proxy;capture. Cannot combine with broker_upstream/broker_secret/etc"},
			"secret":                    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Secrets delivered to tmpfs /run/secrets, each NAME=scheme:ref"},
			"secret_on_demand":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "On-demand secrets NAME=scheme:ref; fetched at runtime, never written to tmpfs"},
			"secrets_env_file":          map[string]any{"type": "string", "description": "Dotenv file whose keys are delivered as secrets"},
			"secrets_audit":             map[string]any{"type": "boolean", "description": "Append every secret access to the workspace audit log"},
		}),
		mcpTool("workspace.start", "Start a prepared workspace, optionally restoring from a snapshot.", []string{"name"}, map[string]any{
			"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"},
			"from_snapshot":             map[string]any{"type": "string", "description": "Restore the workspace in place from this snapshot tag"},
			"model_runner":              map[string]any{"type": "string", "enum": []string{"llamacpp", "vllm", "custom"}},
			"model_gpu":                 map[string]any{"type": "string", "enum": []string{"off", "on", "auto"}},
			"model_runner_model":        map[string]any{"type": "string"},
			"model_runner_served_model": map[string]any{"type": "string"},
			"model_runner_command":      map[string]any{"type": "string"},
			"model_runner_name":         map[string]any{"type": "string"},
			"model_runner_health_path":  map[string]any{"type": "string"},
			"model_runner_args":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"model_runner_env":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"model_mediation":           map[string]any{"type": "string", "enum": []string{"off", "local-allow", "policy"}},
			"model_policy_url":          map[string]any{"type": "string"},
			"model_policy_file":         map[string]any{"type": "string"},
			"model_policy_timeout":      map[string]any{"type": "string"},
		}),
		mcpTool("workspace.wait", "Block until a workspace reaches a terminal state (stopped, halted, failed, quarantined, or prepared) and report it, replacing workspace.inspect polling loops.", []string{"name"}, map[string]any{
			"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"},
			"timeout":  map[string]any{"type": "string", "description": "Give up after this long (Go duration, e.g. 30s, 5m); empty or 0 waits until the client cancels"},
			"interval": map[string]any{"type": "string", "description": "Delay between state checks (Go duration; default 1s)"},
		}),
		mcpTool("workspace.exec", "Run a structured command in a running workspace.", []string{"name"}, workspaceExecInputSchema()),
		mcpTool("workspace.dispatch", "Run one task in a fresh, isolated, single-use workspace under egress guardrails, then tear it down. Returns the guest result plus a mediator-written summary of what the workspace reached on the network, so a caller can judge whether the task stayed on-intent. Ideal for delegating untrusted or parallel work to its own machine.", []string{"image"}, map[string]any{
			"image":                 map[string]any{"type": "string", "description": "OCI image to boot"},
			"exec":                  map[string]any{"type": "string", "description": "Command to run in the workspace"},
			"state_dir":             map[string]any{"type": "string"},
			"timeout":               map[string]any{"type": "string", "description": "Max wall-clock for the task (e.g. 5m)"},
			"network":               map[string]any{"type": "string", "enum": []string{"user", "nat", "isolated"}},
			"egress":                map[string]any{"type": "string", "enum": []string{"broker", "mitm", "off"}, "description": "Egress mediation mode (default broker)"},
			"egress_allow":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Allowlisted egress hosts; .suffix matches subdomains. In broker/mitm this ADDS to the allow-broad default — set egress_lock_allowlist to restrict egress to only these"},
			"egress_lock_allowlist": map[string]any{"type": "boolean", "description": "Restrict egress to the allowlisted hosts only (drop the allow-broad default) in broker/mitm mode"},
			"egress_passthrough":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Egress hosts allowed without TLS interception"},
			"egress_policy":         map[string]any{"type": "string", "description": "Path to an egress policy file (.yaml/.yml/.json)"},
			"egress_swap_config":    map[string]any{"type": "string", "description": "Credential-swap config path; mediator injects the real secret host-side so the guest never holds it"},
			"cred_swap":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Inject a built-in provider's API key host-side: PROVIDER[=env:NAME|file:PATH|vault:PATH] (e.g. anthropic, openai). Guest never holds the key; reference only, never a literal"},
			"broker_upstream":       map[string]any{"type": "string", "description": "Egress broker upstream base URL; the broker injects the credential host-side and originates its own TLS, so the guest never holds the key"},
			"broker_secret":         map[string]any{"type": "string", "description": "Broker credential NAME=scheme:ref; held host-side only, the guest sends @secret:NAME references. Requires broker_upstream"},
			"broker_env":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Guest env vars pointed at the broker, each KEY[=VALUE] (empty VALUE = broker URL)"},
			"broker_proxy":          map[string]any{"type": "boolean", "description": "Also set HTTPS_PROXY/HTTP_PROXY in the guest to the broker (CONNECT tunneling)"},
			"broker_capture":        map[string]any{"type": "boolean", "description": "Opt in to raw capture of pre-swap broker requests to an owner-only file; default is the minimized decision stream"},
			"broker_ca":             map[string]any{"type": "string", "description": "PEM bundle path the broker upstream TLS client trusts; empty = system roots"},
			"brokers":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Declare multiple broker endpoints instead of a single broker_*; each item is ;-separated key=value pairs: upstream=<url>;secret=NAME=<scheme>:<ref>;base-url-env=KEY[=VALUE];ca=<path>;proxy;capture. Cannot combine with broker_upstream/broker_secret/etc"},
			"secret":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Secrets delivered to tmpfs /run/secrets, each NAME=scheme:ref"},
			"secret_on_demand":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "On-demand secrets NAME=scheme:ref; never written to tmpfs"},
			"secrets_env_file":      map[string]any{"type": "string", "description": "Dotenv file whose keys are delivered as secrets"},
			"secrets_audit":         map[string]any{"type": "boolean", "description": "Append every secret access to the workspace audit log"},
		}),
		mcpTool("workspace.halt", "Halt a workspace and preserve disk state.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.kill", "Force stop a workspace runtime.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.quarantine", "Sever host-side network and mediation for a workspace.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.pause", "Pause a running workspace when the backend supports pause/resume.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.resume", "Resume a paused workspace when the backend supports pause/resume.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.delete", "Delete a workspace.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "force": map[string]any{"type": "boolean"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("workspace.list", "List workspaces.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.inspect", "Inspect workspace state.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "format": map[string]any{"type": "string", "enum": []string{"summary", "full"}}}),
		mcpTool("workspace.result", "Read the structured workspace result.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.stats", "Sample current workspace resource usage.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.logs", "Read workspace serial logs. Defaults to a compact tail summary; pass format=full for the complete log buffer.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "format": map[string]any{"type": "string", "enum": []string{"summary", "full"}}, "tail_lines": map[string]any{"type": "integer"}}),
		mcpTool("workspace.events", "Read workspace lifecycle events. Defaults to a compact recent-event summary; pass format=full for all events.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "format": map[string]any{"type": "string", "enum": []string{"summary", "full"}}, "limit": map[string]any{"type": "integer"}, "after_index": map[string]any{"type": "integer"}}),
		mcpTool("workspace.egress", "Read the egress mediator's audit decisions (allow/deny/MITM/DNS/UDP) for a workspace. Egress mediation is on by default; an absent audit log returns an empty list, not an error.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.clone", "Clone a stopped workspace to a new workspace name.", []string{"source", "target"}, map[string]any{"source": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("workspace.apply", "Apply supported changes from a workspace spec file.", []string{"file"}, map[string]any{"file": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "supervisor": map[string]any{"type": "string"}}),
		mcpTool("workspace.commit", "Commit a stopped workspace rootfs into a local OCI image, optionally pushing it.", []string{"name", "image"}, map[string]any{"name": map[string]any{"type": "string"}, "image": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "push": map[string]any{"type": "boolean"}}),
		mcpTool("workspace.estimate_cost", "Estimate workspace resource consumption before creating or starting it.", nil, map[string]any{"profile": map[string]any{"type": "string"}, "memory_mib": map[string]any{"type": "integer"}, "cpus": map[string]any{"type": "integer"}, "size_mib": map[string]any{"type": "integer"}, "price_per_hour": map[string]any{"type": "number"}}),
		mcpTool("artifacts.list", "List declared workspace artifacts.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("snapshot.create", "Create a backend snapshot for a workspace when supported. Set forensic to capture for investigation: guest secrets are retained and the capture is not restorable.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "tag": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "forensic": map[string]any{"type": "boolean"}}),
		mcpTool("snapshot.list", "List snapshots for a workspace.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("snapshot.delete", "Delete a workspace snapshot.", []string{"name", "tag"}, map[string]any{"name": map[string]any{"type": "string"}, "tag": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("network.inspect", "Inspect a workspace's network.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("volume.create", "Create a named managed ext4 volume.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "size_mib": map[string]any{"type": "integer"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("volume.list", "List named managed volumes.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("volume.inspect", "Inspect one named managed volume.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("volume.delete", "Delete a named managed volume.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "force": map[string]any{"type": "boolean"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("images.pull", "Pull a reusable image rootfs.", []string{"image"}, map[string]any{"image": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}}),
		mcpTool("images.list", "List reusable local image records.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("images.push", "Push a locally committed OCI image to its registry.", []string{"image"}, map[string]any{"image": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("images.tag", "Tag a local image record.", []string{"source", "target"}, map[string]any{"source": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("images.delete", "Delete a local image record, optionally deleting cached rootfs files.", []string{"image"}, map[string]any{"image": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "delete_files": map[string]any{"type": "boolean"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("images.prune", "Prune stale local image records, optionally deleting cached rootfs files.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}, "delete_files": map[string]any{"type": "boolean"}, "preview": map[string]any{"type": "boolean"}}),
		mcpTool("models.pull", "Pull a GGUF model from HuggingFace into the local store.", []string{"model"}, map[string]any{"model": map[string]any{"type": "string"}, "token": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.list", "List locally stored models.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.remove", "Remove a model from the local store.", []string{"model"}, map[string]any{"model": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.prune", "Prune local model records whose blobs are missing.", nil, map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.serve", "Start or reuse a local host model server for a stored or pulled model.", []string{"model"},
			map[string]any{
				"model":               map[string]any{"type": "string"},
				"dedicated":           map[string]any{"type": "boolean"},
				"runner":              map[string]any{"type": "string", "enum": []string{"llamacpp", "vllm", "custom"}},
				"runner_gpu":          map[string]any{"type": "string", "enum": []string{"off", "on", "auto"}},
				"runner_model":        map[string]any{"type": "string"},
				"runner_served_model": map[string]any{"type": "string"},
				"runner_command":      map[string]any{"type": "string"},
				"runner_name":         map[string]any{"type": "string"},
				"runner_health_path":  map[string]any{"type": "string"},
				"runner_args":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"runner_env":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"token":               map[string]any{"type": "string"},
				"state_dir":           map[string]any{"type": "string"},
			}),
		mcpTool("models.stop", "Stop local host model server instances for a model.", []string{"model"},
			map[string]any{"model": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.runners", "List running local model servers.", nil,
			map[string]any{"state_dir": map[string]any{"type": "string"}}),
		mcpTool("models.policy.validate", "Validate a structured model mediation policy file.", []string{"policy_file"},
			map[string]any{"policy_file": map[string]any{"type": "string"}}),
		mcpTool("models.policy.evaluate", "Dry-run a structured model mediation policy file against request metadata.", []string{"policy_file"},
			map[string]any{
				"policy_file":   map[string]any{"type": "string"},
				"method":        map[string]any{"type": "string"},
				"request_path":  map[string]any{"type": "string"},
				"workspace_id":  map[string]any{"type": "string"},
				"capability":    map[string]any{"type": "string"},
				"worker_id":     map[string]any{"type": "string"},
				"model":         map[string]any{"type": "string"},
				"request_bytes": map[string]any{"type": "integer"},
				"text_bytes":    map[string]any{"type": "integer"},
				"messages":      map[string]any{"type": "integer"},
				"max_tokens":    map[string]any{"type": "integer"},
				"stream":        map[string]any{"type": "boolean"},
				"tools":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"expect":        map[string]any{"type": "string", "enum": []string{"allow", "deny"}},
			}),
		mcpTool("profiles.list", "List resource profiles.", nil, nil),
		mcpTool("host.inspect", "Report host capabilities for the selected backend.", nil, map[string]any{"backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "supervisor": map[string]any{"type": "string"}}),
		mcpTool("doctor.check", "Run host diagnostics for the selected backend.", nil, map[string]any{"backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "supervisor": map[string]any{"type": "string"}}),
		mcpTool("contract.get", "Return the backend-neutral runtime contract.", nil, nil),
		mcpTool("kernel.verify", "Verify the configured or supplied kernel artifact.", nil, map[string]any{"path": map[string]any{"type": "string"}, "sha256": map[string]any{"type": "string"}, "backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}}),
		mcpTool("kernel.install", "Install a kernel artifact after preview confirmation.", nil, map[string]any{"url": map[string]any{"type": "string"}, "from": map[string]any{"type": "string"}, "sha256": map[string]any{"type": "string"}, "out": map[string]any{"type": "string"}, "backend": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "preview": map[string]any{"type": "boolean"}, "confirm_token": map[string]any{"type": "string"}}),
		mcpTool("rootfs.build", "Build a rootfs from an OCI image after preview confirmation.", []string{"image"}, map[string]any{"image": map[string]any{"type": "string"}, "os": map[string]any{"type": "string"}, "arch": map[string]any{"type": "string"}, "out": map[string]any{"type": "string"}, "init": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}, "mke2fs": map[string]any{"type": "string"}, "size_mib": map[string]any{"type": "integer"}, "exec": map[string]any{"type": "string"}, "allow_mutable": map[string]any{"type": "boolean"}, "keep_stage": map[string]any{"type": "boolean"}, "stage_snapshot": map[string]any{"type": "string"}, "preview": map[string]any{"type": "boolean"}, "confirm_token": map[string]any{"type": "string"}}),
		mcpTool("cp", "Copy files into or out of a stopped workspace.", []string{"source", "target"}, map[string]any{"source": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
		mcpTool("artifacts.get", "Copy a declared workspace artifact out to the host.", []string{"name", "artifact", "target"}, map[string]any{"name": map[string]any{"type": "string"}, "artifact": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}, "state_dir": map[string]any{"type": "string"}}),
	}
}

func mcpTool(name, description string, required []string, properties map[string]any) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	if mcpMutationTool(name) {
		properties["idempotency_key"] = map[string]any{"type": "string"}
	}
	properties["principal"] = principalContextSchema()
	inputSchema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": true,
	}
	if len(required) > 0 {
		inputSchema["required"] = required
	}
	return map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": inputSchema,
	}
}

func microagentCapabilityManifest() map[string]any {
	operations := make([]map[string]any, 0, len(mcpTools()))
	for _, tool := range mcpTools() {
		name, _ := tool["name"].(string)
		if name == "microagent.ping" {
			continue
		}
		operation, _ := vmkit.OperationForMCPTool(name)
		operations = append(operations, map[string]any{
			"operation_id":          operation.ID,
			"feature_id":            operation.FeatureID,
			"required_capabilities": operation.RequiredCapabilities,
			"request_type":          operation.RequestType,
			"result_type":           operation.ResultType,
			"name":                  name,
			"description":           tool["description"],
			"input_schema":          tool["inputSchema"],
			"output_schema":         mcpToolOutputSchema(name),
			"side_effects":          mcpToolSideEffects(name),
			"idempotency":           mcpToolIdempotency(name),
			"principal_scope":       mcpToolPrincipalScope(name),
			"cost_class":            mcpToolCostClass(name),
			"structured_errors":     []string{string(errorKindTransient), string(errorKindPermanent), string(errorKindConflict), string(errorKindNotFound), string(errorKindResourceExhausted), string(errorKindUnsupported), string(errorKindPolicyDenied)},
			"correlation_id_key":    "error.data.correlation_id",
		})
	}
	return map[string]any{
		"schema_version":    "2026-07-22",
		"service":           "microagent",
		"version":           version,
		"transport":         "mcp_stdio",
		"response_envelope": mcpResponseEnvelopeSchema(),
		"agent_experience": map[string]any{
			"defaults": []string{
				"parse every tool payload as the unified envelope {ok, result, meta}: read the answer from result, transport facts (timing_ms, principal_context, idempotency_replay, retry metadata) from meta",
				"read failures from the JSON-RPC error.data object: the structuredError fields (kind, message, remediation, retryable, correlation_id) with the same meta block attached as a sibling",
				"use compact summary outputs for repeated state checks",
				"request format=full only when a complete log, event, or inspect payload is needed",
				"use tail_lines and after_index for bounded stream polling instead of long-running follow calls",
				"use preview=true before destructive delete operations when user confirmation is still pending",
				"use the preview confirmation_token for host-mutating install/setup/build operations",
				"use idempotency_key on retryable mutation calls",
			},
			"evidence": "agent-experience harness runs showed lower token waste when agents used compact structured MCP state instead of scraping prose or repeatedly requesting full state",
		},
		"readiness_signals": []map[string]string{
			{"name": "guestReady", "description": "workspace reached a started terminal or runtime state"},
			{"name": "shellReady", "description": "interactive console shell is reachable and command round-trip works"},
			{"name": "execReady", "description": "structured exec service is reachable and a no-op exec succeeds end-to-end"},
			{"name": "resultReady", "description": "structured guest result is available"},
			{"name": "mediationReady", "description": "declared mediation channel target is live reachable for a running workspace"},
		},
		"operations": operations,
	}
}

// mcpToolOutputSchema describes the successful tool payload: the unified
// envelope {ok:true, result, meta}. Failures are not part of the tool payload;
// they arrive as a JSON-RPC error whose data follows response_envelope.error
// (see mcpResponseEnvelopeSchema).
func mcpToolOutputSchema(name string) map[string]any {
	if name == "workspace.exec" {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ok":     map[string]any{"type": "boolean"},
				"result": execResultSchema(),
				"meta":   mcpMetaSchema(true),
			},
		}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ok": map[string]any{"type": "boolean"},
			"result": map[string]any{"type": "object", "properties": map[string]any{
				"readiness": map[string]any{"type": "object", "properties": map[string]any{
					"guestReady":     readinessSignalSchema(),
					"shellReady":     readinessSignalSchema(),
					"execReady":      readinessSignalSchema(),
					"resultReady":    readinessSignalSchema(),
					"mediationReady": readinessSignalSchema(),
				}},
			}},
			"meta": mcpMetaSchema(false),
		},
	}
}

// mcpMetaSchema describes the transport `meta` block attached to every MCP
// response (success payload and error.data alike). withRetry adds the exec
// retry-metadata fields.
func mcpMetaSchema(withRetry bool) map[string]any {
	props := map[string]any{
		"timing_ms":          map[string]any{"type": "integer"},
		"principal_context":  map[string]any{"type": "object"},
		"idempotency_replay": map[string]any{"type": "boolean"},
	}
	if withRetry {
		props["retry_count"] = map[string]any{"type": "integer"}
		props["retry_wall_clock_ms"] = map[string]any{"type": "integer"}
		props["retry_exhausted"] = map[string]any{"type": "boolean"}
	}
	return map[string]any{"type": "object", "properties": props}
}

// mcpStructuredErrorSchema describes the structuredError object carried in
// JSON-RPC error.data.
func mcpStructuredErrorSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind":           map[string]any{"type": "string", "enum": []string{string(errorKindTransient), string(errorKindPermanent), string(errorKindConflict), string(errorKindNotFound), string(errorKindResourceExhausted), string(errorKindUnsupported), string(errorKindPolicyDenied)}},
			"message":        map[string]any{"type": "string"},
			"remediation":    map[string]any{"type": "string"},
			"retryable":      map[string]any{"type": "boolean"},
			"retry_after_ms": map[string]any{"type": "integer"},
			"partial_output": map[string]any{"type": "string"},
			"correlation_id": map[string]any{"type": "string"},
		},
	}
}

// mcpResponseEnvelopeSchema documents the two response shapes for a tool call:
// a success payload {ok:true, result, meta} returned inside the MCP tool
// content, and a failure surfaced as a JSON-RPC error whose data is the
// structuredError with a sibling meta block.
func mcpResponseEnvelopeSchema() map[string]any {
	errData := mcpStructuredErrorSchema()
	errProps, _ := errData["properties"].(map[string]any)
	if errProps != nil {
		errProps["meta"] = mcpMetaSchema(true)
	}
	return map[string]any{
		"discriminator": "ok",
		"success": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ok":     map[string]any{"const": true},
				"result": map[string]any{"description": "operation payload; see each operation's output_schema"},
				"meta":   mcpMetaSchema(true),
			},
		},
		"error": map[string]any{
			"description": "delivered as a JSON-RPC error; error.data carries these fields",
			"data":        errData,
		},
	}
}

func workspaceExecInputSchema() map[string]any {
	return map[string]any{
		"name":                      map[string]any{"type": "string"},
		"command":                   map[string]any{"description": "Legacy shell command string, or argv array.", "oneOf": []map[string]any{{"type": "string"}, {"type": "array", "items": map[string]any{"type": "string"}}}},
		"argv":                      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"env":                       map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"cwd":                       map[string]any{"type": "string"},
		"stdin":                     map[string]any{"type": "string", "description": "Bounded stdin bytes represented as a JSON string."},
		"timeout_ms":                map[string]any{"type": "integer"},
		"output_limit_bytes_stdout": map[string]any{"type": "integer"},
		"output_limit_bytes_stderr": map[string]any{"type": "integer"},
		"state_dir":                 map[string]any{"type": "string"},
	}
}

func execResultSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"protocol_version": map[string]any{"type": "string"},
			"started_at":       map[string]any{"type": "string"},
			"completed_at":     map[string]any{"type": "string"},
			"exit_code":        map[string]any{"type": "integer"},
			"stdout":           map[string]any{"type": "string", "contentEncoding": "base64"},
			"stderr":           map[string]any{"type": "string", "contentEncoding": "base64"},
			"stdout_truncated": map[string]any{"type": "boolean"},
			"stderr_truncated": map[string]any{"type": "boolean"},
			"status":           map[string]any{"type": "string", "enum": []string{string(execprotocol.ExecStatusExited), string(execprotocol.ExecStatusSignaled), string(execprotocol.ExecStatusTimedOut), string(execprotocol.ExecStatusFailedToStart)}},
			"error":            map[string]any{"type": "object"},
		},
	}
}

func readinessSignalSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ready":      map[string]any{"type": "boolean"},
			"observedAt": map[string]any{"type": "string"},
			"detail":     map[string]any{"type": "string"},
			"error":      map[string]any{"type": "string"},
		},
	}
}

func mcpToolSideEffects(name string) []string {
	if operation, ok := vmkit.OperationForMCPTool(name); ok {
		if len(operation.SideEffects) == 0 {
			return nil
		}
		effects := make([]string, len(operation.SideEffects))
		for i, effect := range operation.SideEffects {
			effects[i] = string(effect)
		}
		return effects
	}
	return nil
}

func mcpToolIdempotency(name string) string {
	if operation, ok := vmkit.OperationForMCPTool(name); ok {
		switch operation.Idempotency {
		case vmkit.OperationIdempotencyReadOnly:
			return "read_only"
		case vmkit.OperationIdempotencyReplayable:
			return "accepts idempotency_key; identical retries by the same principal replay the first completed response for 15 minutes"
		case vmkit.OperationIdempotencyKeyedReplay:
			return "not inherently idempotent; idempotency_key coalesces concurrent identical calls and replays the first completed response for 15 minutes"
		default:
			return "not_idempotent"
		}
	}
	return "not_idempotent"
}

func mcpToolPrincipalScope(name string) []string {
	switch name {
	case "workspace.dispatch", "workspace.create", "workspace.start", "workspace.wait", "workspace.exec", "workspace.halt", "workspace.kill", "workspace.quarantine", "workspace.pause", "workspace.resume", "workspace.delete", "workspace.list", "workspace.inspect", "workspace.result", "workspace.stats", "workspace.logs", "workspace.events", "workspace.egress", "workspace.clone", "workspace.apply", "workspace.commit":
		return []string{"workspace.lifecycle"}
	case "snapshot.create", "snapshot.list", "snapshot.delete":
		return []string{"workspace.snapshot"}
	case "network.inspect":
		return []string{"network.read"}
	case "volume.create", "volume.list", "volume.inspect", "volume.delete":
		return []string{"volume.read", "volume.write"}
	case "images.pull", "images.list", "images.push", "images.tag", "images.delete", "images.prune":
		return []string{"images.read", "images.write"}
	case "artifacts.list", "cp", "artifacts.get":
		return []string{"workspace.files"}
	case "models.pull", "models.list", "models.remove", "models.prune", "models.serve", "models.stop", "models.runners", "models.policy.validate", "models.policy.evaluate":
		return []string{"models.read", "models.write"}
	case "kernel.install", "rootfs.build":
		return []string{"host.write"}
	case "host.inspect", "doctor.check", "contract.get", "kernel.verify", "profiles.list":
		return []string{"host.read"}
	default:
		return []string{"microagent.read"}
	}
}

func mcpToolCostClass(name string) string {
	switch name {
	case "workspace.dispatch", "workspace.create", "workspace.start", "workspace.exec", "workspace.clone", "workspace.apply", "workspace.commit", "snapshot.create", "snapshot.delete", "volume.create", "volume.delete", "images.pull", "images.push", "images.tag", "images.delete", "images.prune", "models.pull", "models.serve", "kernel.install", "rootfs.build":
		return "host_compute_and_storage"
	case "cp", "artifacts.get", "models.remove", "models.prune", "models.stop":
		return "host_io"
	default:
		return "metadata"
	}
}

func mcpMutationTool(name string) bool {
	if operation, ok := vmkit.OperationForMCPTool(name); ok {
		return operation.Effect != vmkit.OperationEffectRead
	}
	return false
}

func principalContextSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workload_identity":   map[string]any{"type": "string"},
			"delegated_authority": map[string]any{"type": "string"},
			"purpose":             map[string]any{"type": "string"},
			"correlation_id":      map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}
