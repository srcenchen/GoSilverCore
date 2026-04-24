package handle

import (
	"encoding/json"
	"go-silver-core/internal/chunk"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/pkg/mempool"
	"net"
)

// sender 发送方处理接收到的数据，进行对应的操作

type ToolSession interface {
	IndexValid(int64) (bool, uint32)
	ReadChunk(i int64, buf []byte) (int, error)
	CloseConn(conn net.Conn)
	GetChunk() chunk.FileChunk
	GetMemPool() *mempool.MemPool
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
	data = codec.Encode(gsp.TypeJSON, resp)
	conn.Write(data)
}

// WantChunk 想要这个 chunk
func WantChunk(conn net.Conn, data []byte, tool ToolSession) {

}

// ReportChunkStatus 上报自己拥有了这个块
func ReportChunkStatus(conn net.Conn, data []byte, tool ToolSession) {

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
	respData := codec.Encode(gsp.TypeJSON, resp)
	if _, err := conn.Write(respData); err != nil || !has {
		tool.CloseConn(conn)
		return
	}
	// 发送回应结束，开始发送数据块
	// 借用
	mp := tool.GetMemPool()
	fileChunk := mp.Get(ck.GetChunkSize())
	n, err := tool.ReadChunk(gc.Index, *fileChunk)
	if err != nil {
		tool.CloseConn(conn)
		return
	}
	respData = codec.Encode(gsp.TypeFileChunk, (*fileChunk)[:n])
	if _, err = conn.Write(respData); err != nil {
		tool.CloseConn(conn)
		return
	}
}
