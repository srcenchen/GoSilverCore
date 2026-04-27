package gsp

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// codec gsp 协议的编解码模块

type Codec struct {
}

// Decode GSP 数据解码
func (c *Codec) Decode(r io.Reader, payloadBuf []byte) (*Packet, error) {
	// 获取帧头
	header := [5]byte{}
	_, err := io.ReadFull(r, header[:])
	if err != nil {
		return nil, err
	}
	dataLen := binary.LittleEndian.Uint32(header[1:])
	// 读取 Payload
	if uint32(cap(payloadBuf)) < dataLen {
		return nil, fmt.Errorf("packet payload size (%d) exceeds buffer capacity (%d)", dataLen, cap(payloadBuf))
	}
	_, err = io.ReadFull(r, payloadBuf[:dataLen])
	if err != nil {
		return nil, err
	}
	return &Packet{
		Type:    header[0],
		Length:  dataLen,
		Payload: payloadBuf[:dataLen],
	}, nil
}

// EncodeTo GSP 数据编码
func (c *Codec) EncodeTo(conn net.Conn, typ uint8, payload []byte) error {
	// 编码帧头
	header := [5]byte{}
	header[0] = typ
	binary.LittleEndian.PutUint32(header[1:], uint32(len(payload)))
	buf := net.Buffers{
		header[:],
		payload,
	}
	_, err := buf.WriteTo(conn)
	return err
}
