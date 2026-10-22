package model

import "encoding/json"

// 定义相关JSON结构

// BaseJson 最基本的JSON，用于解析出 Operate
type BaseJson struct {
	Operate string `json:"operate"` // 操作类型
}

type GetChunkReq struct {
	Operate string `json:"operate"`
	Index   int64  `json:"index"` // 申请指定的片
}

type GetChunkResp struct {
	Error    string `json:"error,omitempty"`
	Index    int64  `json:"index"`    // 申请指定的片
	Status   bool   `json:"status"`   // 申请的片的状态
	CheckSum uint32 `json:"checkSum"` // 申请的片的哈希校验值
}

type GetFileStatusResp struct {
	Error     string `json:"error,omitempty"`
	FileName  string `json:"fileName"`
	FileSize  int64  `json:"fileSize"`
	ChunkSize int64  `json:"chunkSize"`
	ChunkNum  int64  `json:"chunkNum"`
}

type WantChunkReq struct {
	UUID    string `json:"uuid"`
	Operate string `json:"operate"`
	Index   int64  `json:"index"` // 申请指定的片
}

type WantChunkResp struct {
	Index    int64  `json:"index"`    // 申请指定的片
	Addr     string `json:"addr"`     // 申请的片的Peer地址
	CheckSum uint32 `json:"checkSum"` // 申请的片的哈希校验值
	UUID     string `json:"uuid"`     // 对端的UUID信息
}
type ReportChunkReq struct {
	Operate string `json:"operate"`
	Index   int64  `json:"index"` // 告知指定的片
	UUID    string `json:"uuid"`
}

type PeerRegReq struct {
	Operate string `json:"operate"`
	Port    string `json:"port"`
	UUID    string `json:"uuid"`
}

type PushStart struct {
	ChunkSize int64  `json:"chunkSize"`
	Operate   string `json:"operate"`
	FileName  string `json:"fileName"`
	FileSize  int64  `json:"fileSize"`
	ChunkNum  int64  `json:"chunkNum"`
}

type PeerInfo struct {
	UUID     string  `json:"uuid"`
	Addr     string  `json:"addr"`
	Chunks   int     `json:"chunks"`
	Total    int     `json:"total"`
	Speed    float64 `json:"speed"`
	Inflight int     `json:"inflight"`
	Failed   int     `json:"failed"`
	Self     bool    `json:"self"`
	Online   bool    `json:"online"`
}

type PeerReportReq struct {
	Operate      string `json:"operate"`
	UUID         string `json:"uuid"`
	ProviderUUID string `json:"providerUuid"`
	Status       string `json:"status"`
	Speed        int64  `json:"speed"`
	Index        int64  `json:"index"`
}

// ControlRequest uses the peer listener for authenticated receiver management.
type ControlRequest struct {
	Operate string          `json:"operate"`
	Method  string          `json:"method"`
	Token   string          `json:"token"`
	Payload json.RawMessage `json:"payload"`
}
type ControlResponse struct {
	Value any    `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

// NodeStatus describes the sender independently of a prepared file.
type NodeStatus struct {
	UUID    string `json:"uuid"`
	Sender  bool   `json:"sender"`
	HasFile bool   `json:"hasFile"`
}
