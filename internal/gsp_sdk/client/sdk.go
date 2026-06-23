package client

import (
	"go-silver-core/internal/gsp"
	"go-silver-core/pkg/conn_pool"
	"go-silver-core/pkg/mempool"
)

// GspSdk 大多数的功能是给 receiver 端调用的
type GspSdk struct {
	srvAddr  string
	codec    gsp.Codec
	connPool *conn_pool.ConnPool
	memPool  *mempool.MemPool
	selfUUID string // 本节点 UUID，随请求上报给中心用于归因
	zone     string // 本节点机房标签（可空，空则中心按 IP 推断）
}

// NewGspSdk 创建一个 SDK 实例。selfUUID/zone 用于让中心调度器识别请求方身份与机房。
func NewGspSdk(srvAddr string, memPool *mempool.MemPool, selfUUID string, zone string) GspSdk {
	connPool := conn_pool.NewConnPool(10)
	return GspSdk{
		connPool: connPool,
		srvAddr:  srvAddr,
		codec:    gsp.Codec{},
		memPool:  memPool,
		selfUUID: selfUUID,
		zone:     zone,
	}
}
