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
	ExtraArgs []string
}

func (l LlamaCPP) Name() string { return "llama.cpp" }

func (l LlamaCPP) Argv(modelPath, host string, port int) []string {
	argv := []string{l.BinPath, "--model", modelPath, "--host", host, "--port", strconv.Itoa(port)}
	if !llamaArgsOptIntoGPU(l.ExtraArgs) {
		argv = append(argv, "--device", "none", "--gpu-layers", "0")
	}
	return append(argv, l.ExtraArgs...)
}

func (l LlamaCPP) HealthPath() string { return "/health" }

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
	if len(config.Command) != 0 {
		return CommandEngine{
			RunnerName: config.Name,
			Command:    config.Command,
			ExtraArgs:  config.Args,
			Health:     config.HealthPath,
		}, nil
	}
	binPath, err := ResolveLlamaServerPath()
	if err != nil {
		return nil, err
	}
	return LlamaCPP{BinPath: binPath, ExtraArgs: config.Args}, nil
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
