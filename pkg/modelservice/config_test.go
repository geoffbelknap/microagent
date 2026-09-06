package modelservice

import (
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

	engine, config, err := modelrunner.ResolveRunner(modelrunner.RunnerOverrides{
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

func TestResolveModelRunnerVLLMBackend(t *testing.T) {
	python := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(python, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MICROAGENT_VLLM_PYTHON", python)

	engine, config, err := modelrunner.ResolveRunner(modelrunner.RunnerOverrides{
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
