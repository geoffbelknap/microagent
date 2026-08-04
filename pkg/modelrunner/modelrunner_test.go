package modelrunner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLlamaCPPEngine(t *testing.T) {
	e := LlamaCPP{BinPath: "/usr/bin/llama-server", ExtraArgs: []string{"-ngl", "all"}}
	if e.Name() != "llama.cpp" || e.HealthPath() != "/health" {
		t.Fatalf("unexpected engine metadata: %s %s", e.Name(), e.HealthPath())
	}
	argv := e.Argv("/models/m.gguf", "127.0.0.1", 9999)
	want := []string{"/usr/bin/llama-server", "--model", "/models/m.gguf", "--host", "127.0.0.1", "--port", "9999", "--ctx-size", "32768", "-ngl", "all"}
	if len(argv) != len(want) {
		t.Fatalf("argv len: got %v want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d]: got %q want %q", i, argv[i], want[i])
		}
	}
}

func TestLlamaCPPEngineDefaultsToCPU(t *testing.T) {
	e := LlamaCPP{BinPath: "/usr/bin/llama-server"}
	got := e.Argv("/models/m.gguf", "127.0.0.1", 9999)
	want := []string{"/usr/bin/llama-server", "--model", "/models/m.gguf", "--host", "127.0.0.1", "--port", "9999", "--device", "none", "--gpu-layers", "0", "--ctx-size", "32768"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestLlamaCPPEngineGPUOptInSkipsCPUDefault(t *testing.T) {
	e := LlamaCPP{BinPath: "/usr/bin/llama-server", ExtraArgs: []string{"--gpu-layers=all"}}
	got := e.Argv("/models/m.gguf", "127.0.0.1", 9999)
	want := []string{"/usr/bin/llama-server", "--model", "/models/m.gguf", "--host", "127.0.0.1", "--port", "9999", "--ctx-size", "32768", "--gpu-layers=all"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

// Opting into the GPU must not pin a layer count: llama.cpp only fits the
// offload to free VRAM while n_gpu_layers is unset, and aborts rather than
// splitting once a value is supplied. A model larger than the card is exactly
// what needs the split, so pinning turns "use the GPU" into "fail to load".
func TestLlamaCPPEngineNamedGPUOptInLeavesLayersUnset(t *testing.T) {
	for _, mode := range []string{GPUOn, GPUAuto} {
		e := LlamaCPP{BinPath: "/usr/bin/llama-server", GPU: mode}
		got := e.Argv("/models/m.gguf", "127.0.0.1", 9999)
		want := []string{"/usr/bin/llama-server", "--model", "/models/m.gguf", "--host", "127.0.0.1", "--port", "9999", "--ctx-size", "32768"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("GPU=%s argv = %#v, want %#v", mode, got, want)
		}
	}
}

// Off still has to keep the model off the GPU entirely.
func TestLlamaCPPEngineGPUOffPinsZero(t *testing.T) {
	e := LlamaCPP{BinPath: "/usr/bin/llama-server", GPU: GPUOff}
	got := e.Argv("/models/m.gguf", "127.0.0.1", 9999)
	want := []string{"/usr/bin/llama-server", "--model", "/models/m.gguf", "--host", "127.0.0.1", "--port", "9999", "--device", "none", "--gpu-layers", "0", "--ctx-size", "32768"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestVLLMEngine(t *testing.T) {
	e := VLLM{
		PythonPath:  "/venv/bin/python",
		Model:       "Qwen/Qwen2.5-0.5B-Instruct",
		ServedModel: "local-chat",
		ExtraArgs:   []string{"--max-model-len", "2048"},
	}
	if e.Name() != "vllm" || e.HealthPath() != "/health" {
		t.Fatalf("unexpected engine metadata: %s %s", e.Name(), e.HealthPath())
	}
	got := e.Argv("/ignored/local.gguf", "127.0.0.1", 9999)
	want := []string{"/venv/bin/python", "-m", "vllm.entrypoints.openai.api_server", "--model", "Qwen/Qwen2.5-0.5B-Instruct", "--served-model-name", "local-chat", "--host", "127.0.0.1", "--port", "9999", "--max-model-len", "2048"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestCommandEngine(t *testing.T) {
	e := CommandEngine{
		RunnerName: "runner-x",
		Command:    []string{"runner", "serve", "{model}", "--host", "{host}", "--port", "{port}", "--addr", "{addr}"},
		ExtraArgs:  []string{"--gpu", "auto"},
		Health:     "/ready",
	}
	if e.Name() != "runner-x" || e.HealthPath() != "/ready" {
		t.Fatalf("unexpected engine metadata: %s %s", e.Name(), e.HealthPath())
	}
	got := e.Argv("/models/m.gguf", "127.0.0.1", 9999)
	want := []string{"runner", "serve", "/models/m.gguf", "--host", "127.0.0.1", "--port", "9999", "--addr", "127.0.0.1:9999", "--gpu", "auto"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestResolveLlamaServerPathEnvOverride(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "llama-server")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MICROAGENT_LLAMA_SERVER", bin)
	got, err := ResolveLlamaServerPath()
	if err != nil || got != bin {
		t.Fatalf("resolve: got %q err=%v", got, err)
	}
	t.Setenv("MICROAGENT_LLAMA_SERVER", filepath.Join(dir, "nonexistent"))
	if _, err := ResolveLlamaServerPath(); err == nil {
		t.Fatal("expected error for unusable override")
	}
}

func TestAllocateFreePort(t *testing.T) {
	p, err := allocateFreePort()
	if err != nil || p <= 0 {
		t.Fatalf("allocateFreePort: %d err=%v", p, err)
	}
}

func TestWaitHealthySucceedsAgainstServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	// Point waitHealthy at the test server via probeHealth override that ignores host/port.
	prev := probeHealth
	probeHealth = func(ctx context.Context, _ string, timeout time.Duration) error {
		return prev(ctx, srv.URL+"/health", timeout)
	}
	t.Cleanup(func() { probeHealth = prev })
	if err := waitHealthy(context.Background(), "127.0.0.1", 1234, "/health", 2*time.Second); err != nil {
		t.Fatalf("waitHealthy: %v", err)
	}
}

func TestWaitHealthyTimesOut(t *testing.T) {
	prev := probeHealth
	probeHealth = func(ctx context.Context, _ string, _ time.Duration) error {
		return context.DeadlineExceeded
	}
	t.Cleanup(func() { probeHealth = prev })
	err := waitHealthy(context.Background(), "127.0.0.1", 1234, "/health", 400*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestSpawnAndStopRealProcess(t *testing.T) {
	// Spawn a real, harmless long-lived process to exercise spawn/alive/stop.
	logPath := filepath.Join(t.TempDir(), "proc.log")
	pid, err := spawnProcess(longRunningArgv(), nil, logPath)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !processAlive(pid) {
		t.Fatalf("process %d should be alive", pid)
	}
	if err := stopProcess(pid); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// Give it a moment to die.
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("process %d still alive after stop", pid)
	}
}
