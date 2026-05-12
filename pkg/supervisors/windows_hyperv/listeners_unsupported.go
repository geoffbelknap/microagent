//go:build !windows

package windows_hyperv

import (
	"context"
	"fmt"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func startRuntimeListeners(ctx context.Context, handle computeSystemHandle, req vmkit.Request) (runtimeListenerSet, error) {
	if req.Config == nil || len(req.Config.VsockListeners) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("windows-hyperv listeners are only supported on windows")
}
