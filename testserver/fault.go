package testserver

import (
	"bytes"
	"net"
	"sync"
	"time"
)

// Fault describes how the server misbehaves on one accepted connection.
type Fault struct {
	// Reject closes the connection right after accept, before any greeting.
	Reject bool
	// CloseAfterCommands drops the connection once the client has sent this
	// many lines. 0 means never.
	CloseAfterCommands int
	// Latency delays every read from the client.
	Latency time.Duration
}

// faultListener hands the next queued Fault to each accepted connection.
type faultListener struct {
	net.Listener
	mu    sync.Mutex
	queue []Fault
}

func (l *faultListener) enqueue(count int, f Fault) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for range count {
		l.queue = append(l.queue, f)
	}
}

func (l *faultListener) next() (Fault, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.queue) == 0 {
		return Fault{}, false
	}
	f := l.queue[0]
	l.queue = l.queue[1:]
	return f, true
}

func (l *faultListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		f, ok := l.next()
		if !ok {
			return conn, nil
		}
		if f.Reject {
			conn.Close()
			continue
		}
		return &faultConn{Conn: conn, fault: f}, nil
	}
}

// faultConn counts client lines and applies the Fault to reads.
type faultConn struct {
	net.Conn
	fault    Fault
	commands int
}

func (c *faultConn) Read(p []byte) (int, error) {
	if c.fault.Latency > 0 {
		time.Sleep(c.fault.Latency)
	}
	n, err := c.Conn.Read(p)
	if n > 0 && c.fault.CloseAfterCommands > 0 {
		c.commands += bytes.Count(p[:n], []byte{'\n'})
		if c.commands >= c.fault.CloseAfterCommands {
			c.Conn.Close()
			return n, net.ErrClosed
		}
	}
	return n, err
}
