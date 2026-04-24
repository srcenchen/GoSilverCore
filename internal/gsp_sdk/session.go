package gsp_sdk

import (
	"errors"
	"fmt"
	"go-silver-core/internal/gsp"
	"go-silver-core/pkg/chunk"
	"hash/crc32"
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
	chunkHash       map[int64]uint32 // 块哈希值
	chunkProvider   chunk.FileChunk  // chunk块
}

// GetChunk 获取块实体
func (s *Session) GetChunk() chunk.FileChunk {
	return s.chunkProvider
}

// ReadChunk 获取Chunk块
func (s *Session) ReadChunk(i int64) ([]byte, error) {
	return s.chunkProvider.ReadChunk(i)
}

func NewGspSession(addr string) *Session {
	return &Session{
		addr:            addr,
		chunkHash:       map[int64]uint32{},
		ChunkBlockOwner: make(map[int64][]*Peer),
		conn:            make(map[string]net.Conn),
	}
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
	s.chunkProvider = *ck
	for i := int64(0); i < n; i++ {
		peer := &Peer{connAddr: "", connNum: 0}
		s.ChunkBlockOwner[i] = append(s.ChunkBlockOwner[i], peer)
	}
	return nil
}

// handle 处理接收端的连接
func (s *Session) handle(conn net.Conn) {
	addr := conn.RemoteAddr()
	s.conn[addr.String()] = conn
	slog.Info("与接收端的连接已经建立 " + addr.String())
	defer s.CloseConn(conn)
	for {
		codec := gsp.Codec{}
		packet, err := codec.Decode(conn)
		if err != nil {
			slog.Error(fmt.Sprintf("接收端 %s 即将断开连接 %s. ", addr, err))
			s.CloseConn(conn)
			return
		}
		if err := s.parsePacket(conn, packet); err != nil {
			slog.Error(fmt.Sprintf("接收端 %s 即将断开连接 %s. ", addr, err))
			s.CloseConn(conn)
			return
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
	if i < 0 || i >= int64(len(s.ChunkBlockOwner)) {
		return false, 0
	}
	c, _ := s.chunkProvider.ReadChunk(i)
	return true, crc32.ChecksumIEEE(c)
}

// CloseConn 关闭连接
func (s *Session) CloseConn(conn net.Conn) {
	_ = conn.Close()
	if _, ok := s.conn[conn.RemoteAddr().String()]; ok {
		delete(s.conn, conn.RemoteAddr().String())
	}
}
