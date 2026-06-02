package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

// apiClient performs runtime control on a running Firecracker instance over its
// API unix socket. The VM is booted from the config file (--config-file); the
// API socket is additionally exposed (only --no-api would disable it) so the
// supervisor can pause/resume and snapshot the running VM.
type apiClient struct {
	http *http.Client
}

func newAPIClient(socketPath string) *apiClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	return &apiClient{http: &http.Client{Transport: transport}}
}

func (c *apiClient) do(ctx context.Context, method, path string, body any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	// The host is ignored by the unix DialContext; any value keeps net/http happy.
	req, err := http.NewRequestWithContext(ctx, method, "http://firecracker"+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("firecracker api %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("firecracker api %s %s: status %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}

// patchVMState pauses ("Paused") or resumes ("Resumed") the running VM.
func (c *apiClient) patchVMState(ctx context.Context, state string) error {
	return c.do(ctx, http.MethodPatch, "/vm", map[string]string{"state": state})
}

// createSnapshot writes a full snapshot. The VM must be paused first.
func (c *apiClient) createSnapshot(ctx context.Context, snapshotPath, memFilePath string) error {
	return c.do(ctx, http.MethodPut, "/snapshot/create", map[string]string{
		"snapshot_type": "Full",
		"snapshot_path": snapshotPath,
		"mem_file_path": memFilePath,
	})
}

// loadSnapshot loads a snapshot into a freshly launched Firecracker process.
// networkOverride remaps a restored network interface to a host tap that exists
// in the loading process's network namespace. A fork's tap name is derived from
// its own (different) workspace name, so the snapshot's baked tap must be
// remapped on load or the restored guest has no network backend.
type networkOverride struct {
	IfaceID     string `json:"iface_id"`
	HostDevName string `json:"host_dev_name"`
}

func (c *apiClient) loadSnapshot(ctx context.Context, snapshotPath, memFilePath string, resume bool, networkOverrides []networkOverride) error {
	body := map[string]any{
		"snapshot_path": snapshotPath,
		"mem_file_path": memFilePath,
		"resume_vm":     resume,
	}
	if len(networkOverrides) > 0 {
		body["network_overrides"] = networkOverrides
	}
	return c.do(ctx, http.MethodPut, "/snapshot/load", body)
}
