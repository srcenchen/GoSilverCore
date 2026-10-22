package conn_pool

import (
	"context"
	"net"
	"sync"
	"time"
)

type bucket struct {
	idle    chan idleConn
	permits chan struct{}
}

type idleConn struct {
	conn  net.Conn
	since time.Time
}

// Retire pooled sockets before the server's two-minute idle read timeout.
const maxIdleTime = time.Minute

// ConnPool bounds both borrowed and idle connections per address.
type ConnPool struct {
	mu         sync.Mutex
	buckets    map[string]*bucket
	borrowed   map[net.Conn]*bucket
	maxPerConn int
	closed     bool
	done       chan struct{}
}

func NewConnPool(max int) *ConnPool {
	if max < 1 {
		max = 1
	}
	return &ConnPool{buckets: make(map[string]*bucket), borrowed: make(map[net.Conn]*bucket), maxPerConn: max, done: make(chan struct{})}
}
func (p *ConnPool) GetConn(addr string) (net.Conn, error) {
	return p.GetConnContext(context.Background(), addr)
}
func (p *ConnPool) GetConnContext(ctx context.Context, addr string) (net.Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, net.ErrClosed
	}
	b := p.buckets[addr]
	if b == nil {
		b = &bucket{idle: make(chan idleConn, p.maxPerConn), permits: make(chan struct{}, p.maxPerConn)}
		p.buckets[addr] = b
	}
	p.mu.Unlock()
	select {
	case b.permits <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return nil, net.ErrClosed
	}
	var c net.Conn
	for c == nil {
		select {
		case idle := <-b.idle:
			if time.Since(idle.since) < maxIdleTime {
				c = idle.conn
			} else {
				_ = idle.conn.Close()
			}
		default:
			goto borrowed
		}
	}
borrowed:
	if c == nil {
		var err error
		c, err = (&net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", addr)
		if err != nil {
			<-b.permits
			return nil, err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		c.Close()
		<-b.permits
		return nil, net.ErrClosed
	}
	p.borrowed[c] = b
	return c, nil
}
func (p *ConnPool) Discard(c net.Conn)           { p.release(c, false) }
func (p *ConnPool) PutConn(_ string, c net.Conn) { p.release(c, true) }
func (p *ConnPool) release(c net.Conn, reuse bool) {
	if c == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.borrowed[c]
	if b == nil {
		c.Close()
		return
	}
	delete(p.borrowed, c)
	<-b.permits
	if !reuse || p.closed {
		c.Close()
		return
	}
	if err := c.SetDeadline(time.Time{}); err != nil {
		c.Close()
		return
	}
	select {
	case b.idle <- idleConn{conn: c, since: time.Now()}:
	default:
		c.Close()
	}
}
func (p *ConnPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	close(p.done)
	for c := range p.borrowed {
		c.Close()
	}
	for _, b := range p.buckets {
		for {
			select {
			case idle := <-b.idle:
				idle.conn.Close()
			default:
				goto drained
			}
		}
	drained:
	}
	return nil
}
