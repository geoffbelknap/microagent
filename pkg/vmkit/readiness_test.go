package vmkit

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestMediationReadinessSignalRequiresLiveReachability(t *testing.T) {
	mediation := MediationConfig{
		Enabled:    true,
		Required:   true,
		Port:       2048,
		Target:     unusedTCPAddr(t),
		FailClosed: true,
	}
	signal := MediationReadinessSignal(context.Background(), mediation, StateRunning, nil, 150*time.Millisecond)
	if signal.Ready {
		t.Fatalf("mediation readiness = %#v, want not ready before target is reachable", signal)
	}
	if signal.Error == "" || !strings.Contains(signal.Detail, "unreachable") {
		t.Fatalf("mediation readiness = %#v, want required unreachable error detail", signal)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	acceptDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptDone <- err
			return
		}
		acceptDone <- conn.Close()
	}()
	mediation.Target = listener.Addr().String()
	signal = MediationReadinessSignal(context.Background(), mediation, StateRunning, nil, time.Second)
	if !signal.Ready {
		t.Fatalf("mediation readiness = %#v, want ready when target is reachable", signal)
	}
	if signal.Error != "" || !strings.Contains(signal.Detail, "reachable") {
		t.Fatalf("mediation readiness = %#v, want reachable detail without error", signal)
	}
	if err := <-acceptDone; err != nil {
		t.Fatalf("mediation target accept: %v", err)
	}
}

func TestMediationReadinessSignalOptionalUnavailableIsNotHardError(t *testing.T) {
	mediation := MediationConfig{
		Enabled:  true,
		Port:     2048,
		Target:   unusedTCPAddr(t),
		Required: false,
	}
	signal := MediationReadinessSignal(context.Background(), mediation, StateRunning, nil, 150*time.Millisecond)
	if signal.Ready {
		t.Fatalf("mediation readiness = %#v, want not ready before target is reachable", signal)
	}
	if signal.Error != "" {
		t.Fatalf("mediation readiness error = %q, want no hard error for optional mediation", signal.Error)
	}
}

func unusedTCPAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	// On some network stacks (notably WSL2) closing a listener takes a few
	// milliseconds to propagate, during which the port still completes TCP
	// handshakes. A caller that immediately probes the address would see it as
	// reachable. Wait until the address genuinely refuses connections before
	// handing it out; close propagation is monotonic, so once it refuses it
	// stays refused for the rest of the test.
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			break
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("address %s still reachable after closing its listener; could not obtain an unused port", addr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return addr
}
