package client

import (
	"context"
	"go-silver-core/internal/gsp"
	"go-silver-core/pkg/conn_pool"
	"go-silver-core/pkg/mempool"
	"net"
	"sync"
)

type controlState struct {
	mu     sync.Mutex
	conn   net.Conn
	peerID string
}
type GspSdk struct {
	srvAddr  string
	codec    gsp.Codec
	connPool *conn_pool.ConnPool
	memPool  *mempool.MemPool
	ctx      context.Context
	control  *controlState
	zone     string
}

func NewGspSdk(addr string, pool *mempool.MemPool, identity ...string) GspSdk {
	g := GspSdk{srvAddr: addr, connPool: conn_pool.NewConnPool(10), memPool: pool, ctx: context.Background(), control: &controlState{}}
	if len(identity) > 0 {
		g.control.peerID = identity[0]
	}
	if len(identity) > 1 {
		g.zone = identity[1]
	}
	return g
}
func (g GspSdk) WithContext(ctx context.Context) GspSdk { g.ctx = ctx; return g }
func (g *GspSdk) Close() {
	g.control.mu.Lock()
	if g.control.conn != nil {
		_ = g.control.conn.Close()
		g.control.conn = nil
	}
	g.control.mu.Unlock()
	_ = g.connPool.Close()
}
