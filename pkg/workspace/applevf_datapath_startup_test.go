package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func appleVFDatapathStartFixture(t *testing.T, statusJSON string, exitStatus int) (Options, vmkit.Request) {
	t.Helper()
	dir := t.TempDir()
	workspaceDir := filepath.Join(dir, "datapath-start")
	statusPath := filepath.Join(workspaceDir, "datapath-startup.json")
	supervisor := filepath.Join(dir, "fake-applevf-supervisor")
	script := fmt.Sprintf(
		"#!/bin/sh\ncat >/dev/null\nmkdir -p %q\nprintf '%%s\\n' %q > %q\nexit %d\n",
		workspaceDir, statusJSON, statusPath, exitStatus,
	)
	if err := os.WriteFile(supervisor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	kernel := filepath.Join(dir, "Image")
	if err := os.WriteFile(kernel, []byte("kernel"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Name:           "datapath-start",
		StateDir:       dir,
		Backend:        vmkit.BackendAppleVF,
		KernelPath:     kernel,
		SupervisorPath: supervisor,
	}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "request",
			RuntimeID: opts.Name,
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendAppleVF,
		},
		Config: &vmkit.Config{
			StateDir:   dir,
			Network:    &vmkit.NetworkConfig{Mode: "user"},
			EgressMode: vmkit.EgressModeMITM,
		},
	}
	return opts, req
}

func TestStartDetachedReturnsTypedAppleVFDatapathFailure(t *testing.T) {
	failureJSON := `{"ok":false,"failure":{"boundary":"apple-vf.host-fd.datapath","executablePath":"/usr/bin/false","exitStatus":23,"diagnosticsPath":"/state/ws/datapath.log","reason":"datapath exited before preboot readiness"}}`
	opts, req := appleVFDatapathStartFixture(t, failureJSON, 1)
	statusPath := filepath.Join(opts.StateDir, opts.Name, "datapath-startup.json")
	if err := os.MkdirAll(filepath.Dir(statusPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	resp, err := startDetached(opts, req)
	if err == nil {
		t.Fatal("startDetached succeeded with failed datapath status")
	}
	if resp.OK || resp.DatapathStartupFailure == nil {
		t.Fatalf("response = %#v, want typed datapath failure", resp)
	}
	if resp.DatapathStartupFailure.Boundary != "apple-vf.host-fd.datapath" ||
		resp.DatapathStartupFailure.ExitStatus == nil || *resp.DatapathStartupFailure.ExitStatus != 23 {
		t.Fatalf("datapath failure = %#v", resp.DatapathStartupFailure)
	}
	for _, want := range []string{"apple-vf.host-fd.datapath", "exit_status=23", "/state/ws/datapath.log"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
	runtimeState, stateErr := ReadRuntimeState(opts)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if runtimeState.Event.State != vmkit.StateFailed || runtimeState.PID != 0 {
		t.Fatalf("runtime state = %#v, want failed with no live PID", runtimeState)
	}
}

func TestStartDetachedWaitsForAppleVFDatapathSuccess(t *testing.T) {
	opts, req := appleVFDatapathStartFixture(t, `{"ok":true}`, 0)
	var phases []string
	opts.Progress = func(event operation.ProgressEvent) {
		phases = append(phases, event.Phase)
	}
	resp, err := startDetached(opts, req)
	if err != nil {
		t.Fatalf("startDetached: %v", err)
	}
	if !resp.OK || resp.DatapathStartupFailure != nil {
		t.Fatalf("response = %#v, want successful detached start", resp)
	}
	if len(phases) != 1 || phases[0] != "start_interface" {
		t.Fatalf("progress phases = %#v, want start_interface", phases)
	}
}

func TestAppleVFDatapathStartupGateOnlyAppliesToMediatedUserNetworking(t *testing.T) {
	opts := Options{Backend: vmkit.BackendAppleVF}
	req := vmkit.Request{Config: &vmkit.Config{Network: &vmkit.NetworkConfig{Mode: "user"}, EgressMode: vmkit.EgressModeMITM}}
	if !appleVFDatapathStartupRequired(opts, req) {
		t.Fatal("mediated Apple VF user networking did not require the startup gate")
	}
	for _, mutate := range []func(*Options, *vmkit.Request){
		func(opts *Options, _ *vmkit.Request) { opts.Backend = vmkit.BackendLinuxKVM },
		func(_ *Options, req *vmkit.Request) { req.Config.Network.Mode = "isolated" },
		func(_ *Options, req *vmkit.Request) { req.Config.EgressMode = vmkit.EgressModeOff },
	} {
		candidateOpts := opts
		candidateReq := req
		config := *req.Config
		network := *req.Config.Network
		config.Network = &network
		candidateReq.Config = &config
		mutate(&candidateOpts, &candidateReq)
		if appleVFDatapathStartupRequired(candidateOpts, candidateReq) {
			t.Fatalf("startup gate unexpectedly required for opts=%#v config=%#v", candidateOpts, candidateReq.Config)
		}
	}
}
