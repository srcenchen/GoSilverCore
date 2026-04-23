package queue

import (
	"go-silver-core/internal/gsp"
	"net"
)

// DownloadQueue 下载队列

type DownloadQueue interface {
	want(i int64, conn net.Conn)
}

type downloadQueue struct {
	session gsp.Session // 这里我提供了Session，可以在这里根据块的index去获取相关的peer,有此块的设备信息
}

func NewDownloadQueue(session gsp.Session) DownloadQueue {
	return downloadQueue{session: session}
}

// want 当有人要加入队列，此函数就会被调用
// 第一个参数是块的序号，从0开始。第二个是连接的实体。
func (d downloadQueue) want(i int64, conn net.Conn) {
	//TODO implement me
	panic("implement me")
}

// send 将获取到的设备信息发送给对应的设备
// @yyy 这个地方我来写
// 第一个参数：文件块序号
// 第二个参数：接收者的连接实体
// 第三个参数：在 session.ChunkBlockOwner 中获取的address
func (d downloadQueue) send(i int64, receiver net.Conn, address string) {

}
