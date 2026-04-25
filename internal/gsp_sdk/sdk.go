package gsp_sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"go-silver-core/internal/chunk"
	"go-silver-core/internal/conn_pool"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"hash/crc32"
)

// GspSdk 大多数的功能是给 receiver 端调用的
type GspSdk struct {
	srvAddr  string
	codec    gsp.Codec
	connPool *conn_pool.ConnPool
}

func NewGspSdk(srvAddr string) GspSdk {
	connPool := conn_pool.NewConnPool(3)
	return GspSdk{connPool: connPool, srvAddr: srvAddr}
}

// GetFileStatus 获取文件状态请求
func (g *GspSdk) GetFileStatus() (r model.GetFileStatusResp, err error) {
	conn, err := g.connPool.GetConn(g.srvAddr)
	if err != nil {
		return
	}
	req := model.BaseJson{Operate: "getFileStatus"}
	reqJson, _ := json.Marshal(req)
	reqLoad := g.codec.Encode(gsp.TypeJSON, reqJson)
	if _, err = conn.Write(reqLoad); err != nil {
		return
	}
	// 接收数据信息
	resp, _ := g.codec.Decode(conn)
	fmt.Println(string(resp.Payload))
	if err = json.Unmarshal(resp.Payload, &r); err != nil {
		return
	}
	return
}

// GetChunk 获取文件块
func (g *GspSdk) GetChunk(addr string, i int64, ck *chunk.FileChunk) (r []byte, checksum uint32, err error) {
	conn, err := g.connPool.GetConn(addr)
	defer g.connPool.PutConn(addr, conn)
	if err != nil {
		return r, 0, err
	}
	reqG := model.GetChunkReq{Index: i, Operate: "getChunk"}
	reqJson, _ := json.Marshal(reqG)
	reqLoad := g.codec.Encode(gsp.TypeJSON, reqJson)
	if _, err = conn.Write(reqLoad); err != nil {
		return
	}
	resp, err := g.codec.Decode(conn)
	if err != nil || resp == nil {
		return nil, 0, fmt.Errorf("接收块信息失败: %v", err)
	}
	var chunkInfo model.GetChunkResp
	if err := json.Unmarshal(resp.Payload, &chunkInfo); err != nil {
		return nil, 0, fmt.Errorf("解析块信息失败: %v", err)
	}
	resp, _ = g.codec.Decode(conn)
	r = resp.Payload
	curChecksum := crc32.ChecksumIEEE(resp.Payload)
	if curChecksum != chunkInfo.CheckSum {
		return r, 0, errors.New("接收块失败，Checksum校验失败")
	}
	checksum = curChecksum
	ck.Save(i, resp.Payload)
	// 归还conn

	return
}

// ReportChunk 告知服务端，我已经拥有 第 i 块
func (g *GspSdk) ReportChunk(localPort string, i int64) error {
	conn, err := g.connPool.GetConn(g.srvAddr)
	defer g.connPool.PutConn(g.srvAddr, conn)
	if err != nil {
		return err
	}
	reqG := model.ReportChunkReq{Index: i, Operate: "reportChunk", Port: localPort}
	reqJson, _ := json.Marshal(reqG)
	reqLoad := g.codec.Encode(gsp.TypeJSON, reqJson)
	if _, err = conn.Write(reqLoad); err != nil {
		return err
	}
	return nil
}

// WantChunk 向服务端请求第i块
// 服务端处理后将会返回一个地址
func (g *GspSdk) WantChunk(i int64) (*model.WantChunkResp, error) {
	conn, err := g.connPool.GetConn(g.srvAddr)
	defer g.connPool.PutConn(g.srvAddr, conn)
	if err != nil {
		return nil, err
	}
	reqG := model.WantChunkReq{Index: i, Operate: "wantChunk"}
	reqJson, _ := json.Marshal(reqG)
	reqLoad := g.codec.Encode(gsp.TypeJSON, reqJson)
	if _, err = conn.Write(reqLoad); err != nil {
		return nil, err
	}
	resp, err := g.codec.Decode(conn)
	if err != nil {
		return nil, err
	}
	if resp.Type != gsp.TypeJSON {
		return nil, errors.New("与预期返回类型不符")
	}
	var respJ model.WantChunkResp
	err = json.Unmarshal(resp.Payload, &respJ)
	if err != nil {
		return nil, errors.New("JSON 解析失败")
	}
	return &respJ, nil
}
