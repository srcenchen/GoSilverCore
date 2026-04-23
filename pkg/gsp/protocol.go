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
