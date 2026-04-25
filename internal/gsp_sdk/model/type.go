package model

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
	Index    int64  `json:"index"`    // 申请指定的片
	Status   bool   `json:"status"`   // 申请的片的状态
	CheckSum uint32 `json:"checkSum"` // 申请的片的哈希校验值
}

type GetFileStatusResp struct {
	FileName  string `json:"fileName"`
	FileSize  int64  `json:"fileSize"`
	ChunkSize int64  `json:"chunkSize"`
	ChunkNum  int64  `json:"chunkNum"`
}

type WantChunkReq struct {
	Operate string `json:"operate"`
	Index   int64  `json:"index"` // 申请指定的片
}

type WantChunkResp struct {
	Index    int64  `json:"index"`    // 申请指定的片
	Addr     string `json:"addr"`     // 申请的片的Peer地址
	CheckSum uint32 `json:"checkSum"` // 申请的片的哈希校验值
}
type ReportChunkReq struct {
	Operate string `json:"operate"`
	Index   int64  `json:"index"` // 告知指定的片
	Port    string `json:"port"`
}
