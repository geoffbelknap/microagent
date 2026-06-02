package secret

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// EnvProvider reads a secret from one of the CLI process's own environment
// variables. The reference is the variable name, e.g. "env:API_KEY".
type EnvProvider struct {
	// Getenv looks up an environment variable; defaults to os.Getenv.
	Getenv func(string) string
}

func (p *EnvProvider) Resolve(_ context.Context, name string) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("env reference is missing a variable name")
	}
	get := p.Getenv
	if get == nil {
		get = os.Getenv
	}
	return []byte(get(name)), nil
}

func (p *EnvProvider) Plaintext() bool { return true }

// FileProvider reads a secret from a file's raw contents. The reference is the
// path, e.g. "file:/run/keys/api". Contents are returned verbatim (no trimming)
// so binary secrets and intentional trailing newlines survive intact.
type FileProvider struct{}

func (p *FileProvider) Resolve(_ context.Context, path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("file reference is missing a path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read secret file: %w", err)
	}
	return data, nil
}

func (p *FileProvider) Plaintext() bool { return true }

// DotenvProvider reads a single KEY from a dotenv file. The reference is
// PATH#KEY, e.g. "dotenv:/etc/app.env#API_KEY".
type DotenvProvider struct{}

func (p *DotenvProvider) Resolve(_ context.Context, rest string) ([]byte, error) {
	path, key, ok := strings.Cut(rest, "#")
	if !ok || path == "" || key == "" {
		return nil, fmt.Errorf("dotenv reference %q must be PATH#KEY", rest)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dotenv file: %w", err)
	}
	values, err := parseDotenv(data)
	if err != nil {
		return nil, err
	}
	value, ok := values[key]
	if !ok {
		return nil, fmt.Errorf("key %q not found in dotenv file %q", key, path)
	}
	return []byte(value), nil
}

func (p *DotenvProvider) Plaintext() bool { return true }
