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
	AddPeer(uuid string, addr string)
	UpdatePeer(providerUuid string, speed int64)
	IsMain() bool
	AcquireUploadSlot() error
	ReleaseUploadSlot()
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
	q.Want(wc.Index, conn)
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
	resp, _ := json.Marshal(model.GetChunkResp{Index: gc.Index, Status: has, CheckSum: checkSum})
	codec := gsp.Codec{}
	if err := codec.EncodeTo(conn, gsp.TypeJSON, resp); err != nil || !has {
		fmt.Println(err)
		tool.CloseConn(conn)
		return
	}
	
	// 如果是发送主机（主节点），需要限流控制以防止局域网高并发冲垮磁盘或网络带宽
	if tool.IsMain() {
		if err := tool.AcquireUploadSlot(); err != nil {
			tool.CloseConn(conn)
			return
		}
		defer tool.ReleaseUploadSlot()
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
	tool.AddPeer(wc.UUID, strings.Split(conn.RemoteAddr().String(), ":")[0]+":"+wc.Port)
	codec := gsp.Codec{}
	buf := tool.GetMemPool().Get(1)
	defer tool.GetMemPool().Put(buf)
	_, err = codec.Decode(conn, *buf)
	if err != nil {
		tool.RemovePeer(wc.UUID)
		slog.Info("对端下线，尝试清理:" + strings.Split(conn.RemoteAddr().String(), ":")[0] + ":" + wc.Port)
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
		tool.UpdatePeer(wc.ProviderUUID, wc.Speed)
	}
}
