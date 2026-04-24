package gsp_sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"go-silver-core/internal/conn_pool"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/pkg/chunk"
	"hash/crc32"
	"net"
)

// GspSdk 大多数的功能是给 receiver 端调用的
type GspSdk struct {
	conn     net.Conn // 与发送端的连接
	codec    gsp.Codec
	connPool *conn_pool.ConnPool
}

func NewGspSdk(conn net.Conn) GspSdk {
	connPool := conn_pool.NewConnPool(3)
	return GspSdk{conn: conn, connPool: connPool}
}

// GetFileStatus 获取文件状态请求
func (g *GspSdk) GetFileStatus() (r model.GetFileStatusResp, err error) {
	req := model.BaseJson{Operate: "getFileStatus"}
	reqJson, _ := json.Marshal(req)
	reqLoad := g.codec.Encode(gsp.TypeJSON, reqJson)
	if _, err = g.conn.Write(reqLoad); err != nil {
		return
	}
	// 接收数据信息
	resp, _ := g.codec.Decode(g.conn)
	fmt.Println(string(resp.Payload))
	if err = json.Unmarshal(resp.Payload, &r); err != nil {
		return
	}
	return
}

// GetChunk 获取文件块
func (g *GspSdk) GetChunk(addr string, i int64, ck *chunk.FileChunk) (r []byte, err error) {
	conn, err := g.connPool.GetConn(addr)
	if err != nil {
		return r, err
	}
	reqG := model.GetChunkReq{Index: i, Operate: "getChunk"}
	reqJson, _ := json.Marshal(reqG)
	reqLoad := g.codec.Encode(gsp.TypeJSON, reqJson)
	if _, err = conn.Write(reqLoad); err != nil {
		return
	}
	resp, _ := g.codec.Decode(conn)
	var chunkInfo model.GetChunkResp
	_ = json.Unmarshal(resp.Payload, &chunkInfo)
	resp, _ = g.codec.Decode(conn)
	r = resp.Payload
	curChecksum := crc32.ChecksumIEEE(resp.Payload)
	if curChecksum != chunkInfo.CheckSum {
		return r, errors.New("接收块失败，Checksum校验失败")
	}
	ck.Save(i, resp.Payload)
	// 归还conn
	g.connPool.PutConn(addr, conn)
	return
}
