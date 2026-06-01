package firecracker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func startFakeFirecracker(t *testing.T, handler http.Handler) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "fc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return sock
}

func TestAPIClientEndpoints(t *testing.T) {
	type call struct {
		method, path, body string
	}
	var got []call
	sock := startFakeFirecracker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, call{r.Method, r.URL.Path, string(b)})
		w.WriteHeader(http.StatusNoContent)
	}))
	c := newAPIClient(sock)
	ctx := context.Background()
	mustNoErr(t, c.putMachineConfig(ctx, machineConfig{VCPUCount: 2, MemSizeMiB: 512}))
	mustNoErr(t, c.putBootSource(ctx, bootSource{KernelImagePath: "/k", BootArgs: "console=ttyS0"}))
	mustNoErr(t, c.putDrive(ctx, drive{DriveID: "rootfs", PathOnHost: "/r", IsRootDevice: true}))
	mustNoErr(t, c.putNetworkInterface(ctx, networkInterface{IfaceID: "eth0", HostDevName: "tap0"}))
	mustNoErr(t, c.putVsock(ctx, vsockConfig{VsockID: "vsock0", GuestCID: 3, UDSPath: "/v.sock"}))
	mustNoErr(t, c.instanceStart(ctx))
	mustNoErr(t, c.patchVMState(ctx, "Paused"))
	mustNoErr(t, c.createSnapshot(ctx, "/s/vmstate", "/s/mem"))
	mustNoErr(t, c.loadSnapshot(ctx, "/s/vmstate", "/s/mem", true))

	want := []struct{ method, path string }{
		{"PUT", "/machine-config"},
		{"PUT", "/boot-source"},
		{"PUT", "/drives/rootfs"},
		{"PUT", "/network-interfaces/eth0"},
		{"PUT", "/vsock"},
		{"PUT", "/actions"},
		{"PATCH", "/vm"},
		{"PUT", "/snapshot/create"},
		{"PUT", "/snapshot/load"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d calls, want %d: %#v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].method != w.method || got[i].path != w.path {
			t.Fatalf("call %d = %s %s, want %s %s", i, got[i].method, got[i].path, w.method, w.path)
		}
	}

	var mc map[string]any
	mustNoErr(t, json.Unmarshal([]byte(got[0].body), &mc))
	if mc["vcpu_count"].(float64) != 2 || mc["mem_size_mib"].(float64) != 512 {
		t.Fatalf("machine-config body = %s", got[0].body)
	}
	var action map[string]any
	mustNoErr(t, json.Unmarshal([]byte(got[5].body), &action))
	if action["action_type"] != "InstanceStart" {
		t.Fatalf("action body = %s", got[5].body)
	}
	var vm map[string]any
	mustNoErr(t, json.Unmarshal([]byte(got[6].body), &vm))
	if vm["state"] != "Paused" {
		t.Fatalf("vm body = %s", got[6].body)
	}
	var snap map[string]any
	mustNoErr(t, json.Unmarshal([]byte(got[7].body), &snap))
	if snap["snapshot_type"] != "Full" || snap["snapshot_path"] != "/s/vmstate" || snap["mem_file_path"] != "/s/mem" {
		t.Fatalf("snapshot/create body = %s", got[7].body)
	}
}

func TestAPIClientSurfacesErrorBody(t *testing.T) {
	sock := startFakeFirecracker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"fault_message":"the kernel is wrong"}`)
	}))
	c := newAPIClient(sock)
	err := c.instanceStart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "the kernel is wrong") {
		t.Fatalf("err = %v, want it to contain the fault message", err)
	}
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
