package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/modelservice"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// Exercise the real companion argv, process lifecycle and forwarding without
// downloading a model or booting a VM. This runs on both supported host OSes.
func TestModelServiceCompanionRestartAndPolicy(t *testing.T) {
	bundled, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	standalone := filepath.Join(t.TempDir(), "microagent-model-service")
	build := exec.Command("go", "build", "-o", standalone, "../microagent-model-service")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build companion: %v\n%s", err, out)
	}
	for _, c := range []struct{ name, path string }{{"bundled", bundled}, {"standalone", standalone}} {
		t.Run(c.name, func(t *testing.T) { testModelServiceCompanion(t, c.path) })
	}
}

func testModelServiceCompanion(t *testing.T, exe string) {
	dir := t.TempDir()
	const ref = "hf.co/test/model@main/model.gguf"
	server := func(body string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		t.Cleanup(s.Close)
		return s
	}
	first, second := server("first"), server("second")
	record := func(s *httptest.Server) modelrunner.Record {
		host, rawPort, err := net.SplitHostPort(strings.TrimPrefix(s.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(rawPort)
		if err != nil {
			t.Fatal(err)
		}
		return modelrunner.Record{Key: "paired", ModelRef: ref, Host: host, Port: port, PID: os.Getpid()}
	}
	writeRunner := func(records ...modelrunner.Record) {
		if err := modelrunner.WriteIndex(dir, modelrunner.Index{Runners: records}); err != nil {
			t.Fatal(err)
		}
	}
	writeRunner(record(first))
	opts := modelservice.Options{StateDir: dir, WorkspaceID: "service-test", ExecPath: exe, Runner: record(first)}
	t.Cleanup(func() { _ = modelservice.Release(dir, opts.WorkspaceID) })
	attachment, err := modelservice.Attach(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Mode != "forward" || attachment.AuditLogPath != "" {
		t.Fatalf("unmediated attachment = %+v", attachment)
	}
	reused, err := modelservice.Attach(t.Context(), opts)
	if err != nil || reused.PID != attachment.PID {
		t.Fatalf("reuse = %+v, %v", reused, err)
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	t.Cleanup(client.CloseIdleConnections)
	assertResponse := func(target, want string, status int) {
		resp, err := client.Get("http://" + target + "/v1/models")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status || !strings.Contains(string(body), want) {
			t.Fatalf("response %d %q, want %d containing %q", resp.StatusCode, body, status, want)
		}
	}
	assertResponse(attachment.Target, "first", http.StatusOK)
	writeRunner(record(second))
	first.Close()
	assertResponse(attachment.Target, "second", http.StatusOK)
	// The bootstrap address must not be used if the runner disappears.
	writeRunner()
	if resp, err := client.Get("http://" + attachment.Target + "/v1/models"); err == nil {
		resp.Body.Close()
		t.Fatal("missing runner unexpectedly accepted a request")
	}
	writeRunner(record(second))
	policy := server(`{"decision":"deny","reason":"blocked by fixture"}`)
	opts.Mode, opts.PolicyURL = "policy", policy.URL
	mediated, err := modelservice.Attach(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	assertResponse(mediated.Target, "blocked by fixture", http.StatusForbidden)
	// The library sends only the service address to either supervisor.
	// No runner ref can cause a mediator bypass.
	for _, backend := range []string{vmkit.BackendLinuxKVM, vmkit.BackendAppleVF} {
		for _, service := range []modelservice.Attachment{attachment, mediated} {
			req, err := workspace.Request(workspace.Options{
				Name: "service-test", StateDir: dir, Backend: backend, Model: ref,
				ModelRunnerKey: "paired", ModelTarget: service.Target, ModelTargetStable: true,
			}, "run", "/tmp/rootfs.ext4", "request")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, listener := range req.Config.VsockListeners {
				if listener.Port != workspace.DefaultModelVsockPort {
					continue
				}
				found = true
				if listener.Target != service.Target || listener.ModelRef != "" || listener.ModelRunnerKey != "" {
					t.Fatalf("%s listener can bypass service: %+v", backend, listener)
				}
			}
			if !found {
				t.Fatalf("%s missing model listener", backend)
			}
		}
	}
	if err := modelservice.Release(dir, opts.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", mediated.Target, time.Second)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("released service still accepts connections")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
