package cache

import (
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

const (
	poolMaxIdle = 2
	poolIdleTTL = 5 * time.Minute
)

type pooledConn struct {
	c        *client.Client
	released time.Time
}

// connPool holds a few idle IMAP connections per account so handlers and
// appends skip the TLS handshake and login round trips.
type connPool struct {
	mu   sync.Mutex
	idle []pooledConn
}

// Acquire returns a working connection, reusing an idle one when its NOOP
// still succeeds. Callers must Release it (or Logout on a hard failure).
func (a *Account) Acquire() (*client.Client, error) {
	for {
		c := a.pool.pop()
		if c == nil {
			break
		}
		if c.State()&imap.ConnectedState != 0 && c.Noop() == nil {
			return c, nil
		}
		c.Logout()
	}
	return a.Connect()
}

// Release returns c to the pool, or logs it out when it is dead or the pool
// is full.
func (a *Account) Release(c *client.Client) {
	if c == nil {
		return
	}
	if c.State()&imap.ConnectedState == 0 || !a.pool.push(c) {
		c.Logout()
	}
}

func (a *Account) idleConnections() int {
	a.pool.mu.Lock()
	defer a.pool.mu.Unlock()
	return len(a.pool.idle)
}

// pop returns the most recently released connection, evicting stale ones.
func (p *connPool) pop() *client.Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	cutoff := time.Now().Add(-poolIdleTTL)
	for len(p.idle) > 0 {
		last := p.idle[len(p.idle)-1]
		p.idle = p.idle[:len(p.idle)-1]
		if last.released.Before(cutoff) {
			go last.c.Logout()
			continue
		}
		return last.c
	}
	return nil
}

func (p *connPool) push(c *client.Client) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.idle) >= poolMaxIdle {
		return false
	}
	p.idle = append(p.idle, pooledConn{c: c, released: time.Now()})
	return true
}
