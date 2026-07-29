package firecracker

import (
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// An enabled mediation config with no listener on its port must synthesize
// one, mirroring the apple-vf supervisor: a direct library caller setting
// only Config.Mediation would otherwise boot a guest dialing a port nothing
// serves — with failClosed mediation, silent total egress loss.
func TestEffectiveVsockListenersSynthesizesMediation(t *testing.T) {
	if got := effectiveVsockListeners(nil); got != nil {
		t.Fatalf("nil config = %#v", got)
	}
	config := &vmkit.Config{
		Mediation: &vmkit.MediationConfig{Enabled: true, Port: 1027, Target: "127.0.0.1:9000"},
	}
	got := effectiveVsockListeners(config)
	if len(got) != 1 || got[0].Port != 1027 || got[0].Target != "127.0.0.1:9000" {
		t.Fatalf("synthesized = %#v", got)
	}
	if len(config.VsockListeners) != 0 {
		t.Fatalf("caller config mutated: %#v", config.VsockListeners)
	}

	// A listener already on the mediation port wins; nothing is added.
	config.VsockListeners = []vmkit.VsockListener{{Port: 1027, Target: "127.0.0.1:9001"}}
	got = effectiveVsockListeners(config)
	if len(got) != 1 || got[0].Target != "127.0.0.1:9001" {
		t.Fatalf("occupied port = %#v", got)
	}

	// Disabled mediation synthesizes nothing.
	config = &vmkit.Config{Mediation: &vmkit.MediationConfig{Enabled: false, Port: 1027, Target: "t"}}
	if got := effectiveVsockListeners(config); len(got) != 0 {
		t.Fatalf("disabled mediation = %#v", got)
	}
}

// The accept loop must never run more than limit handlers concurrently, and
// must close excess connections rather than queue them.
func TestServeBoundedAcceptsRefusesBeyondLimit(t *testing.T) {
	dir := t.TempDir()
	listener, err := net.Listen("unix", filepath.Join(dir, "bounded.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	const limit = 2
	release := make(chan struct{})
	var mu sync.Mutex
	active, peak := 0, 0
	go serveBoundedAccepts(listener, limit, func(c net.Conn) {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		_ = c.Close()
	})

	dial := func() net.Conn {
		t.Helper()
		conn, err := net.Dial("unix", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	held := []net.Conn{dial(), dial()}
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()

	// Wait until both held connections occupy the limit.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		occupied := active == limit
		mu.Unlock()
		if occupied {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("handlers never occupied the limit")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The connection beyond the limit is closed by the acceptor: a read
	// returns EOF promptly instead of blocking on a queued handler.
	excess := dial()
	defer func() { _ = excess.Close() }()
	_ = excess.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1)
	if _, err := excess.Read(buf); err == nil {
		t.Fatal("excess connection was handled, want refused (closed)")
	}

	close(release)
	mu.Lock()
	gotPeak := peak
	mu.Unlock()
	if gotPeak > limit {
		t.Fatalf("peak concurrent handlers = %d, want <= %d", gotPeak, limit)
	}
}
