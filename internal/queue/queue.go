package queue

import (
	"net"
)

// DownloadQueue 下载队列（调度器）接口。
// 实现见 internal/gsp_sdk/server/queue.go 的 queue2。
type DownloadQueue interface {
	// Want 当有接收端申请某个块时被调用。
	// 第一个参数是块序号（从 0 开始），第二个是接收端连接实体。
	Want(i int64, conn net.Conn)
}
