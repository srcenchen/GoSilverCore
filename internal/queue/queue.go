package queue

import (
	"go-silver-core/internal/gsp_sdk/model"
	"net"
)

// DownloadQueue 下载队列（调度器）接口。
// 实现见 internal/gsp_sdk/server/queue.go 的 queue2。
type DownloadQueue interface {
	// Want 当有接收端申请某个块时被调用。
	// req 携带块序号、请求方机房/UUID 与期望候选数；conn 是接收端连接，
	// 调度结果（候选源列表）通过该连接写回。
	Want(req model.WantChunkReq, conn net.Conn)
}
