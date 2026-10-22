package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go-silver-core/internal/chunk"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"hash/crc32"
	"net"
	"strconv"
	"time"
)

const ioTimeout = 20 * time.Second

func (g *GspSdk) borrow(addr string) (net.Conn, func(bool), error) {
	conn, err := g.connPool.GetConnContext(g.ctx, addr)
	if err != nil {
		return nil, nil, err
	}
	deadline := time.Now().Add(ioTimeout)
	if d, ok := g.ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(g.ctx, func() { _ = conn.Close() })
	return conn, func(ok bool) {
		stop()
		if ok {
			g.connPool.PutConn(addr, conn)
		} else {
			g.connPool.Discard(conn)
		}
	}, nil
}
func (g *GspSdk) exchange(addr string, req, out any) (err error) {
	conn, finish, err := g.borrow(addr)
	if err != nil {
		return err
	}
	ok := false
	defer func() { finish(ok) }()
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if err = g.codec.EncodeTo(conn, gsp.TypeJSON, payload); err != nil {
		return err
	}
	if out != nil {
		buf := make([]byte, 64<<10)
		packet, e := g.codec.DecodeBounded(conn, buf, 4<<20)
		if e != nil {
			return e
		}
		if packet.Type != gsp.TypeJSON {
			return gsp.ErrInvalidType
		}
		if err = json.Unmarshal(packet.Payload, out); err != nil {
			return err
		}
	}
	ok = true
	return nil
}
func (g *GspSdk) GetFileStatus() (r model.GetFileStatusResp, err error) {
	err = g.exchange(g.srvAddr, model.BaseJson{Operate: "getFileStatus"}, &r)
	if err != nil {
		return
	}
	if r.Error != "" {
		return r, errors.New(r.Error)
	}
	if r.FileName == "" || r.FileSize < 0 || r.ChunkSize != _const.ChunkSize || r.ChunkNum != (r.FileSize+r.ChunkSize-1)/r.ChunkSize {
		err = errors.New("invalid file metadata (incompatible sender or source not ready)")
	}
	return
}

// FetchChunk verifies and writes directly from the pooled buffer, without a full-chunk copy.
func (g *GspSdk) FetchChunk(addr string, i int64, ck *chunk.FileChunk) (n int, checksum uint32, err error) {
	return g.fetch(addr, i, ck, nil)
}

