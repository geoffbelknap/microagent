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

// apiClient talks to a single Firecracker instance over its API unix socket.
// Firecracker's config-file format is the union of the API request bodies, so
// the same config sub-objects (machineConfig, bootSource, drive, ...) are sent
// here unchanged.
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

func (c *apiClient) putMachineConfig(ctx context.Context, m machineConfig) error {
	return c.do(ctx, http.MethodPut, "/machine-config", m)
}

func (c *apiClient) putBootSource(ctx context.Context, b bootSource) error {
	return c.do(ctx, http.MethodPut, "/boot-source", b)
}

func (c *apiClient) putDrive(ctx context.Context, d drive) error {
	return c.do(ctx, http.MethodPut, "/drives/"+d.DriveID, d)
}

func (c *apiClient) putNetworkInterface(ctx context.Context, n networkInterface) error {
	return c.do(ctx, http.MethodPut, "/network-interfaces/"+n.IfaceID, n)
}

func (c *apiClient) putVsock(ctx context.Context, v vsockConfig) error {
	return c.do(ctx, http.MethodPut, "/vsock", v)
}

func (c *apiClient) instanceStart(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "/actions", map[string]string{"action_type": "InstanceStart"})
}

func (c *apiClient) patchVMState(ctx context.Context, state string) error {
	return c.do(ctx, http.MethodPatch, "/vm", map[string]string{"state": state})
}

func (c *apiClient) createSnapshot(ctx context.Context, snapshotPath, memFilePath string) error {
	return c.do(ctx, http.MethodPut, "/snapshot/create", map[string]string{
		"snapshot_type": "Full",
		"snapshot_path": snapshotPath,
		"mem_file_path": memFilePath,
	})
}

func (c *apiClient) loadSnapshot(ctx context.Context, snapshotPath, memFilePath string, resume bool) error {
	return c.do(ctx, http.MethodPut, "/snapshot/load", map[string]any{
		"snapshot_path": snapshotPath,
		"mem_file_path": memFilePath,
		"resume_vm":     resume,
	})
}
