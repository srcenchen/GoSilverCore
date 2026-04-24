package conn_pool

import (
	"net"
	"sync"
)

// ConnPool 连接池
type ConnPool struct {
	mu         sync.Mutex
	conn       map[string]chan net.Conn
	maxPerConn int
}

func NewConnPool(maxPerConn int) *ConnPool {
	return &ConnPool{
		conn:       make(map[string]chan net.Conn),
		maxPerConn: maxPerConn,
	}
}

// GetConn 借出连接
func (cp *ConnPool) GetConn(addr string) (net.Conn, error) {
	cp.mu.Lock()
	ch, ok := cp.conn[addr]
	if !ok {
		ch = make(chan net.Conn, cp.maxPerConn)
		cp.conn[addr] = ch
	}
	cp.mu.Unlock()

	select {
	case conn := <-ch:
		return conn, nil
	default:
		return net.Dial("tcp", addr)
	}
}

// PutConn 归还连接
func (cp *ConnPool) PutConn(addr string, conn net.Conn) {
	if conn == nil {
		return
	}
	cp.mu.Lock()
	ch, ok := cp.conn[addr]
	if !ok {
		return
	}
	cp.mu.Unlock()
	select {
	case ch <- conn:
	default:
		conn.Close()
	}
}
