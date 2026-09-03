// Package netlimit bounds concurrent connections accepted by a listener.
package netlimit

import (
	"net"
	"sync"
)

// DefaultMaxConnections is the per-listener bound shared by host services
// exposed to a guest or published network client.
const DefaultMaxConnections = 128

// Listener refuses connections beyond its concurrent limit and closes every
// active connection when it is closed.
type Listener struct {
	net.Listener
	sem chan struct{}

	mu     sync.Mutex
	active map[*limitedConn]struct{}
	closed bool
	once   sync.Once
	err    error
}

// New wraps listener with a fail-closed concurrent connection limit.
func New(listener net.Listener, maxConnections int) *Listener {
	if maxConnections <= 0 {
		panic("netlimit: max connections must be positive")
	}
	return &Listener{
		Listener: listener,
		sem:      make(chan struct{}, maxConnections),
		active:   make(map[*limitedConn]struct{}),
	}
}

// Accept closes excess connections rather than queueing them behind active
// handlers. Accepted connections release their slot when closed.
func (l *Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.sem <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}

		limited := &limitedConn{Conn: conn, owner: l}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			_ = conn.Close()
			<-l.sem
			return nil, net.ErrClosed
		}
		l.active[limited] = struct{}{}
		l.mu.Unlock()
		return limited, nil
	}
}

// Close stops accepts and closes active connections so idle clients cannot
// outlive the workspace service that accepted them.
func (l *Listener) Close() error {
	l.once.Do(func() {
		l.err = l.Listener.Close()
		l.mu.Lock()
		l.closed = true
		active := make([]*limitedConn, 0, len(l.active))
		for conn := range l.active {
			active = append(active, conn)
		}
		l.mu.Unlock()
		for _, conn := range active {
			_ = conn.Close()
		}
	})
	return l.err
}

func (l *Listener) release(conn *limitedConn) {
	l.mu.Lock()
	if _, ok := l.active[conn]; ok {
		delete(l.active, conn)
		<-l.sem
	}
	l.mu.Unlock()
}

type limitedConn struct {
	net.Conn
	owner *Listener
	once  sync.Once
	err   error
}

func (c *limitedConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.owner.release(c)
	})
	return c.err
}
