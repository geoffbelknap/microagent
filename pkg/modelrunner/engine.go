package modelrunner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Engine is the model-server contract: given a model file and a host:port,
// produce the argv to launch an OpenAI-compatible server and the HTTP path to
// probe for readiness.
type Engine interface {
	Name() string
	// Argv returns the full command line; element 0 is the binary path.
	Argv(modelPath, host string, port int) []string
	HealthPath() string
}

// LlamaCPP is the blessed engine: llama.cpp's llama-server.
type LlamaCPP struct {
	BinPath   string
	GPU       string
	ExtraArgs []string
}

func (l LlamaCPP) Name() string { return "llama.cpp" }

func (l LlamaCPP) Argv(modelPath, host string, port int) []string {
	argv := []string{l.BinPath, "--model", modelPath, "--host", host, "--port", strconv.Itoa(port)}
	if !llamaArgsOptIntoGPU(l.ExtraArgs) {
		switch l.GPU {
		case GPUOn, GPUAuto:
			// Deliberately leave the layer count unset. llama.cpp fits the
			// offload to free VRAM on its own, but treats any explicit value
			// -- including "all", which is -2 -- as the operator having
			// decided, and then aborts instead of splitting when the model is
			// larger than the card. That is precisely the case worth
			// offloading: an MoE like Qwen3-Coder-30B-A3B activates 3B
			// parameters per token and stays fast across a GPU/CPU split.
			// Omitting the flag reaches the GPU and never fails to load.
		default:
			argv = append(argv, "--device", "none", "--gpu-layers", "0")
		}
	}
	return append(argv, l.ExtraArgs...)
}

func (l LlamaCPP) HealthPath() string { return "/health" }

// VLLM launches vLLM's OpenAI-compatible API server. The local model path is
// intentionally ignored: vLLM loads a Hugging Face model id, while microagent's
// current model store records the local pairing ref.
type VLLM struct {
	PythonPath  string
	Model       string
	ServedModel string
	ExtraArgs   []string
}

func (v VLLM) Name() string { return "vllm" }

func (v VLLM) Argv(_ string, host string, port int) []string {
	served := strings.TrimSpace(v.ServedModel)
	if served == "" {
		served = v.Model
	}
	argv := []string{
		v.PythonPath,
		"-m", "vllm.entrypoints.openai.api_server",
		"--model", v.Model,
		"--served-model-name", served,
		"--host", host,
		"--port", strconv.Itoa(port),
	}
	return append(argv, v.ExtraArgs...)
}

func (v VLLM) HealthPath() string { return "/health" }

func (v VLLM) validate() error {
	if strings.TrimSpace(v.Model) == "" {
		return fmt.Errorf("%s is required for backend vllm", EnvModelRunnerModel)
	}
	if strings.TrimSpace(v.PythonPath) == "" {
		return fmt.Errorf("vLLM python path is required")
	}
	return nil
}

func llamaArgsOptIntoGPU(args []string) bool {
	for _, arg := range args {
		flag := arg
		if before, _, ok := strings.Cut(arg, "="); ok {
			flag = before
		}
		switch flag {
		case "-dev", "--device",
			"-ngl", "--gpu-layers", "--n-gpu-layers",
			"-sm", "--split-mode",
			"-ts", "--tensor-split",
			"-mg", "--main-gpu",
			"--gpu":
			return true
		}
	}
	return false
}

// CommandEngine runs an operator-supplied command template. The template is
// argv-shaped, not shell-expanded, and supports {model}, {host}, {port}, and
// {addr} placeholders.
type CommandEngine struct {
	RunnerName string
	Command    []string
	ExtraArgs  []string
	Health     string
}

func (c CommandEngine) Name() string {
	if name := strings.TrimSpace(c.RunnerName); name != "" {
		return name
	}
	return "custom"
}

func (c CommandEngine) Argv(modelPath, host string, port int) []string {
	portText := strconv.Itoa(port)
	addr := host + ":" + portText
	argv := make([]string, 0, len(c.Command)+len(c.ExtraArgs))
	for _, arg := range c.Command {
		arg = strings.ReplaceAll(arg, "{model}", modelPath)
		arg = strings.ReplaceAll(arg, "{host}", host)
		arg = strings.ReplaceAll(arg, "{port}", portText)
		arg = strings.ReplaceAll(arg, "{addr}", addr)
		argv = append(argv, arg)
	}
	return append(argv, c.ExtraArgs...)
}

func (c CommandEngine) HealthPath() string {
	if c.Health != "" {
		return c.Health
	}
	return "/health"
}

func ResolveEngine(config RunnerConfig) (Engine, error) {
	config, err := normalizeRunnerConfig(config)
	if err != nil {
		return nil, err
	}
	switch config.Backend {
	case BackendCustom:
		return CommandEngine{
			RunnerName: config.Name,
			Command:    config.Command,
			ExtraArgs:  config.Args,
			Health:     config.HealthPath,
		}, nil
	case BackendVLLM:
		pythonPath, err := ResolveVLLMPythonPath()
		if err != nil {
			return nil, err
		}
		engine := VLLM{
			PythonPath:  pythonPath,
			Model:       config.BackendModel,
			ServedModel: config.ServedModel,
			ExtraArgs:   config.Args,
		}
		if err := engine.validate(); err != nil {
			return nil, err
		}
		return engine, nil
	case BackendLlamaCPP:
		binPath, err := ResolveLlamaServerPath()
		if err != nil {
			return nil, err
		}
		return LlamaCPP{BinPath: binPath, GPU: config.GPU, ExtraArgs: config.Args}, nil
	default:
		return nil, fmt.Errorf("%s must be llamacpp, vllm, or custom", EnvModelRunnerBackend)
	}
}

// ResolveLlamaServerPath finds the llama-server binary: MICROAGENT_LLAMA_SERVER
// env override, then PATH, then a libexec dir next to the microagent executable.
func ResolveLlamaServerPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("MICROAGENT_LLAMA_SERVER")); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("MICROAGENT_LLAMA_SERVER is not usable: %w", err)
		}
		return p, nil
	}
	if p, err := exec.LookPath("llama-server"); err == nil {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates := []string{
			filepath.Join(dir, "..", "libexec", "llama-server"),
			filepath.Join(dir, "llama-server"),
		}
		if runtime.GOOS == "windows" {
			candidates = append(candidates,
				filepath.Join(dir, "..", "libexec", "llama-server.exe"),
				filepath.Join(dir, "llama-server.exe"),
			)
		}
		for _, c := range candidates {
			if info, statErr := os.Stat(c); statErr == nil && !info.IsDir() {
				return c, nil
			}
		}
	}
	return "", fmt.Errorf("llama-server not found; install llama.cpp or set MICROAGENT_LLAMA_SERVER")
}

// ResolveVLLMPythonPath finds the Python executable used to launch vLLM. The
// environment override is intentionally explicit because vLLM is normally
// installed in a backend-specific virtualenv.
func ResolveVLLMPythonPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("MICROAGENT_VLLM_PYTHON")); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("MICROAGENT_VLLM_PYTHON is not usable: %w", err)
		}
		return p, nil
	}
	if p, err := exec.LookPath("python3"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("vLLM python not found; set MICROAGENT_VLLM_PYTHON to a vLLM virtualenv python")
}
