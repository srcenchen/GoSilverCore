package gsp

import (
	"errors"
	"fmt"
	"go-silver-core/internal/gsp_sdk"
	"log/slog"
	"net"
)

type Peer struct {
	conn    net.Conn // 连接实例
	connNum int      // 连接数
}

// Session 这里是发送端的Session
// 但每个节点都算一个发送端的，所以都会配备一个Session
type Session struct {
	lis             net.Listener
	addr            string
	conn            map[string]net.Conn
	ChunkBlockOwner map[int64][]Peer
}

func NewGspSession(addr string) *Session {
	return &Session{addr: addr}
}

func (s *Session) Start() error {
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.lis = lis
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				slog.Error("与接收端建立连接失败")
			}
			go s.handle(conn)
		}
	}()
	return nil
}

func (s *Session) handle(conn net.Conn) {
	addr := conn.RemoteAddr()
	s.conn[addr.String()] = conn
	defer s.closeConn(conn)
	for {
		codec := Codec{IReader: conn}
		packet, err := codec.Decode()
		if err != nil {
			slog.Error(fmt.Sprintf("接收端 %s 发生错误，即将断开连接 %s. "))
			s.closeConn(conn)
		}
		if err := s.parsePacket(conn, packet); err != nil {
			slog.Error(fmt.Sprintf("接收端 %s 发生错误，即将断开连接 %s. "))
			s.closeConn(conn)
		}
	}
}

func (s *Session) parsePacket(conn net.Conn, packet *Packet) error {
	if packet.Type != TypeJSON {
		return errors.New("接收到非法的PacketType")
	}
	if gsp_sdk.SenderOperation(conn, packet.Payload) != nil {
		return errors.New("接收到无法解析的指令")
	}
	return nil
}

// closeConn 关闭连接
func (s *Session) closeConn(conn net.Conn) {
	_ = conn.Close()
	delete(s.conn, conn.RemoteAddr().String())
}
