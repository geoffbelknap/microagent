//go:build linux

package firecracker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestLifecycleAuditPersistsInTerminalEvent(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Name: "agent", StateDir: dir}
	audit := &vmkit.LifecycleAudit{
		Initiator:    vmkit.CallerAttribution{Channel: "mcp", Subject: "operator-7", Assurance: "caller_asserted"},
		Reason:       "incident response",
		WorkInFlight: vmkit.WorkInFlight{CaptureStatus: "captured", GuestReported: []vmkit.GuestProcess{{PID: 42, PPID: 1, Command: "run-task"}}},
		Notification: vmkit.NotificationRecord{Status: "not_performed", Owner: "caller"},
	}
	req := vmkit.Request{
		Command: "halt", Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: opts.Name, Role: vmkit.RoleWorkload, Backend: vmkit.BackendLinuxKVM},
		Config: &vmkit.Config{StateDir: dir}, Lifecycle: audit,
	}
	if err := writeProcessState(opts, req, vmkit.StateHalted, 0, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, opts.Name, "event.json"))
	if err != nil {
		t.Fatal(err)
	}
	var event eventFile
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if event.Lifecycle == nil || event.Lifecycle.Reason != "incident response" || len(event.Lifecycle.WorkInFlight.GuestReported) != 1 {
		t.Fatalf("event lifecycle = %#v", event.Lifecycle)
	}
	resp := responseFromEvent(event, "")
	if resp.Event == nil || resp.Event.Lifecycle == nil || resp.Event.Lifecycle.Initiator.Subject != "operator-7" {
		t.Fatalf("response lifecycle = %#v", resp.Event)
	}
}
