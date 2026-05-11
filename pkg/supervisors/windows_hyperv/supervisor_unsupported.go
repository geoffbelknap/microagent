//go:build !windows

package windows_hyperv

import (
	"context"
	"fmt"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func (s Supervisor) Do(ctx context.Context, req vmkit.Request) (vmkit.Response, error) {
	if err := vmkit.ValidateRequest(req); err != nil {
		return vmkit.Response{}, err
	}
	resp := hostResponse(ctx, s.runtimeAdapter())
	err := fmt.Errorf("%s", resp.Error)
	return resp, err
}

func HostResponse() vmkit.Response {
	return hostResponse(context.Background(), defaultAdapter{})
}

func hostResponse(ctx context.Context, adapter runtimeAdapter) vmkit.Response {
	host, err := adapter.Host(ctx)
	resp := vmkit.Response{
		OK:      err == nil,
		Backend: vmkit.BackendWindowsHyperV,
		Host:    &host,
		Kernel: &vmkit.KernelSupport{
			Backend:      vmkit.BackendWindowsHyperV,
			Architecture: host.Architecture,
			Status:       "unknown",
		},
	}
	if err != nil {
		resp.Error = err.Error()
	}
	return resp
}
