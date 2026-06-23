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
	Index    int64  `json:"index"`         // 申请指定的片
	Status   bool   `json:"status"`        // 申请的片的状态
	CheckSum uint32 `json:"checkSum"`      // 申请的片的哈希校验值
	Msg      string `json:"msg,omitempty"` // 错误或状态附加信息，如 PeerBusy
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
	Zone    string `json:"zone"`  // 请求方所在机房（可空，空则由中心按 IP 推断）
	UUID    string `json:"uuid"`  // 请求方自身 UUID
	Want    int    `json:"want"`  // 期望返回的候选源数量（普通=1，endgame>1）
}

// SourceCandidate 是调度中心为某个块挑选出的一个候选数据源。
// 客户端按列表顺序（最优在前）尝试，首选失败立即转次选，实现本地故障转移。
type SourceCandidate struct {
	Addr string `json:"addr"` // 候选源地址 ip:port；空字符串表示中心服务端
	UUID string `json:"uuid"` // 候选源 UUID（供客户端 ReportPeer 归因）
}

type WantChunkResp struct {
	Index    int64             `json:"index"`    // 申请指定的片
	CheckSum uint32            `json:"checkSum"` // 申请的片的哈希校验值
	Sources  []SourceCandidate `json:"sources"`  // 候选源列表（已按期望吞吐降序排好）
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
	Zone    string `json:"zone"` // 节点所在机房标签（可空，空则中心按 IP 推断）
}

// ControlMsg 是中心经 PeerReg 长连接主动下发给节点的控制指令（统一指挥）。
type ControlMsg struct {
	Cmd   string `json:"cmd"`             // "prefetch" | "backoff" | "keepSeeding"
	Index int64  `json:"index,omitempty"` // prefetch 目标块号
	Until int64  `json:"until,omitempty"` // backoff 截止 Unix 秒
}

type PeerReportReq struct {
	Operate      string `json:"operate"`
	UUID         string `json:"uuid"`
	ProviderUUID string `json:"providerUuid"`
	Status       string `json:"status"`
	Speed        int64  `json:"speed"`
}
