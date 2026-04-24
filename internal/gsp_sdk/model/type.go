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
