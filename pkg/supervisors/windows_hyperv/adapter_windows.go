//go:build windows

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
		FrameworkAvailable:      true,
		VirtualizationSupported: true,
		ConsoleAvailable:        false,
		ConsoleMode:             "unsupported",
	}, nil
}

func (defaultAdapter) Check(ctx context.Context) error {
	return nil
}

func (defaultAdapter) Create(ctx context.Context, spec computeSystemSpec) (computeSystemHandle, error) {
	return computeSystemHandle{}, errHCSNotImplemented("create")
}

func (defaultAdapter) Start(ctx context.Context, id string) error {
	return errHCSNotImplemented("start")
}

func (defaultAdapter) Shutdown(ctx context.Context, id string) error {
	return errHCSNotImplemented("shutdown")
}

func (defaultAdapter) Kill(ctx context.Context, id string) error {
	return errHCSNotImplemented("kill")
}

func (defaultAdapter) Delete(ctx context.Context, id string) error {
	return errHCSNotImplemented("delete")
}

func errHCSNotImplemented(operation string) error {
	return fmt.Errorf("windows-hyperv HCS adapter %s is experimental and not implemented yet", operation)
}
