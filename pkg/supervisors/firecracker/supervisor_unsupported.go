//go:build !linux

package firecracker

import (
	"context"
	"fmt"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

type Options struct {
	Name               string
	StateDir           string
	Timeout            time.Duration
	FirecrackerPath    string
	ResolveFirecracker func() (string, error)
}

type Supervisor struct {
	Options Options
}

func (s Supervisor) Do(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	err := fmt.Errorf("firecracker supervisor is only supported on linux")
	return vmkit.Response{OK: false, Backend: vmkit.BackendFirecracker, Error: err.Error()}, err
}

func ResolveBinary() (string, error) {
	return "", fmt.Errorf("firecracker supervisor is only supported on linux")
}

func GuestHalted(serialPath string) bool {
	return false
}
