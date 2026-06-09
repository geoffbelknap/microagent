package modelrunner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Engine is the model-server contract: given a model file and a host:port,
// produce the argv to launch an OpenAI-compatible server and the HTTP path to
// probe for readiness. llama.cpp is the blessed implementation; a second engine
// (e.g. vLLM) can satisfy this contract later without changing callers.
type Engine interface {
	Name() string
	// Argv returns the full command line; element 0 is the binary path.
	Argv(modelPath, host string, port int) []string
	HealthPath() string
}

// LlamaCPP is the blessed engine: llama.cpp's llama-server.
type LlamaCPP struct {
	BinPath string
}

func (l LlamaCPP) Name() string { return "llama.cpp" }

func (l LlamaCPP) Argv(modelPath, host string, port int) []string {
	return []string{l.BinPath, "--model", modelPath, "--host", host, "--port", strconv.Itoa(port)}
}

func (l LlamaCPP) HealthPath() string { return "/health" }

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
		for _, c := range []string{
			filepath.Join(dir, "..", "libexec", "llama-server"),
			filepath.Join(dir, "llama-server"),
		} {
			if info, statErr := os.Stat(c); statErr == nil && !info.IsDir() {
				return c, nil
			}
		}
	}
	return "", fmt.Errorf("llama-server not found; install llama.cpp or set MICROAGENT_LLAMA_SERVER")
}
