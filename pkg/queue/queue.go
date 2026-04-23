package queue

import "go-silver-core/pkg/gsp"

// DownloadQueue 下载队列

type DownloadQueue interface {
	want(int64, string)
}

type downloadQueue struct {
	transferSession gsp.TransferSession // 这里我提供了TransferSession，可以在这里根据块的index去获取相关的peer,有此块的设备信息
}

func NewDownloadQueue(transferSession gsp.TransferSession) DownloadQueue {
	return downloadQueue{transferSession: transferSession}
}

// want 当有人要加入队列，此函数就会被调用
// 第一个参数是块的序号，从0开始。第二个是设备id。
func (d downloadQueue) want(i int64, s string) {
	//TODO implement me
	panic("implement me")
}

// send 将获取到的设备信息发送给对应的设备
// @yyy 这个地方我来写
// 第一个参数：文件块序号
// 第二个参数：设备Id
// 第三个参数：在transferSession中获取的address
func (d downloadQueue) send(i int64, deviceId, address string) {
	
}
