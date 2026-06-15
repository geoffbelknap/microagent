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

type RunnerConfig struct {
	Command    []string
	Name       string
	HealthPath string
	Args       []string
	Env        []string
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
	name := strings.TrimSpace(c.Name)
	healthPath := strings.TrimSpace(c.HealthPath)
	if len(cleanCommand) == 0 {
		if name != "" {
			return RunnerConfig{}, fmt.Errorf("%s requires %s", EnvModelRunnerName, EnvModelRunnerCommand)
		}
		if healthPath != "" {
			return RunnerConfig{}, fmt.Errorf("%s requires %s", EnvModelRunnerHealthPath, EnvModelRunnerCommand)
		}
	} else {
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
		Command:    cleanCommand,
		Name:       name,
		HealthPath: healthPath,
		Args:       cleanArgs,
		Env:        cleanEnv,
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
		Command:    command,
		Name:       os.Getenv(EnvModelRunnerName),
		HealthPath: os.Getenv(EnvModelRunnerHealthPath),
		Args:       args,
		Env:        env,
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
	if len(c.Command) == 0 && c.Name == "" && c.HealthPath == "" && len(c.Args) == 0 && len(c.Env) == 0 {
		return ""
	}
	data, _ := json.Marshal(struct {
		Command    []string `json:"command,omitempty"`
		Name       string   `json:"name,omitempty"`
		HealthPath string   `json:"health_path,omitempty"`
		Args       []string `json:"args,omitempty"`
		Env        []string `json:"env,omitempty"`
	}{
		Command:    c.Command,
		Name:       c.Name,
		HealthPath: c.HealthPath,
		Args:       c.Args,
		Env:        c.Env,
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
