//go:build linux

package firecracker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func eventResponse(req vmkit.Request, state vmkit.VMState, errorText string) vmkit.Response {
	now := time.Now().UTC()
	event := &vmkit.Event{EventID: fmt.Sprintf("event-%d", now.UnixNano()), State: state, ObservedAt: now}
	if req.Identity != nil {
		event.Identity = *req.Identity
	}
	if req.Config != nil && req.Identity != nil {
		event.Detail = "serial=" + filepath.Join(req.Config.StateDir, req.Identity.RuntimeID, "serial.log")
	}
	resp := vmkit.Response{OK: state != vmkit.StateFailed, Backend: vmkit.BackendLinuxKVM, Event: event}
	if errorText != "" {
		resp.Error = errorText
	}
	return resp
}

func failedResponse(req vmkit.Request, errorText string) vmkit.Response {
	return eventResponse(req, vmkit.StateFailed, errorText)
}

func configPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "firecracker.json")
}

// apiSocketPath is the Firecracker API unix socket. It is deterministic from
// the workspace identity, so pause/resume/snapshot reach it without recording
// it in runtime state. The VM boots from the config file; the API socket is
// additionally exposed (only --no-api would disable it) for runtime control.
func apiSocketPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "firecracker-api.sock")
}

func serialLogPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "serial.log")
}

func serialInputPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "serial.in")
}

func vsockSocketPath(opts Options) string {
	return filepath.Join(opts.StateDir, opts.Name, "vsock.sock")
}

func firecrackerGuestVsockPath(opts Options, port uint32) string {
	return fmt.Sprintf("%s_%d", vsockSocketPath(opts), port)
}

func cleanupWorkspaceState(opts Options) {
	if state, err := readRuntimeState(opts); err == nil {
		cleanupTransientFirewallRules(state.FirewallRules)
		cleanupTransientNetworkDevices(state.NetworkDevices)
	}
	_ = os.RemoveAll(filepath.Join(opts.StateDir, "workspaces", opts.Name))
	_ = os.RemoveAll(filepath.Join(opts.StateDir, opts.Name))
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomically(path, data, 0o600)
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
