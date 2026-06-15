package modelrunner

import (
	"reflect"
	"testing"
)

func TestParseRunnerArgs(t *testing.T) {
	got, err := ParseRunnerArgs(`-ngl all --ctx-size "8 192" '--dry'`)
	if err != nil {
		t.Fatalf("ParseRunnerArgs: %v", err)
	}
	want := []string{"-ngl", "all", "--ctx-size", "8 192", "--dry"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}

	got, err = ParseRunnerArgs(`["-ngl","all"]`)
	if err != nil {
		t.Fatalf("ParseRunnerArgs JSON: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"-ngl", "all"}) {
		t.Fatalf("json args = %#v", got)
	}
}

func TestRunnerConfigFromEnvCustomCommand(t *testing.T) {
	t.Setenv(EnvModelRunnerCommand, `runner serve {model} --listen {addr}`)
	t.Setenv(EnvModelRunnerName, "runner-x")
	t.Setenv(EnvModelRunnerHealthPath, "/ready")
	t.Setenv(EnvModelRunnerArgs, `--gpu auto`)
	t.Setenv(EnvModelRunnerEnv, `{"CUDA_VISIBLE_DEVICES":"0"}`)

	got, err := RunnerConfigFromEnv()
	if err != nil {
		t.Fatalf("RunnerConfigFromEnv: %v", err)
	}
	if !reflect.DeepEqual(got.Command, []string{"runner", "serve", "{model}", "--listen", "{addr}"}) {
		t.Fatalf("command = %#v", got.Command)
	}
	if got.Name != "runner-x" || got.HealthPath != "/ready" {
		t.Fatalf("metadata = %q %q", got.Name, got.HealthPath)
	}
	if !reflect.DeepEqual(got.Args, []string{"--gpu", "auto"}) {
		t.Fatalf("args = %#v", got.Args)
	}
	if !reflect.DeepEqual(got.Env, []string{"CUDA_VISIBLE_DEVICES=0"}) {
		t.Fatalf("env = %#v", got.Env)
	}
}

func TestRunnerConfigRejectsIncompleteCustomCommand(t *testing.T) {
	t.Setenv(EnvModelRunnerCommand, `runner serve {model}`)
	if _, err := RunnerConfigFromEnv(); err == nil {
		t.Fatal("expected missing port/addr error")
	}
}

func TestParseRunnerEnv(t *testing.T) {
	got, err := ParseRunnerEnv(`CUDA_VISIBLE_DEVICES=0 GGML_CUDA_ENABLE_UNIFIED_MEMORY=1`)
	if err != nil {
		t.Fatalf("ParseRunnerEnv: %v", err)
	}
	want := []string{"CUDA_VISIBLE_DEVICES=0", "GGML_CUDA_ENABLE_UNIFIED_MEMORY=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env = %#v, want %#v", got, want)
	}

	got, err = ParseRunnerEnv(`{"B":"2","A":"1"}`)
	if err != nil {
		t.Fatalf("ParseRunnerEnv JSON: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"A=1", "B=2"}) {
		t.Fatalf("json env = %#v", got)
	}
}

func TestRunnerConfigDigestAndKeys(t *testing.T) {
	cfg, err := NewRunnerConfig([]string{"-ngl", "all"}, []string{"CUDA_VISIBLE_DEVICES=0"})
	if err != nil {
		t.Fatalf("NewRunnerConfig: %v", err)
	}
	if cfg.Digest() == "" {
		t.Fatal("expected non-empty digest")
	}
	if !reflect.DeepEqual(cfg.EnvKeys(), []string{"CUDA_VISIBLE_DEVICES"}) {
		t.Fatalf("env keys = %#v", cfg.EnvKeys())
	}
	empty, err := NewRunnerConfig(nil, nil)
	if err != nil {
		t.Fatalf("empty config: %v", err)
	}
	if empty.Digest() != "" {
		t.Fatalf("empty digest = %q, want empty", empty.Digest())
	}
}
