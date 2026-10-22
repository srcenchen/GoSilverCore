package handle

import (
	"encoding/json"
	"fmt"
	"go-silver-core/internal/chunk"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/internal/queue"
	"go-silver-core/pkg/mempool"
	"hash/crc32"
	"log/slog"
	"net"
	"strconv"
	"time"
)

// sender 发送方处理接收到的数据，进行对应的操作

// ToolSession Session的一些工具链
type ToolSession interface {
	IndexValid(int64) (bool, uint32)
	HasChunk(int64) bool
	ReadChunk(i int64, buf []byte) (int, error)
	CloseConn(conn net.Conn)
	GetChunk() chunk.FileChunk
	GetMemPool() *mempool.MemPool
	GetQueue() queue.DownloadQueue
	AddBlockOwner(i int64, uuid string)
	RemovePeer(addr string)
	AddPeer(uuid string, addr string, conn net.Conn)
	UpdatePeer(providerUuid string, speed int64, status string, index int64, requester string)
	IsMain() bool
	AcquireUploadSlot() error
	ReleaseUploadSlot()
}

// GetFileStatus 获取文件信息
func GetFileStatus(conn net.Conn, data []byte, tool ToolSession) {
	ck := tool.GetChunk()
	if ck.FileStat == nil {
		sendJSON(conn, model.GetFileStatusResp{Error: "发送端文件尚未就绪，请重新准备发送服务"})
		return
	}
	if tool.IsMain() && !ck.Unchanged() {
		sendJSON(conn, model.GetFileStatusResp{Error: "源文件在准备后已变化，请停止并重新准备发送服务"})
		return
	}
	resp, _ := json.Marshal(model.GetFileStatusResp{
		FileName:  ck.FileStat.Name(),
		FileSize:  ck.FileStat.Size(),
		ChunkSize: ck.GetChunkSize(),
		ChunkNum:  ck.GetChunkNum(),
	})
	codec := gsp.Codec{}
	codec.EncodeTo(conn, gsp.TypeJSON, resp)
}

// WantChunk 想要这个 chunk
func WantChunk(conn net.Conn, data []byte, tool ToolSession) {
	var wc model.WantChunkReq
	err := json.Unmarshal(data, &wc)
	if err != nil {
		tool.CloseConn(conn)
		return
	}
	q := tool.GetQueue()
	if fq, ok := q.(interface{ WantFor(int64, net.Conn, string) }); ok {
		fq.WantFor(wc.Index, conn, wc.UUID)
	} else {
		q.Want(wc.Index, conn)
	}
}

// ReportChunk 接收端上报自己拥有了这个块
func ReportChunk(conn net.Conn, data []byte, tool ToolSession) {
	var wc model.ReportChunkReq
	err := json.Unmarshal(data, &wc)
	if err != nil {
		tool.CloseConn(conn)
		return
	}
	tool.AddBlockOwner(wc.Index, wc.UUID)
}

// GetChunk 处理获取指定片的请求处理
// 当接收端发起这个请求，我们就需要开始发送这一个块
func GetChunk(conn net.Conn, data []byte, tool ToolSession) {
	ck := tool.GetChunk()
	var gc model.GetChunkReq
	err := json.Unmarshal(data, &gc)
	if err != nil {
		tool.CloseConn(conn)
		return
	}
	if !tool.HasChunk(gc.Index) {
		sendJSON(conn, model.GetChunkResp{Index: gc.Index, Error: "对端没有该分块"})
		return
	}
	if err := tool.AcquireUploadSlot(); err != nil {
		sendJSON(conn, model.GetChunkResp{Index: gc.Index, Error: err.Error()})
		return
	}
	defer tool.ReleaseUploadSlot()
	mp := tool.GetMemPool()
	buf := mp.Get(ck.GetChunkSize())
	defer mp.Put(buf)
	n, err := tool.ReadChunk(gc.Index, *buf)
	if err != nil {
		sendJSON(conn, model.GetChunkResp{Index: gc.Index, Error: err.Error()})
		return
	}
	resp, _ := json.Marshal(model.GetChunkResp{Index: gc.Index, Status: true, CheckSum: crc32.ChecksumIEEE((*buf)[:n])})
	codec := gsp.Codec{}
	_ = conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
	if codec.EncodeTo(conn, gsp.TypeJSON, resp) != nil || codec.EncodeTo(conn, gsp.TypeFileChunk, (*buf)[:n]) != nil {
		tool.CloseConn(conn)
	}

}

// PeerReg 对端注册
func PeerReg(conn net.Conn, data []byte, tool ToolSession) {
	var wc model.PeerRegReq
	err := json.Unmarshal(data, &wc)
	if err != nil {
		tool.CloseConn(conn)
		return
	}
	port, e := strconv.Atoi(wc.Port)
	if e != nil || port < 1 || port > 65535 || wc.UUID == "" {
		tool.CloseConn(conn)
		return
	}
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		tool.CloseConn(conn)
		return
	}
	codec := gsp.Codec{}
	// Acknowledge and watch the SAME connection used for registration.
	if codec.EncodeTo(conn, gsp.TypeJSON, []byte(`{"ready":true}`)) != nil {
		tool.CloseConn(conn)
		return
	}
	tool.AddPeer(wc.UUID, net.JoinHostPort(host, wc.Port), conn)
	defer tool.RemovePeer(wc.UUID)
	_ = conn.SetReadDeadline(time.Time{})
	var one [1]byte
	_, _ = conn.Read(one[:])
	tool.CloseConn(conn)

}

// PeerReport 对端信息上报
func PeerReport(conn net.Conn, data []byte, tool ToolSession) {
	var wc model.PeerReportReq
	err := json.Unmarshal(data, &wc)
	if err != nil {
		tool.CloseConn(conn)
		return
	}
	slog.Debug(fmt.Sprintf("对端状态返回：设备UUID: %s ProviderUUID: %s Speed: %d mb/s Status: %s", wc.UUID, wc.ProviderUUID, wc.Speed, wc.Status))
	if wc.ProviderUUID != "" {
		tool.UpdatePeer(wc.ProviderUUID, wc.Speed, wc.Status, wc.Index, wc.UUID)
	}
}

func sendJSON(conn net.Conn, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = (&gsp.Codec{}).EncodeTo(conn, gsp.TypeJSON, payload)
}
