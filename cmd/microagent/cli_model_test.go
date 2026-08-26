package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/internal/hostworker"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestResolveModelRunnerCustomCommandAllowsEnvMetadata(t *testing.T) {
	t.Setenv(modelrunner.EnvModelRunnerName, "runner-x")
	t.Setenv(modelrunner.EnvModelRunnerHealthPath, "/ready")

	engine, config, err := resolveModelRunner(modelRunnerOverrides{
		CommandRaw: "runner serve {model} --listen {addr}",
		Args:       []string{"--gpu", "auto"},
	})
	if err != nil {
		t.Fatalf("resolveModelRunner: %v", err)
	}
	if config.Name != "runner-x" || config.HealthPath != "/ready" {
		t.Fatalf("config metadata = %q %q", config.Name, config.HealthPath)
	}
	got := engine.Argv("/models/m.gguf", "127.0.0.1", 9999)
	want := []string{"runner", "serve", "/models/m.gguf", "--listen", "127.0.0.1:9999", "--gpu", "auto"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	if engine.Name() != "runner-x" || engine.HealthPath() != "/ready" {
		t.Fatalf("engine metadata = %q %q", engine.Name(), engine.HealthPath())
	}
}

func TestModelRunnerUpstreamResolverPrefersWorkerID(t *testing.T) {
	dir := t.TempDir()
	const ref = "hf.co/org/repo@main/model.gguf"
	idx := modelrunner.Index{Runners: []modelrunner.Record{
		{Key: "wrong-config", ModelRef: ref, Host: "127.0.0.1", Port: 31001, PID: os.Getpid()},
		{Key: "paired-config", ModelRef: ref, Host: "127.0.0.1", Port: 31002, PID: os.Getpid()},
	}}
	if err := modelrunner.WriteIndex(dir, idx); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}
	if got := modelRunnerUpstreamResolver(dir, "paired-config", ref)(); got != "127.0.0.1:31002" {
		t.Fatalf("resolved upstream = %q, want paired runner", got)
	}
}

