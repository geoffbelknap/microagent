package modelrunner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
)

const (
	// EnvModelRunnerBackend selects the built-in runner backend. Supported
	// values are llamacpp, vllm, and custom. Empty keeps the default llamacpp
	// runner unless a custom command is supplied.
	EnvModelRunnerBackend = "MICROAGENT_MODEL_RUNNER_BACKEND"
	// EnvModelRunnerGPU selects runner GPU intent: off, on, or auto.
	EnvModelRunnerGPU = "MICROAGENT_MODEL_RUNNER_GPU"
	// EnvModelRunnerModel supplies the backend model id for runners such as
	// vLLM that do not serve the local GGUF path directly.
	EnvModelRunnerModel = "MICROAGENT_MODEL_RUNNER_MODEL"
	// EnvModelRunnerServedModel supplies the OpenAI-compatible served model
	// name for runners that distinguish backend model id from request model id.
	EnvModelRunnerServedModel = "MICROAGENT_MODEL_RUNNER_SERVED_MODEL"
	// EnvModelRunnerCommand overrides the built-in runner command. It accepts
	// shell-like fields or a JSON array of strings. The command must include a
	// {model} placeholder and either {port} or {addr}; {host} is also available.
	EnvModelRunnerCommand = "MICROAGENT_MODEL_RUNNER_COMMAND"
	// EnvModelRunnerName labels a custom runner command in state output.
	EnvModelRunnerName = "MICROAGENT_MODEL_RUNNER_NAME"
	// EnvModelRunnerHealthPath overrides the custom runner readiness path.
	EnvModelRunnerHealthPath = "MICROAGENT_MODEL_RUNNER_HEALTH_PATH"
	// EnvModelRunnerArgs is a host-side default for extra argv entries appended
	// to the selected model runner. It accepts either shell-like fields or a JSON
	// array of strings.
	EnvModelRunnerArgs = "MICROAGENT_MODEL_RUNNER_ARGS"
	// EnvModelRunnerEnv is a host-side default for model runner environment
	// overrides. It accepts shell-like KEY=VALUE fields, a JSON array of
	// KEY=VALUE strings, or a JSON object.
	EnvModelRunnerEnv = "MICROAGENT_MODEL_RUNNER_ENV"
)

const (
	BackendLlamaCPP = "llamacpp"
	BackendVLLM     = "vllm"
	BackendCustom   = "custom"

	GPUOff  = "off"
	GPUOn   = "on"
	GPUAuto = "auto"
)

type RunnerConfig struct {
	Backend      string
	GPU          string
	BackendModel string
	ServedModel  string
	Command      []string
	Name         string
	HealthPath   string
	Args         []string
	Env          []string
}

func NewRunnerConfig(args, env []string) (RunnerConfig, error) {
	return normalizeRunnerConfig(RunnerConfig{Args: args, Env: env})
}

func (c RunnerConfig) WithCommand(command []string, name, healthPath string) (RunnerConfig, error) {
	c.Command = command
	c.Name = name
	c.HealthPath = healthPath
	return normalizeRunnerConfig(c)
}

