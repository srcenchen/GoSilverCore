package gsp

import (
	"encoding/binary"
	"io"
)

// codec gsp 协议的编解码模块

type Codec struct {
	IReader io.Reader
}

// Decode GSP 数据解码
func (c *Codec) Decode() (*Packet, error) {
	r := c.IReader
	// 获取帧头
	header := make([]byte, 5)
	_, err := io.ReadFull(r, header)
	if err != nil {
		return nil, err
	}
	dataLen := binary.LittleEndian.Uint32(header[1:])
	// 读取 Payload
	payload := make([]byte, dataLen)
	_, err = io.ReadFull(r, payload)
	if err != nil {
		return nil, err
	}
	return &Packet{
		Type:    header[0],
		Length:  dataLen,
		Payload: payload,
	}, nil
}

// Encode GSP 数据编码
func (c *Codec) Encode(typ uint8, payload []byte) []byte {
	// 编码帧头
	header := make([]byte, 5)
	header[0] = typ
	binary.LittleEndian.PutUint32(header[1:], uint32(len(payload)))
	data := append(header, payload...)
	return data
}
