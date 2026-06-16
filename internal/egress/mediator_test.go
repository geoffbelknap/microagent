package egress

import (
	"bufio"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestHandlerAllowForwards(t *testing.T) {
	up, _ := net.Listen("tcp", "127.0.0.1:0")
	defer up.Close()
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		io.Copy(c, c) // echo
		c.Close()
	}()
	upAddr := netip.MustParseAddrPort(up.Addr().String())

	pol, _ := NewPolicy([]string{"api.github.com"})
	log := &BufferLogger{}
	h := &Handler{
		Policy:       pol,
		Logger:       log,
		OrigDst:      func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
		Dial:         net.Dial,
		SniffTimeout: 500 * time.Millisecond,
	}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { h.Handle(server); close(done) }()
	go func() {
		client.Write([]byte("GET / HTTP/1.1\r\nHost: api.github.com\r\n\r\n"))
	}()
	br := bufio.NewReader(client)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil || line != "GET / HTTP/1.1\r\n" {
		t.Fatalf("echo = %q err=%v", line, err)
	}
	client.Close()
	<-done // wait for Handle to finish writing the audit log before reading it
	assertEvent(t, log, "egress_allow")
}

func TestHandlerDenyFailsClosed(t *testing.T) {
	dialed := false
	pol, _ := NewPolicy([]string{"api.github.com"})
	log := &BufferLogger{}
	h := &Handler{
		Policy:       pol,
		Logger:       log,
		OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("10.0.0.9:443"), nil },
		Dial:         func(string, string) (net.Conn, error) { dialed = true; return nil, nil },
		SniffTimeout: 500 * time.Millisecond,
	}
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { h.Handle(server); close(done) }()
	go client.Write([]byte("GET / HTTP/1.1\r\nHost: evil.com\r\n\r\n"))
	<-done
	client.Close()
	if dialed {
		t.Fatal("upstream dialed despite deny (must fail closed)")
	}
	assertEvent(t, log, "egress_deny")
}

// TestMediatedAllowsUnlisted proves the Mode field's two behaviors against a
// host that is NOT on the allowlist:
//   - Mode "mediated": the connection is forwarded (L4 splice roundtrips) and
//     logged egress_allow with unlisted=true.
//   - Mode "strict": the same host is denied (egress_deny, no upstream dial).
func TestMediatedAllowsUnlisted(t *testing.T) {
	t.Run("mediated forwards unlisted", func(t *testing.T) {
		up, _ := net.Listen("tcp", "127.0.0.1:0")
		defer up.Close()
		go func() {
			c, err := up.Accept()
			if err != nil {
				return
			}
			io.Copy(c, c) // echo
			c.Close()
		}()
		upAddr := netip.MustParseAddrPort(up.Addr().String())

		pol, _ := NewPolicy([]string{"allowed.example.com"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:         "mediated",
			Policy:       pol,
			Logger:       log,
			OrigDst:      func(net.Conn) (netip.AddrPort, error) { return upAddr, nil },
			Dial:         net.Dial,
			SniffTimeout: 500 * time.Millisecond,
		}

		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { h.Handle(server); close(done) }()
		go func() {
			client.Write([]byte("GET / HTTP/1.1\r\nHost: unlisted.example.com\r\n\r\n"))
		}()
		br := bufio.NewReader(client)
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil || line != "GET / HTTP/1.1\r\n" {
			t.Fatalf("echo = %q err=%v (unlisted host must be forwarded in mediated mode)", line, err)
		}
		client.Close()
		<-done
		assertEventWithField(t, log, "egress_allow", "unlisted", true)
	})

	t.Run("strict denies unlisted", func(t *testing.T) {
		dialed := false
		pol, _ := NewPolicy([]string{"allowed.example.com"})
		log := &BufferLogger{}
		h := &Handler{
			Mode:         "strict",
			Policy:       pol,
			Logger:       log,
			OrigDst:      func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("10.0.0.9:443"), nil },
			Dial:         func(string, string) (net.Conn, error) { dialed = true; return nil, nil },
			SniffTimeout: 500 * time.Millisecond,
		}
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { h.Handle(server); close(done) }()
		go client.Write([]byte("GET / HTTP/1.1\r\nHost: unlisted.example.com\r\n\r\n"))
		<-done
		client.Close()
		if dialed {
			t.Fatal("upstream dialed despite strict deny (must fail closed)")
		}
		assertEvent(t, log, "egress_deny")
	})
}

func assertEvent(t *testing.T, log *BufferLogger, event string) {
	t.Helper()
	for _, e := range log.Events {
		if e["event"] == event {
			return
		}
	}
	t.Fatalf("event %q not logged; got %+v", event, log.Events)
}
