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
	mustNoErr(t, c.patchVMState(ctx, "Paused"))
	mustNoErr(t, c.patchVMState(ctx, "Resumed"))
	mustNoErr(t, c.createSnapshot(ctx, "/s/vmstate", "/s/mem"))
	mustNoErr(t, c.loadSnapshot(ctx, "/s/vmstate", "/s/mem", true, []networkOverride{{IfaceID: "eth0", HostDevName: "tap-fork1"}}))

	want := []struct{ method, path string }{
		{"PATCH", "/vm"},
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

	var pause map[string]any
	mustNoErr(t, json.Unmarshal([]byte(got[0].body), &pause))
	if pause["state"] != "Paused" {
		t.Fatalf("pause body = %s", got[0].body)
	}
	var snap map[string]any
	mustNoErr(t, json.Unmarshal([]byte(got[2].body), &snap))
	if snap["snapshot_type"] != "Full" || snap["snapshot_path"] != "/s/vmstate" || snap["mem_file_path"] != "/s/mem" {
		t.Fatalf("snapshot/create body = %s", got[2].body)
	}
	var load map[string]any
	mustNoErr(t, json.Unmarshal([]byte(got[3].body), &load))
	if load["resume_vm"] != true {
		t.Fatalf("snapshot/load body = %s", got[3].body)
	}
}

func TestAPIClientSurfacesErrorBody(t *testing.T) {
	sock := startFakeFirecracker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"fault_message":"the kernel is wrong"}`)
	}))
	c := newAPIClient(sock)
	err := c.patchVMState(context.Background(), "Paused")
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
