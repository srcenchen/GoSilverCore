package gsp_sdk

import (
	"errors"
	"fmt"
	"go-silver-core/internal/gsp"
	"go-silver-core/pkg/chunk"
	"log/slog"
	"net"
	"os"
)

type Peer struct {
	connAddr string // 连接地址
	connNum  int    // 连接数
}

// Session 这里是发送端的Session
// 但每个节点都算一个发送端的，所以都会配备一个Session
type Session struct {
	lis             net.Listener
	addr            string
	conn            map[string]net.Conn
	ChunkBlockOwner map[int64][]*Peer
	chunkHas        map[int64]uint32 // 用于判断是否拥有这个块，value是他的哈希值
	chunkProvider   chunk.FileChunk  // chunk块
}

// ReadChunk 获取Chunk块
func (s *Session) ReadChunk(i int64) ([]byte, error) {
	return s.chunkProvider.ReadChunk(i)
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

// BeSendMain 作为发送主机
func (s *Session) BeSendMain(f *os.File) error {
	ck := chunk.NewFileChunk(f)
	n := ck.GetChunkNum()
	for i := int64(0); i < n; i++ {
		cs, err := ck.CheckSum(i)
		if err != nil {
			return err
		}
		s.chunkHas[i] = cs
		peer := &Peer{connAddr: "", connNum: 0}
		s.ChunkBlockOwner[i] = append(s.ChunkBlockOwner[i], peer)
	}
	return nil
}

// handle 处理接收端的连接
func (s *Session) handle(conn net.Conn) {
	addr := conn.RemoteAddr()
	s.conn[addr.String()] = conn
	defer s.CloseConn(conn)
	for {
		codec := gsp.Codec{}
		packet, err := codec.Decode(conn)
		if err != nil {
			slog.Error(fmt.Sprintf("接收端 %s 发生错误，即将断开连接 %s. "))
			s.CloseConn(conn)
		}
		if err := s.parsePacket(conn, packet); err != nil {
			slog.Error(fmt.Sprintf("接收端 %s 发生错误，即将断开连接 %s. "))
			s.CloseConn(conn)
		}
	}
}

// parsePacket 解析接收端发出的信息
func (s *Session) parsePacket(conn net.Conn, packet *gsp.Packet) error {
	if packet.Type != gsp.TypeJSON {
		return errors.New("接收到非法的PacketType")
	}
	if s.SenderOperation(conn, packet.Payload) != nil {
		return errors.New("接收到无法解析的指令")
	}
	return nil
}

// IndexValid 校验 index 下标这个块是合法的，当前拥有这个块
// 返回 存在与否、哈希校验值
func (s *Session) IndexValid(i int64) (bool, uint32) {
	if v, ok := s.chunkHas[i]; ok {
		return true, v
	}
	return false, 0
}

// CloseConn 关闭连接
func (s *Session) CloseConn(conn net.Conn) {
	_ = conn.Close()
	delete(s.conn, conn.RemoteAddr().String())
}