// GetChunk preserves the SDK's byte-returning API; the download engine uses FetchChunk.
func (g *GspSdk) GetChunk(addr string, i int64, ck *chunk.FileChunk) (data []byte, checksum uint32, err error) {
	_, checksum, err = g.fetch(addr, i, ck, &data)
	return
}
func (g *GspSdk) fetch(addr string, i int64, ck *chunk.FileChunk, copyTo *[]byte) (n int, checksum uint32, err error) {
	conn, finish, err := g.borrow(addr)
	if err != nil {
		return 0, 0, err
	}
	ok := false
	defer func() { finish(ok) }()
	req, _ := json.Marshal(model.GetChunkReq{Operate: "getChunk", Index: i})
	if err = g.codec.EncodeTo(conn, gsp.TypeJSON, req); err != nil {
		return
	}
	var header [4096]byte
	resp, err := g.codec.Decode(conn, header[:])
	if err != nil {
		return
	}
	if resp.Type != gsp.TypeJSON {
		return 0, 0, gsp.ErrInvalidType
	}
	var info model.GetChunkResp
	if err = json.Unmarshal(resp.Payload, &info); err != nil {
		return
	}
	if info.Error != "" {
		return 0, 0, errors.New(info.Error)
	}
	if !info.Status || info.Index != i {
		return 0, 0, errors.New("peer returned an unavailable or different chunk")
	}
	buf := g.memPool.Get(_const.ChunkSize)
	defer g.memPool.Put(buf)
	resp, err = g.codec.Decode(conn, *buf)
	if err != nil {
		return
	}
	if resp.Type != gsp.TypeFileChunk {
		return 0, 0, gsp.ErrInvalidType
	}
	checksum = crc32.ChecksumIEEE(resp.Payload)
	if checksum != info.CheckSum {
		return 0, 0, errors.New("chunk CRC32 mismatch")
	}
	if err = ck.Save(i, resp.Payload); err != nil {
		return
	}
	if copyTo != nil {
		*copyTo = append([]byte(nil), resp.Payload...)
	}
	ok = true
	return len(resp.Payload), checksum, nil
}
func (g *GspSdk) ReportChunk(id string, i int64) error {
	return g.exchange(g.srvAddr, model.ReportChunkReq{Operate: "reportChunk", UUID: id, Index: i}, nil)
}
func (g *GspSdk) WantChunk(i int64) (*model.WantChunkResp, error) {
	var out model.WantChunkResp
	g.control.mu.Lock()
	id := g.control.peerID
	g.control.mu.Unlock()
	err := g.exchange(g.srvAddr, model.WantChunkReq{Operate: "wantChunk", Index: i, UUID: id}, &out)
	if err == nil && out.Index != i {
		err = errors.New("tracker returned a different index")
	}
	return &out, err
}
func (g *GspSdk) PeerReg(port int, id string) error {
	conn, err := (&net.Dialer{Timeout: 4 * time.Second, KeepAlive: 15 * time.Second}).DialContext(g.ctx, "tcp", g.srvAddr)
	if err != nil {
		return err
	}
	stopCancel := context.AfterFunc(g.ctx, func() { _ = conn.Close() })
	defer stopCancel()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	payload, _ := json.Marshal(model.PeerRegReq{Operate: "peerReg", Port: strconv.Itoa(port), UUID: id})
	if err = g.codec.EncodeTo(conn, gsp.TypeJSON, payload); err != nil {
		conn.Close()
		return err
	}
	var buf [1024]byte
	packet, err := g.codec.Decode(conn, buf[:])
	if err != nil {
		conn.Close()
		return fmt.Errorf("peer registration: %w", err)
	}
	var ack struct {
		Ready bool `json:"ready"`
	}
	if packet.Type != gsp.TypeJSON || json.Unmarshal(packet.Payload, &ack) != nil || !ack.Ready {
		conn.Close()
		return errors.New("peer registration refused")
	}
	_ = conn.SetDeadline(time.Time{})
	g.control.mu.Lock()
	defer g.control.mu.Unlock()
	if g.control.conn != nil {
		g.control.conn.Close()
	}
	g.control.conn, g.control.peerID = conn, id
	return nil
}
func (g *GspSdk) Watch(onPush func(model.PushStart), onDown func()) {
	g.control.mu.Lock()
	conn := g.control.conn
	g.control.mu.Unlock()
	if conn == nil {
		if onDown != nil {
			onDown()
		}
		return
	}
	defer func() {
		if onDown != nil {
			onDown()
		}
	}()
	var buf [4096]byte
	for {
		packet, err := g.codec.Decode(conn, buf[:])
		if err != nil {
			return
		}
		var push model.PushStart
		if packet.Type == gsp.TypeJSON && json.Unmarshal(packet.Payload, &push) == nil && push.Operate == "pushStart" && onPush != nil {
			onPush(push)
		}
	}
}
func (g *GspSdk) ReportPeer(id, provider string, speed int64, status string, index int64) error {
	return g.exchange(g.srvAddr, model.PeerReportReq{Operate: "reportPeer", UUID: id, ProviderUUID: provider, Speed: speed, Status: status, Index: index}, nil)
}
func (g *GspSdk) Call(method, token string, payload, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var response struct {
		Value json.RawMessage `json:"value"`
		Error string          `json:"error"`
	}
	if err = g.exchange(g.srvAddr, model.ControlRequest{Operate: "control", Method: method, Token: token, Payload: raw}, &response); err != nil {
		return err
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	if out != nil {
		return json.Unmarshal(response.Value, out)
	}
	return nil
}

func (g *GspSdk) GetNodeStatus() (r model.NodeStatus, err error) {
	err = g.exchange(g.srvAddr, model.BaseJson{Operate: "getNodeStatus"}, &r)
	if err == nil && (!r.Sender || r.UUID == "") {
		err = errors.New("对端不是发送服务")
	}
	return
}
