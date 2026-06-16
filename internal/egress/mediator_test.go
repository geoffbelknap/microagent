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
	go h.Handle(server)
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

func assertEvent(t *testing.T, log *BufferLogger, event string) {
	t.Helper()
	for _, e := range log.Events {
		if e["event"] == event {
			return
		}
	}
	t.Fatalf("event %q not logged; got %+v", event, log.Events)
}
