package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"go-silver-core/internal/chunk"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"hash/crc32"
	"log"
	"net"
	"strconv"
)

// GetFileStatus 获取文件状态请求
func (g *GspSdk) GetFileStatus() (r model.GetFileStatusResp, err error) {
	conn, err := g.connPool.GetConn(g.srvAddr)
	if err != nil {
		return
	}
	defer g.connPool.PutConn(g.srvAddr, conn)
	req := model.BaseJson{Operate: "getFileStatus"}
	reqJson, _ := json.Marshal(req)
	if err = g.codec.EncodeTo(conn, gsp.TypeJSON, reqJson); err != nil {
		return
	}
	// 接收数据信息
	buf := g.memPool.Get(_const.ChunkSize)
	defer g.memPool.Put(buf)
	resp, _ := g.codec.Decode(conn, *buf)
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
	if err = g.codec.EncodeTo(conn, gsp.TypeJSON, reqJson); err != nil {
		return
	}
	buf := g.memPool.Get(_const.ChunkSize)
	defer g.memPool.Put(buf)
	resp, err := g.codec.Decode(conn, *buf)
	if err != nil || resp == nil {
		return nil, 0, fmt.Errorf("接收块信息失败: %v", err)
	}
	var chunkInfo model.GetChunkResp
	if err := json.Unmarshal(resp.Payload, &chunkInfo); err != nil {
		return nil, 0, fmt.Errorf("解析块信息失败: %v", err)
	}
	buf2 := g.memPool.Get(_const.ChunkSize)
	defer g.memPool.Put(buf2)
	resp, err = g.codec.Decode(conn, *buf2)
	if err != nil {
		return nil, 0, err
	}
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

// ReportChunk 告知服务端，我是uuid 我已经拥有 第 i 块
func (g *GspSdk) ReportChunk(uuid string, i int64) error {
	conn, err := g.connPool.GetConn(g.srvAddr)
	defer g.connPool.PutConn(g.srvAddr, conn)
	if err != nil {
		return err
	}
	reqG := model.ReportChunkReq{Index: i, Operate: "reportChunk", UUID: uuid}
	reqJson, _ := json.Marshal(reqG)
	if err = g.codec.EncodeTo(conn, gsp.TypeJSON, reqJson); err != nil {
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
	if err = g.codec.EncodeTo(conn, gsp.TypeJSON, reqJson); err != nil {
		return nil, err
	}
	buf := g.memPool.Get(_const.ChunkSize)
	defer g.memPool.Put(buf)
	resp, err := g.codec.Decode(conn, *buf)
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

// PeerReg Peer 节点注册。
// 使用独立的长连接（不归还连接池），保持该连接存活 = 本节点在 Tracker 上注册存活。
// 连接断开时 Tracker 自动清理此 Peer 的所有分块记录。
func (g *GspSdk) PeerReg(peerPort int, uuid string) error {
	// 直接拨号，不从连接池借，避免耗尽连接池供其他操作使用
	controlConn, err := net.Dial("tcp", g.srvAddr)
	if err != nil {
		return err
	}
	codec := gsp.Codec{}
	jsonReq, _ := json.Marshal(model.PeerRegReq{
		Operate: "peerReg",
		Port:    strconv.Itoa(peerPort),
		UUID:    uuid,
	})
	if err := codec.EncodeTo(controlConn, gsp.TypeJSON, jsonReq); err != nil {
		controlConn.Close()
		return err
	}
	// 持有控制连接直到 Tracker 关闭，保活用。
	go func() {
		defer controlConn.Close()
		buf := [1]byte{}
		_, _ = codec.Decode(controlConn, buf[:])
		log.Println("[client] 与分发服务端控制连接断开")
	}()
	return nil
}

// ReportPeer 向服务端发送Peer信息，包括提供下载的对端UUID和本次状态
func (g *GspSdk) ReportPeer(uuid string, providerUuid string, speed int64, status string) error {
	conn, err := g.connPool.GetConn(g.srvAddr)
	defer g.connPool.PutConn(g.srvAddr, conn)
	if err != nil {
		return err
	}
	reqG := model.PeerReportReq{
		Operate:      "reportPeer",
		UUID:         uuid,
		ProviderUUID: providerUuid,
		Status:       status,
		Speed:        speed,
	}
	reqJson, _ := json.Marshal(reqG)
	if err = g.codec.EncodeTo(conn, gsp.TypeJSON, reqJson); err != nil {
		return err
	}
	return nil
}
