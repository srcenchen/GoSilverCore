package gsp

// GSP 协议是基于 TCP 传输层协议 用于 GoSilver 传输控制信息与文件数据的协议

const (
	TypeJSON      uint8 = 0x01 // JSON 控制类型
	TypeFileChunk uint8 = 0x02 // 数据块
)

// Packet GSP 数据包
type Packet struct {
	Type    uint8
	Length  uint32
	Payload []byte
}

// Peer 对端，也可以认为是连接状态
// 包含连接ip、端口信息。正在连接的设备数
type Peer struct {
	Addr    string
	ConnNum int
}

// TransferSession GSP 协议的连接会话
type TransferSession struct {
	BlockOwners map[int64][]*Peer // BlockOwners[1] 就可以获取到拥有第一块的设备列表
}
