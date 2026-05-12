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
	switch req.Command {
	case "host":
		resp := hostResponse(ctx, s.runtimeAdapter())
		if resp.Error != "" {
			return resp, fmt.Errorf("%s", resp.Error)
		}
		return resp, nil
	case "check":
		if err := s.runtimeAdapter().Check(ctx); err != nil {
			return vmkit.Response{OK: false, Backend: vmkit.BackendWindowsHyperV, Error: err.Error()}, err
		}
		return vmkit.Response{OK: true, Backend: vmkit.BackendWindowsHyperV}, nil
	default:
		resp := hostResponse(ctx, s.runtimeAdapter())
		return resp, fmt.Errorf("%s", resp.Error)
	}
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
