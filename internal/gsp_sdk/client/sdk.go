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
}

func NewGspSdk(addr string, pool *mempool.MemPool) GspSdk {
	return GspSdk{srvAddr: addr, connPool: conn_pool.NewConnPool(10), memPool: pool, ctx: context.Background(), control: &controlState{}}
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