func normalizeRunnerConfig(c RunnerConfig) (RunnerConfig, error) {
	cleanCommand, err := normalizeRunnerCommand(c.Command)
	if err != nil {
		return RunnerConfig{}, err
	}
	backend, err := normalizeRunnerBackend(c.Backend, len(cleanCommand) != 0)
	if err != nil {
		return RunnerConfig{}, err
	}
	gpu, err := normalizeRunnerGPU(c.GPU, backend)
	if err != nil {
		return RunnerConfig{}, err
	}
	backendModel := strings.TrimSpace(c.BackendModel)
	servedModel := strings.TrimSpace(c.ServedModel)
	name := strings.TrimSpace(c.Name)
	healthPath := strings.TrimSpace(c.HealthPath)
	if backend == BackendCustom {
		if len(cleanCommand) == 0 {
			return RunnerConfig{}, fmt.Errorf("custom model runner requires %s", EnvModelRunnerCommand)
		}
	}
	if len(cleanCommand) == 0 {
		if name != "" {
			return RunnerConfig{}, fmt.Errorf("%s requires %s", EnvModelRunnerName, EnvModelRunnerCommand)
		}
		if healthPath != "" {
			return RunnerConfig{}, fmt.Errorf("%s requires %s", EnvModelRunnerHealthPath, EnvModelRunnerCommand)
		}
	} else {
		if backend != BackendCustom {
			return RunnerConfig{}, fmt.Errorf("model runner command requires backend custom")
		}
		if !commandTemplateContains(cleanCommand, "{model}") {
			return RunnerConfig{}, fmt.Errorf("model runner command must include {model}")
		}
		if !commandTemplateContains(cleanCommand, "{port}") && !commandTemplateContains(cleanCommand, "{addr}") {
			return RunnerConfig{}, fmt.Errorf("model runner command must include {port} or {addr}")
		}
		if name == "" {
			name = "custom"
		}
		if healthPath == "" {
			healthPath = "/health"
		}
		if !strings.HasPrefix(healthPath, "/") {
			return RunnerConfig{}, fmt.Errorf("model runner health path must start with /")
		}
	}
	if backend == BackendVLLM {
		if backendModel == "" {
			return RunnerConfig{}, fmt.Errorf("%s requires %s for backend vllm", EnvModelRunnerBackend, EnvModelRunnerModel)
		}
		if servedModel == "" {
			servedModel = backendModel
		}
	} else if backendModel != "" || servedModel != "" {
		return RunnerConfig{}, fmt.Errorf("%s and %s are only supported for backend vllm", EnvModelRunnerModel, EnvModelRunnerServedModel)
	}
	cleanArgs := make([]string, 0, len(c.Args))
	for _, arg := range c.Args {
		if strings.ContainsRune(arg, 0) {
			return RunnerConfig{}, fmt.Errorf("model runner argument contains NUL")
		}
		cleanArgs = append(cleanArgs, arg)
	}
	cleanEnv, err := NormalizeRunnerEnv(c.Env)
	if err != nil {
		return RunnerConfig{}, err
	}
	return RunnerConfig{
		Backend:      backend,
		GPU:          gpu,
		BackendModel: backendModel,
		ServedModel:  servedModel,
		Command:      cleanCommand,
		Name:         name,
		HealthPath:   healthPath,
		Args:         cleanArgs,
		Env:          cleanEnv,
	}, nil
}

func RunnerConfigFromEnv() (RunnerConfig, error) {
	command, err := ParseRunnerCommand(os.Getenv(EnvModelRunnerCommand))
	if err != nil {
		return RunnerConfig{}, fmt.Errorf("%s: %w", EnvModelRunnerCommand, err)
	}
	args, err := ParseRunnerArgs(os.Getenv(EnvModelRunnerArgs))
	if err != nil {
		return RunnerConfig{}, fmt.Errorf("%s: %w", EnvModelRunnerArgs, err)
	}
	env, err := ParseRunnerEnv(os.Getenv(EnvModelRunnerEnv))
	if err != nil {
		return RunnerConfig{}, fmt.Errorf("%s: %w", EnvModelRunnerEnv, err)
	}
	return normalizeRunnerConfig(RunnerConfig{
		Backend:      os.Getenv(EnvModelRunnerBackend),
		GPU:          os.Getenv(EnvModelRunnerGPU),
		BackendModel: os.Getenv(EnvModelRunnerModel),
		ServedModel:  os.Getenv(EnvModelRunnerServedModel),
		Command:      command,
		Name:         os.Getenv(EnvModelRunnerName),
		HealthPath:   os.Getenv(EnvModelRunnerHealthPath),
		Args:         args,
		Env:          env,
	})
}

func (c RunnerConfig) WithAdditional(args, env []string) (RunnerConfig, error) {
	mergedArgs := append(append([]string{}, c.Args...), args...)
	mergedEnv := append(append([]string{}, c.Env...), env...)
	c.Args = mergedArgs
	c.Env = mergedEnv
	return normalizeRunnerConfig(c)
}

func (c RunnerConfig) Digest() string {
	backend := c.Backend
	if backend == BackendLlamaCPP {
		backend = ""
	}
	gpu := c.GPU
	if gpu == GPUOff {
		gpu = ""
	}
	if backend == "" && gpu == "" && c.BackendModel == "" && c.ServedModel == "" && len(c.Command) == 0 && c.Name == "" && c.HealthPath == "" && len(c.Args) == 0 && len(c.Env) == 0 {
		return ""
	}
	data, _ := json.Marshal(struct {
		Backend      string   `json:"backend,omitempty"`
		GPU          string   `json:"gpu,omitempty"`
		BackendModel string   `json:"backend_model,omitempty"`
		ServedModel  string   `json:"served_model,omitempty"`
		Command      []string `json:"command,omitempty"`
		Name         string   `json:"name,omitempty"`
		HealthPath   string   `json:"health_path,omitempty"`
		Args         []string `json:"args,omitempty"`
		Env          []string `json:"env,omitempty"`
	}{
		Backend:      backend,
		GPU:          gpu,
		BackendModel: c.BackendModel,
		ServedModel:  c.ServedModel,
		Command:      c.Command,
		Name:         c.Name,
		HealthPath:   c.HealthPath,
		Args:         c.Args,
		Env:          c.Env,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

func (c RunnerConfig) EnvKeys() []string {
	keys := make([]string, 0, len(c.Env))
	for _, entry := range c.Env {
		key, _, _ := strings.Cut(entry, "=")
		keys = append(keys, key)
	}
	return keys
}

func ParseRunnerCommand(raw string) ([]string, error) {
	return ParseRunnerArgs(raw)
}

func ParseRunnerArgs(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return nil, err
		}
		return args, nil
	}
	return splitRunnerFields(raw)
}

func ParseRunnerEnv(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "{") {
		var values map[string]string
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		env := make([]string, 0, len(keys))
		for _, key := range keys {
			env = append(env, key+"="+values[key])
		}
		return NormalizeRunnerEnv(env)
	}
	if strings.HasPrefix(raw, "[") {
		var env []string
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			return nil, err
		}
		return NormalizeRunnerEnv(env)
	}
	fields, err := splitRunnerFields(raw)
	if err != nil {
		return nil, err
	}
	return NormalizeRunnerEnv(fields)
}

