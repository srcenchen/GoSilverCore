package server

import (
	"errors"
	"fmt"
	"go-silver-core/internal/chunk"
	"go-silver-core/internal/gsp"
	"go-silver-core/pkg/mempool"
	"log/slog"
	"net"
	"os"
	"sync"

	"github.com/google/uuid"
)

type Peer struct {
	connAddr string // 连接地址
	connNum  int    // 连接数
	maxSpeed int64  // 最大连接速度
}

// Session 这里是发送端的Session
// 但每个节点都算一个发送端的，所以都会配备一个Session
type Session struct {
	mu            sync.RWMutex
	lis           net.Listener
	UUID          string
	addr          string
	Peers         map[string]*Peer              // key 是 uuid
	ChunkOwners   map[int64]map[string]struct{} // 这个块拥有的Peer
	PeerOwners    map[string]map[int64]struct{} // 这个Peer拥有的块
	chunkHash     map[int64]uint32              // 块哈希值
	chunkProvider chunk.FileChunk               // chunk块
	memPool       *mempool.MemPool
	queue         *queue2
	isMain        bool                          // 是否为主发送端
	done          chan struct{}                 // 关闭通道
	uploadSem     chan struct{}                 // 并发上传限制 (限流信号量)
}

func NewGspSession(addr string, mempool *mempool.MemPool) *Session {
	uuidV7, _ := uuid.NewV7()
	return &Session{
		addr:        addr,
		UUID:        uuidV7.String(),
		chunkHash:   map[int64]uint32{},
		ChunkOwners: make(map[int64]map[string]struct{}),
		Peers:       map[string]*Peer{},
		PeerOwners:  make(map[string]map[int64]struct{}),
		memPool:     mempool,
		done:        make(chan struct{}),
		uploadSem:   make(chan struct{}, 5), // 限制最大 5 个并发上传
	}
}

// Start 建立服务端监听
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
				select {
				case <-s.done:
					return // 正常停止
				default:
				}
				slog.Error("与接收端建立连接失败: " + err.Error())
				return // 出现非正常错误时退出，防止 CPU 空转和 nil 指针崩溃
			}
			go s.handle(conn)
		}
	}()
	return nil
}

// Stop 停止监听并关闭所有连接
func (s *Session) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		// 已经关闭
	default:
		close(s.done)
	}
	if s.lis != nil {
		_ = s.lis.Close()
		s.lis = nil
	}
}

// BeSendMain 作为发送主机
func (s *Session) BeSendMain(f *os.File) error {
	ck := chunk.NewFileChunk(f, s.memPool)
	nums := ck.GetChunkNum()
	s.chunkProvider = *ck
	s.isMain = true
	// 把自己也作为一个 Peer
	s.Peers[s.UUID] = &Peer{
		connAddr: "",
	}
	for i := int64(0); i < nums; i++ {
		if s.ChunkOwners[i] == nil {
			s.ChunkOwners[i] = make(map[string]struct{})
		}
		s.ChunkOwners[i][s.UUID] = struct{}{}
	}
	return nil
}

// BeSendSub 作为发送从机
func (s *Session) BeSendSub(f *os.File) {
	ck := chunk.NewFileChunk(f, s.memPool)
	s.chunkProvider = *ck
	return
}

// handle 处理接收端的连接
func (s *Session) handle(conn net.Conn) {
	addr := conn.RemoteAddr()
	s.mu.Lock()
	s.mu.Unlock()
	slog.Info("与接收端的连接已经建立 " + addr.String())
	defer s.CloseConn(conn)
	buf := make([]byte, 64*(1<<10))
	for {
		codec := gsp.Codec{}
		packet, err := codec.Decode(conn, buf)
		if err != nil {
			slog.Info(fmt.Sprintf("接收端 %s 即将断开连接 %s. ", addr, err))
			s.CloseConn(conn)
			return
		}
		if err := s.parsePacket(conn, packet); err != nil {
			slog.Info(fmt.Sprintf("接收端 %s 即将断开连接 %s. ", addr, err))
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

// CloseConn 关闭连接
func (s *Session) CloseConn(conn net.Conn) {
	_ = conn.Close()
}