func TestResolveModelRunnerVLLMBackend(t *testing.T) {
	python := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(python, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MICROAGENT_VLLM_PYTHON", python)

	engine, config, err := resolveModelRunner(modelRunnerOverrides{
		Backend:      "vllm",
		BackendModel: "Qwen/Qwen2.5-0.5B-Instruct",
		ServedModel:  "local-chat",
		Args:         []string{"--max-model-len", "2048"},
	})
	if err != nil {
		t.Fatalf("resolveModelRunner: %v", err)
	}
	if config.Backend != modelrunner.BackendVLLM || config.GPU != modelrunner.GPUOn {
		t.Fatalf("config backend/gpu = %q/%q", config.Backend, config.GPU)
	}
	got := engine.Argv("/ignored/local.gguf", "127.0.0.1", 9999)
	want := []string{python, "-m", "vllm.entrypoints.openai.api_server", "--model", "Qwen/Qwen2.5-0.5B-Instruct", "--served-model-name", "local-chat", "--host", "127.0.0.1", "--port", "9999", "--max-model-len", "2048"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestModelMediationConfigFromEnv(t *testing.T) {
	t.Run("off by default", func(t *testing.T) {
		cfg, err := modelMediationConfigFromEnv()
		if err != nil {
			t.Fatalf("modelMediationConfigFromEnv: %v", err)
		}
		if cfg.Enabled {
			t.Fatalf("cfg = %+v, want disabled", cfg)
		}
	})
	t.Run("local allow", func(t *testing.T) {
		t.Setenv(envModelMediation, "local-allow")
		t.Setenv(envModelPolicyTimeout, "250ms")
		cfg, err := modelMediationConfigFromEnv()
		if err != nil {
			t.Fatalf("modelMediationConfigFromEnv: %v", err)
		}
		if !cfg.Enabled || cfg.Mode != hostworker.ModeLocalAllow || cfg.PolicyTimeout != 250*time.Millisecond {
			t.Fatalf("cfg = %+v", cfg)
		}
	})
	t.Run("policy", func(t *testing.T) {
		t.Setenv(envModelMediation, "policy")
		t.Setenv(envModelPolicyURL, "http://127.0.0.1:8000/decide")
		t.Setenv(envModelPolicyTimeout, "2")
		cfg, err := modelMediationConfigFromEnv()
		if err != nil {
			t.Fatalf("modelMediationConfigFromEnv: %v", err)
		}
		if !cfg.Enabled || cfg.Mode != hostworker.ModePolicy || cfg.PolicyURL != "http://127.0.0.1:8000/decide" || cfg.PolicyTimeout != 2*time.Second {
			t.Fatalf("cfg = %+v", cfg)
		}
	})
	t.Run("policy file", func(t *testing.T) {
		t.Setenv(envModelMediation, "policy")
		t.Setenv(envModelPolicyFile, "/tmp/model-policy.json")
		cfg, err := modelMediationConfigFromEnv()
		if err != nil {
			t.Fatalf("modelMediationConfigFromEnv: %v", err)
		}
		if !cfg.Enabled || cfg.Mode != hostworker.ModePolicy || cfg.PolicyFile != "/tmp/model-policy.json" {
			t.Fatalf("cfg = %+v", cfg)
		}
	})
	t.Run("policy requires source", func(t *testing.T) {
		t.Setenv(envModelMediation, "policy")
		_, err := modelMediationConfigFromEnv()
		if err == nil || !strings.Contains(err.Error(), envModelPolicyURL) || !strings.Contains(err.Error(), envModelPolicyFile) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("policy rejects multiple sources", func(t *testing.T) {
		t.Setenv(envModelMediation, "policy")
		t.Setenv(envModelPolicyURL, "http://127.0.0.1:8000/decide")
		t.Setenv(envModelPolicyFile, "/tmp/model-policy.json")
		_, err := modelMediationConfigFromEnv()
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("rejects unsupported mode", func(t *testing.T) {
		t.Setenv(envModelMediation, "broker")
		_, err := modelMediationConfigFromEnv()
		if err == nil || !strings.Contains(err.Error(), envModelMediation) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestModelMediationConfigFromSpec(t *testing.T) {
	t.Setenv(envModelMediation, "local-allow")
	cfg, err := modelMediationConfigFromSpec(workspace.ModelMediationSpec{
		Mode:          "policy",
		PolicyFile:    "/tmp/model-policy.json",
		PolicyTimeout: "250ms",
	})
	if err != nil {
		t.Fatalf("modelMediationConfigFromSpec: %v", err)
	}
	if !cfg.Enabled || cfg.Mode != hostworker.ModePolicy || cfg.PolicyFile != "/tmp/model-policy.json" || cfg.PolicyTimeout != 250*time.Millisecond {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestModelPolicyValidateAndEvaluate(t *testing.T) {
	policyPath := writeModelPolicyTestFile(t, `{
		"schema_version": "microagent.model_policy.v1",
		"default": "deny",
		"rules": [
			{
				"id": "models",
				"effect": "allow",
				"match": {"methods": ["GET"], "paths": ["/v1/models"]}
			},
			{
				"id": "chat",
				"effect": "allow",
				"match": {"methods": ["POST"], "paths": ["/v1/chat/completions"], "models": ["tiny"]},
				"limits": {
					"max_text_bytes": 16,
					"max_messages": 2,
					"max_tokens": 16,
					"stream": false,
					"allowed_tool_names": ["shell"]
				}
			}
		]
	}`)

	validateOut, err := runMainForTest(t, "--json", "model", "policy", "validate", policyPath)
	if err != nil {
		t.Fatalf("policy validate: %v\n%s", err, validateOut)
	}
	var validation modelPolicyValidationOutput
	if err := json.Unmarshal(validateOut, &validation); err != nil {
		t.Fatalf("decode validation output: %v\n%s", err, validateOut)
	}
	if !validation.OK || validation.Rules != 2 || validation.SHA256 == "" || validation.Path == "" {
		t.Fatalf("validation = %+v", validation)
	}

	allowOut, err := runMainForTest(t,
		"--json", "model", "policy", "evaluate", policyPath,
		"--method", "POST",
		"--path", "/v1/chat/completions",
		"--model", "tiny",
		"--max-tokens", "8",
		"--stream", "false",
		"--tool", "shell",
		"--text-bytes", "5",
		"--messages", "1",
		"--expect", "allow",
	)
	if err != nil {
		t.Fatalf("policy evaluate allow: %v\n%s", err, allowOut)
	}
	var allowEval modelPolicyEvaluationOutput
	if err := json.Unmarshal(allowOut, &allowEval); err != nil {
		t.Fatalf("decode allow output: %v\n%s", err, allowOut)
	}
	if allowEval.Decision != "allow" || allowEval.RuleID != "chat" || !allowEval.MatchedExpect {
		t.Fatalf("allow evaluation = %+v", allowEval)
	}

	denyOut, err := runMainForTest(t,
		"--json", "model", "policy", "evaluate", policyPath,
		"--method", "POST",
		"--path", "/v1/chat/completions",
		"--model", "tiny",
		"--max-tokens", "8",
		"--stream", "false",
		"--tool", "network",
		"--text-bytes", "5",
		"--messages", "1",
		"--expect", "deny",
	)
	if err != nil {
		t.Fatalf("policy evaluate deny: %v\n%s", err, denyOut)
	}
	var denyEval modelPolicyEvaluationOutput
	if err := json.Unmarshal(denyOut, &denyEval); err != nil {
		t.Fatalf("decode deny output: %v\n%s", err, denyOut)
	}
	if denyEval.Decision != "deny" || denyEval.Reason != "file_policy_limit_tool_name" || !denyEval.MatchedExpect {
		t.Fatalf("deny evaluation = %+v", denyEval)
	}

	mismatchOut, err := runMainForTest(t,
		"--json", "model", "policy", "evaluate", policyPath,
		"--method", "POST",
		"--path", "/v1/chat/completions",
		"--model", "tiny",
		"--max-tokens", "32",
		"--stream", "false",
		"--expect", "allow",
	)
	if err == nil || !strings.Contains(err.Error(), "did not match expected") {
		t.Fatalf("expected mismatch error, got err=%v out=%s", err, mismatchOut)
	}
	var mismatchEval modelPolicyEvaluationOutput
	if err := json.Unmarshal(mismatchOut, &mismatchEval); err != nil {
		t.Fatalf("decode mismatch output: %v\n%s", err, mismatchOut)
	}
	if mismatchEval.Decision != "deny" || mismatchEval.MatchedExpect {
		t.Fatalf("mismatch evaluation = %+v", mismatchEval)
	}
}

func TestModelPolicyValidateRejectsInvalidPolicy(t *testing.T) {
	policyPath := writeModelPolicyTestFile(t, `{"schema_version":"wrong","default":"allow"}`)
	out, err := runMainForTest(t, "model", "policy", "validate", policyPath)
	if err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("expected schema error, got err=%v out=%s", err, out)
	}
}

func TestModelPolicyEvalSpellingWorks(t *testing.T) {
	t.Cleanup(func() { outputFormat = "" })
	// "eval" is the pre-existing short spelling; verify it reaches evaluate behavior.
	policyPath := writeModelPolicyTestFile(t, `{
		"schema_version": "microagent.model_policy.v1",
		"default": "deny",
		"rules": [
			{
				"id": "allow_all",
				"effect": "allow",
				"match": {"methods": ["GET"], "paths": ["*"]}
			}
		]
	}`)

	evalOut, err := runMainForTest(t,
		"--json", "model", "policy", "eval", policyPath,
		"--method", "GET",
		"--path", "/v1/models",
		"--expect", "allow",
	)
	if err != nil {
		t.Fatalf("policy eval (using 'eval' alias): %v\n%s", err, evalOut)
	}
	var evalResult modelPolicyEvaluationOutput
	if err := json.Unmarshal(evalOut, &evalResult); err != nil {
		t.Fatalf("decode eval output: %v\n%s", err, evalOut)
	}
	if evalResult.Decision != "allow" || !evalResult.MatchedExpect {
		t.Fatalf("eval result = %+v", evalResult)
	}
}

func runMainForTest(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	stdoutPath := filepath.Join(t.TempDir(), "stdout")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	runErr := run(t.Context(), args, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	out, readErr := os.ReadFile(stdoutPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return out, runErr
}

func writeModelPolicyTestFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	return path
}
