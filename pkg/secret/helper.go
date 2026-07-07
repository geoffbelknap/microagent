package secret

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// HelperProvider resolves a reference by executing an operator-owned helper
// binary — the credential-helper pattern (git, docker): microagent stays
// backend-neutral while the embedding platform supplies its cloud's resolver
// (e.g. a cloud secret manager reached via instance identity, no tokens).
//
// The helper command comes from the MICROAGENT_SECRET_HELPER environment
// variable of the resolving process. The reference remainder is passed as
// the single argument, the secret is the helper's stdout (a single trailing
// newline is trimmed), and any nonzero exit fails the resolve closed —
// stderr is surfaced in the error, stdout is never logged.
type HelperProvider struct {
	// Command is the helper binary path. Empty means the scheme is not
	// configured on this host and every resolve fails closed.
	Command string
	// Timeout bounds one resolve; zero means 30s.
	Timeout time.Duration
}

func (p *HelperProvider) Plaintext() bool { return false }

func (p *HelperProvider) Resolve(ctx context.Context, rest string) ([]byte, error) {
	if strings.TrimSpace(rest) == "" {
		return nil, fmt.Errorf("helper reference is empty")
	}
	if p.Command == "" {
		return nil, fmt.Errorf("helper: MICROAGENT_SECRET_HELPER is not set on this host")
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, p.Command, rest)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("helper %s: resolve %q failed: %s", p.Command, rest, detail)
	}
	value := stdout.Bytes()
	value = bytes.TrimSuffix(value, []byte("\n"))
	if len(value) == 0 {
		return nil, fmt.Errorf("helper %s: resolve %q returned an empty secret", p.Command, rest)
	}
	return value, nil
}
