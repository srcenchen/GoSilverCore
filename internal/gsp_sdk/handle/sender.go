package handle

import (
	"encoding/json"
	"go-silver-core/internal/gsp"
	"net"
)

// sender 发送方处理接收到的数据，进行对应的操作
type getChunkReq struct {
	Index int64 `json:"index"` // 申请指定的片
}
type getChunkResp struct {
	Index    int64  `json:"index"`    // 申请指定的片
	Status   bool   `json:"status"`   // 申请的片的状态
	CheckSum uint32 `json:"checkSum"` // 申请的片的哈希校验值
}

type ToolSession interface {
	IndexValid(int64) (bool, uint32)
	ReadChunk(int64) ([]byte, error)
	CloseConn(conn net.Conn)
}

// GetChunk 处理获取指定片的请求处理
// 当接收端发起这个请求，我们就需要开始发送这一个块
func GetChunk(conn net.Conn, data []byte, tool ToolSession) {
	var gc getChunkReq
	err := json.Unmarshal(data, &gc)
	if err != nil {
		tool.CloseConn(conn)
		return
	}
	// 首先，我们要确认我们拥有这个块，并且块合法
	has, checkSum := tool.IndexValid(gc.Index)
	resp, _ := json.Marshal(getChunkResp{Index: gc.Index, Status: has, CheckSum: checkSum})
	codec := gsp.Codec{}
	respData := codec.Encode(gsp.TypeJSON, resp)
	if _, err := conn.Write(respData); err != nil || !has {
		tool.CloseConn(conn)
		return
	}
	// 发送回应结束，开始发送数据块
	fileChunk, err := tool.ReadChunk(gc.Index)
	if err != nil {
		tool.CloseConn(conn)
		return
	}
	respData = codec.Encode(gsp.TypeFileChunk, fileChunk)
	if _, err = conn.Write(respData); err != nil {
		tool.CloseConn(conn)
		return
	}
}
