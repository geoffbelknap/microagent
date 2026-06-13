//go:build !windows

package windows_hyperv

import (
	"context"
	"fmt"
	"runtime"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

type defaultAdapter struct{}

func (defaultAdapter) Host(ctx context.Context) (vmkit.HostSupport, error) {
	return vmkit.HostSupport{
		Backend:                 vmkit.BackendWindowsHyperV,
		Architecture:            runtime.GOARCH,
		FrameworkAvailable:      false,
		VirtualizationSupported: false,
		ConsoleAvailable:        false,
		ConsoleMode:             "unsupported",
	}, errUnsupportedHost()
}

func (defaultAdapter) Check(ctx context.Context) error {
	return errUnsupportedHost()
}

func (defaultAdapter) PrepareNetwork(ctx context.Context, spec computeSystemSpec) (networkAttachment, error) {
	return networkAttachment{}, nil
}

func (defaultAdapter) CleanupNetwork(ctx context.Context, state runtimeState) error {
	return nil
}

func ProbeHCSAccess(ctx context.Context) error {
	return errUnsupportedHost()
}

func ProbeHCNAccess(ctx context.Context) error {
	return errUnsupportedHost()
}

func ProbeHvSocketAccess(ctx context.Context) error {
	return errUnsupportedHost()
}

func (defaultAdapter) Create(ctx context.Context, spec computeSystemSpec) (computeSystemHandle, error) {
	return computeSystemHandle{}, errUnsupportedHost()
}

func (defaultAdapter) Start(ctx context.Context, id string) error {
	return errUnsupportedHost()
}

func (defaultAdapter) Shutdown(ctx context.Context, id string) error {
	return errUnsupportedHost()
}

func (defaultAdapter) Pause(ctx context.Context, id string) error {
	return errUnsupportedHost()
}

func (defaultAdapter) Resume(ctx context.Context, id string) error {
	return errUnsupportedHost()
}

func (defaultAdapter) Kill(ctx context.Context, id string) error {
	return errUnsupportedHost()
}

func (defaultAdapter) Delete(ctx context.Context, id string) error {
	return errUnsupportedHost()
}

func (defaultAdapter) Wait(ctx context.Context, id string) error {
	return errUnsupportedHost()
}

func (defaultAdapter) Exists(ctx context.Context, id string) (bool, error) {
	return false, errUnsupportedHost()
}

func errUnsupportedHost() error {
	return fmt.Errorf("windows-hyperv supervisor is only supported on windows")
}