func NormalizeRunnerEnv(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	byKey := map[string]string{}
	for _, raw := range values {
		if strings.ContainsRune(raw, 0) {
			return nil, fmt.Errorf("model runner environment entry contains NUL")
		}
		key, value, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("model runner environment entry must be KEY=VALUE")
		}
		if strings.ContainsAny(key, "=\x00") {
			return nil, fmt.Errorf("invalid model runner environment key %q", key)
		}
		byKey[key] = value
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+byKey[key])
	}
	return out, nil
}

func normalizeRunnerCommand(command []string) ([]string, error) {
	if len(command) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(command))
	for _, arg := range command {
		if strings.ContainsRune(arg, 0) {
			return nil, fmt.Errorf("model runner command contains NUL")
		}
		out = append(out, arg)
	}
	if strings.TrimSpace(out[0]) == "" {
		return nil, fmt.Errorf("model runner command binary is empty")
	}
	return out, nil
}

func normalizeRunnerBackend(raw string, hasCommand bool) (string, error) {
	backend := strings.ToLower(strings.TrimSpace(raw))
	if backend == "" {
		if hasCommand {
			return BackendCustom, nil
		}
		return BackendLlamaCPP, nil
	}
	switch backend {
	case "llama", "llama.cpp", "llama-cpp", BackendLlamaCPP:
		return BackendLlamaCPP, nil
	case BackendVLLM:
		return BackendVLLM, nil
	case BackendCustom:
		return BackendCustom, nil
	default:
		return "", fmt.Errorf("%s must be llamacpp, vllm, or custom", EnvModelRunnerBackend)
	}
}

func normalizeRunnerGPU(raw, backend string) (string, error) {
	gpu := strings.ToLower(strings.TrimSpace(raw))
	if gpu == "" {
		if backend == BackendVLLM {
			return GPUOn, nil
		}
		return GPUOff, nil
	}
	switch gpu {
	case "0", "false", "disabled", "none", "cpu", GPUOff:
		gpu = GPUOff
	case "1", "true", "gpu", GPUOn:
		gpu = GPUOn
	case GPUAuto:
		gpu = GPUAuto
	default:
		return "", fmt.Errorf("%s must be off, on, or auto", EnvModelRunnerGPU)
	}
	if backend == BackendVLLM && gpu == GPUOff {
		return "", fmt.Errorf("backend vllm requires %s=on or auto", EnvModelRunnerGPU)
	}
	return gpu, nil
}

func commandTemplateContains(command []string, placeholder string) bool {
	for _, arg := range command {
		if strings.Contains(arg, placeholder) {
			return true
		}
	}
	return false
}

func splitRunnerFields(raw string) ([]string, error) {
	var out []string
	var b strings.Builder
	var quote rune
	escaped := false
	inToken := false
	for _, r := range raw {
		if escaped {
			b.WriteRune(r)
			inToken = true
			escaped = false
			continue
		}
		if quote != 0 {
			switch {
			case r == quote:
				quote = 0
			case r == '\\' && quote == '"':
				escaped = true
			default:
				b.WriteRune(r)
			}
			inToken = true
			continue
		}
		switch {
		case r == '\\':
			escaped = true
			inToken = true
		case r == '\'' || r == '"':
			quote = r
			inToken = true
		case unicode.IsSpace(r):
			if inToken {
				out = append(out, b.String())
				b.Reset()
				inToken = false
			}
		default:
			b.WriteRune(r)
			inToken = true
		}
	}
	if escaped {
		return nil, fmt.Errorf("unterminated escape")
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	if inToken {
		out = append(out, b.String())
	}
	return out, nil
}
