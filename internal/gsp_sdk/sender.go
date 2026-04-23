package gsp_sdk

import (
	"encoding/json"
	"net"
)

// sender 发送方处理接收到的数据，进行对应的操作

type getChunk struct {
	Index int64 `json:"index"` // 申请指定的片
}

// GetChunkHandler 获取指定片的请求处理
func GetChunkHandler(conn net.Conn, data []byte) {
	var gc getChunk
	err := json.Unmarshal(data, &gc)
	if err != nil {
		return
	}

}
