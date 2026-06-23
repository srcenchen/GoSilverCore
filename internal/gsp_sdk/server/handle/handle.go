package handle

import (
	"encoding/json"
	"fmt"
	"go-silver-core/internal/chunk"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/internal/queue"
	"go-silver-core/pkg/mempool"
	"log/slog"
	"net"
	"strings"
	"time"
)

// sender 发送方处理接收到的数据，进行对应的操作

// ToolSession Session的一些工具链
type ToolSession interface {
	IndexValid(int64) (bool, uint32)
	ReadChunk(i int64, buf []byte) (int, error)
	CloseConn(conn net.Conn)
	GetChunk() chunk.FileChunk
	GetMemPool() *mempool.MemPool
	GetQueue() queue.DownloadQueue
	AddBlockOwner(i int64, uuid string)
	RemovePeer(addr string)
	AddPeer(uuid string, addr string, zone string)
	UpdatePeer(providerUuid string, speed int64, status string)
	IsMain() bool
	AcquireUpload(timeout time.Duration) bool
	ReleaseUpload()
	SetControlConn(uuid string, conn net.Conn)
}

// GetFileStatus 获取文件信息
func GetFileStatus(conn net.Conn, data []byte, tool ToolSession) {
	ck := tool.GetChunk()
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
	q.Want(wc, conn)
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
	// 首先，我们要确认我们拥有这个块，并且块合法
	has, checkSum := tool.IndexValid(gc.Index)
	if !has {
		resp, _ := json.Marshal(model.GetChunkResp{Index: gc.Index, Status: false, Msg: "ChunkNotFound"})
		codec := gsp.Codec{}
		codec.EncodeTo(conn, gsp.TypeJSON, resp)
		tool.CloseConn(conn)
		return
	}

	// 上传并发限流：抢一个名额（短等待）。抢不到说明本节点正忙，
	// 回 PeerBusy 让客户端转向次选源，避免把 HDD/慢源拉爆。
	if !tool.AcquireUpload(2 * time.Second) {
		resp, _ := json.Marshal(model.GetChunkResp{Index: gc.Index, Status: false, Msg: "PeerBusy"})
		codec := gsp.Codec{}
		codec.EncodeTo(conn, gsp.TypeJSON, resp)
		tool.CloseConn(conn)
		return
	}
	defer tool.ReleaseUpload()

	// 发送回应，表示可以提供分块
	resp, _ := json.Marshal(model.GetChunkResp{Index: gc.Index, Status: true, CheckSum: checkSum})
	codec := gsp.Codec{}
	if err := codec.EncodeTo(conn, gsp.TypeJSON, resp); err != nil {
		fmt.Println(err)
		tool.CloseConn(conn)
		return
	}

	// 发送回应结束，开始发送数据块
	mp := tool.GetMemPool()
	fileChunk := mp.Get(ck.GetChunkSize())
	defer mp.Put(fileChunk)
	n, err := tool.ReadChunk(gc.Index, *fileChunk)
	if err != nil {
		tool.CloseConn(conn)
		return
	}
	// 文件块数据应当以 TypeFileChunk (0x02) 发送
	if err = codec.EncodeTo(conn, gsp.TypeFileChunk, (*fileChunk)[:n]); err != nil {
		tool.CloseConn(conn)
		return
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
	slog.Info("对端注册")
	tool.AddPeer(wc.UUID, strings.Split(conn.RemoteAddr().String(), ":")[0]+":"+wc.Port, wc.Zone)
	// 记录控制长连接，中心据此主动下发指令（prefetch/backoff）。
	tool.SetControlConn(wc.UUID, conn)

	// 持续读以检测对端下线。用小缓冲（不再借用 4MB 内存池，修复内存池被长连接钉死的隐患）。
	codec := gsp.Codec{}
	buf := make([]byte, 256)
	for {
		if _, err = codec.Decode(conn, buf); err != nil {
			tool.RemovePeer(wc.UUID)
			slog.Info("对端下线，尝试清理:" + strings.Split(conn.RemoteAddr().String(), ":")[0] + ":" + wc.Port)
			return
		}
	}
}

// PeerReport 对端信息上报
func PeerReport(conn net.Conn, data []byte, tool ToolSession) {
	var wc model.PeerReportReq
	err := json.Unmarshal(data, &wc)
	if err != nil {
		tool.CloseConn(conn)
		return
	}
	slog.Info(fmt.Sprintf("对端状态返回：设备UUID: %s ProviderUUID: %s Speed: %d mb/s Status: %s", wc.UUID, wc.ProviderUUID, wc.Speed, wc.Status))
	if wc.ProviderUUID != "" {
		tool.UpdatePeer(wc.ProviderUUID, wc.Speed, wc.Status)
	}
}
