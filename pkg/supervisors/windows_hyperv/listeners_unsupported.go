//go:build !windows

package windows_hyperv

import (
	"context"
	"fmt"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func startRuntimeListeners(ctx context.Context, handle computeSystemHandle, req vmkit.Request) (runtimeListenerSet, error) {
	if req.Config == nil || len(req.Config.VsockListeners) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("windows-hyperv listeners are only supported on windows")
}

// shellHVSockProbeHook lets tests substitute a deterministic shell probe.
var shellHVSockProbeHook = probeShellHVSock

func probeShellHVSock(ctx context.Context, state runtimeState, timeout time.Duration) (time.Duration, error) {
	return 0, fmt.Errorf("windows-hyperv shell probe is only supported on windows")
}
