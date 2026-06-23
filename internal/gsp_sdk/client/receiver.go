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
	"strings"
	"time"
)

// GetFileStatus 获取文件状态请求
func (g *GspSdk) GetFileStatus() (r model.GetFileStatusResp, err error) {
	conn, err := g.connPool.GetConn(g.srvAddr)
	if err != nil {
		return
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer func() {
		if err != nil {
			conn.Close()
		} else {
			g.connPool.PutConn(g.srvAddr, conn)
		}
	}()
	req := model.BaseJson{Operate: "getFileStatus"}
	reqJson, _ := json.Marshal(req)
	if err = g.codec.EncodeTo(conn, gsp.TypeJSON, reqJson); err != nil {
		return
	}
	// 接收数据信息
	buf := g.memPool.Get(_const.ChunkSize)
	defer g.memPool.Put(buf)
	resp, derr := g.codec.Decode(conn, *buf)
	if derr != nil || resp == nil {
		// 不能吞掉 Decode 错误：服务端先关连接/网络抖动时 resp 为 nil，
		// 直接 Unmarshal 会空指针 panic。与 GetChunk/WantChunk 行为保持一致。
		err = fmt.Errorf("接收文件状态失败: %v", derr)
		return
	}
	if err = json.Unmarshal(resp.Payload, &r); err != nil {
		return
	}
	return
}

// GetChunk 获取文件块。块数据在函数内经 ck.Save 直接落盘；不返回块切片，
// 避免把指向内存池缓冲（函数返回时已归还）的切片泄漏给调用方造成 use-after-free。
func (g *GspSdk) GetChunk(addr string, i int64, ck *chunk.FileChunk) (checksum uint32, err error) {
	conn, err := g.connPool.GetConn(addr)
	if err != nil {
		return 0, err
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer func() {
		if err != nil {
			conn.Close()
		} else {
			g.connPool.PutConn(addr, conn)
		}
	}()
	reqG := model.GetChunkReq{Index: i, Operate: "getChunk"}
	reqJson, _ := json.Marshal(reqG)
	if err = g.codec.EncodeTo(conn, gsp.TypeJSON, reqJson); err != nil {
		return
	}
	buf := g.memPool.Get(_const.ChunkSize)
	defer g.memPool.Put(buf)
	resp, err := g.codec.Decode(conn, *buf)
	if err != nil || resp == nil {
		return 0, fmt.Errorf("接收块信息失败: %v", err)
	}
	var chunkInfo model.GetChunkResp
	if err := json.Unmarshal(resp.Payload, &chunkInfo); err != nil {
		return 0, fmt.Errorf("解析块信息失败: %v", err)
	}
	if !chunkInfo.Status {
		if chunkInfo.Msg != "" {
			return 0, errors.New(chunkInfo.Msg)
		}
		return 0, errors.New("服务器拒绝提供此分块")
	}
	buf2 := g.memPool.Get(_const.ChunkSize)
	defer g.memPool.Put(buf2)
	resp, err = g.codec.Decode(conn, *buf2)
	if err != nil {
		return 0, err
	}
	curChecksum := crc32.ChecksumIEEE(resp.Payload)
	if curChecksum != chunkInfo.CheckSum {
		return 0, errors.New("接收块失败，Checksum校验失败")
	}
	// 在归还 buf2 之前落盘：Save 内部完成写入，不向调用方暴露指向缓冲的切片。
	if err = ck.Save(i, resp.Payload); err != nil {
		return 0, err
	}
	checksum = curChecksum
	// 归还conn
	return
}

// FetchChunk 按调度中心给的候选源列表下载块 i，内部完成本地故障转移与 ReportPeer 上报：
// 首选源失败立即上报 failed 并转次选，列表耗尽才返回失败（不再整轮重试）。
// senderAddr 用于 Addr 为空（中心源）时定位。
// onAttempt（可空）在每次尝试某个源前回调其地址，供 UI 标注「正在从哪个 peer 下载本块」。
// 返回：块校验值、实测速度(Mbps)、实际使用地址、是否成功。
func (g *GspSdk) FetchChunk(i int64, ck *chunk.FileChunk, sources []model.SourceCandidate, senderAddr string, onAttempt func(addr string)) (cm uint32, speedMbps int64, usedAddr string, ok bool) {
	for _, src := range sources {
		targetAddr := src.Addr
		if targetAddr == "" {
			targetAddr = senderAddr
		}
		if onAttempt != nil {
			onAttempt(targetAddr)
		}
		tBegin := time.Now()
		c2, err := g.GetChunk(targetAddr, i, ck)
		if err != nil {
			// PeerBusy 是源在限流而非故障，按 "busy" 上报（只释放租约、不计失败分）。
			status := "failed"
			if strings.Contains(err.Error(), "PeerBusy") {
				status = "busy"
			}
			log.Printf("[client] 从 %s 下载块 %d 未成功(%s)，转次选: %v", targetAddr, i, status, err)
			_ = g.ReportPeer(g.selfUUID, src.UUID, 0, status)
			continue
		}
		// 速度 = 块比特数 / 1e6 / 秒 = Mbps（浮点，避免整除截断为 0）
		if dur := time.Since(tBegin).Seconds(); dur > 0 {
			speedMbps = int64(float64(_const.ChunkSize) * 8 / 1e6 / dur)
		}
		_ = g.ReportPeer(g.selfUUID, src.UUID, speedMbps, "done")
		return c2, speedMbps, targetAddr, true
	}
	return 0, 0, "", false
}

// ReportChunk 告知服务端，我是uuid 我已经拥有 第 i 块
func (g *GspSdk) ReportChunk(uuid string, i int64) error {
	conn, err := g.connPool.GetConn(g.srvAddr)
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer func() {
		if err != nil {
			conn.Close()
		} else {
			g.connPool.PutConn(g.srvAddr, conn)
		}
	}()
	reqG := model.ReportChunkReq{Index: i, Operate: "reportChunk", UUID: uuid}
	reqJson, _ := json.Marshal(reqG)
	if err = g.codec.EncodeTo(conn, gsp.TypeJSON, reqJson); err != nil {
		return err
	}
	return nil
}

// WantChunk 向服务端请求第i块。
// 服务端返回一个按期望吞吐降序排好的候选源列表（resp.Sources），供本地故障转移。
// want 为期望返回的候选数（普通=1，endgame>1）。
func (g *GspSdk) WantChunk(i int64, want int) (*model.WantChunkResp, error) {
	conn, err := g.connPool.GetConn(g.srvAddr)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer func() {
		if err != nil {
			conn.Close()
		} else {
			g.connPool.PutConn(g.srvAddr, conn)
		}
	}()
	reqG := model.WantChunkReq{Index: i, Operate: "wantChunk", Zone: g.zone, UUID: g.selfUUID, Want: want}
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
// onControl（可空）用于接收中心经此连接主动下发的控制指令（prefetch/backoff 等）。
func (g *GspSdk) PeerReg(peerPort int, uuid string, onControl func(model.ControlMsg)) error {
	// 直接拨号，不从连接池借，避免耗尽连接池供其他操作使用
	controlConn, err := net.DialTimeout("tcp", g.srvAddr, 5*time.Second)
	if err != nil {
		return err
	}
	codec := gsp.Codec{}
	jsonReq, _ := json.Marshal(model.PeerRegReq{
		Operate: "peerReg",
		Port:    strconv.Itoa(peerPort),
		UUID:    uuid,
		Zone:    g.zone,
	})
	if err := codec.EncodeTo(controlConn, gsp.TypeJSON, jsonReq); err != nil {
		controlConn.Close()
		return err
	}
	// 持有控制连接直到 Tracker 关闭：既保活，又接收中心下发的控制指令。
	go func() {
		defer controlConn.Close()
		buf := make([]byte, 64*(1<<10))
		for {
			pkt, err := codec.Decode(controlConn, buf)
			if err != nil {
				log.Println("[client] 与分发服务端控制连接断开")
				return
			}
			if pkt.Type != gsp.TypeJSON || onControl == nil {
				continue
			}
			var msg model.ControlMsg
			if json.Unmarshal(pkt.Payload, &msg) == nil {
				// 异步分发：prefetch 可能耗时（下载 4MB），不阻塞控制通道读取。
				go onControl(msg)
			}
		}
	}()
	return nil
}

// ReportPeer 向服务端发送Peer信息，包括提供下载的对端UUID和本次状态
func (g *GspSdk) ReportPeer(uuid string, providerUuid string, speed int64, status string) error {
	conn, err := g.connPool.GetConn(g.srvAddr)
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer func() {
		if err != nil {
			conn.Close()
		} else {
			g.connPool.PutConn(g.srvAddr, conn)
		}
	}()
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
