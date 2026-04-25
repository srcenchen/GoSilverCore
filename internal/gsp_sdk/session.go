package gsp_sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"go-silver-core/internal/chunk"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/pkg/mempool"
	"go-silver-core/pkg/queue"
	"hash/crc32"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"sync"
)

type Peer struct {
	connAddr string // 连接地址
	connNum  int    // 连接数
}

// Session 这里是发送端的Session
// 但每个节点都算一个发送端的，所以都会配备一个Session
type Session struct {
	mu              sync.Mutex
	lis             net.Listener
	addr            string
	conn            map[string]net.Conn
	ChunkBlockOwner map[int64][]*Peer
	chunkHash       map[int64]uint32 // 块哈希值
	chunkProvider   chunk.FileChunk  // chunk块
	memPool         *mempool.MemPool
	queue           *queue2
}

func (s *Session) GetMemPool() *mempool.MemPool {
	return s.memPool
}

// GetChunk 获取块实体
func (s *Session) GetChunk() chunk.FileChunk {
	return s.chunkProvider
}

// ReadChunk 获取Chunk块
func (s *Session) ReadChunk(i int64, buf []byte) (int, error) {
	return s.chunkProvider.ReadChunk(i, buf)
}

func NewGspSession(addr string, mempool *mempool.MemPool) *Session {

	return &Session{
		addr:            addr,
		chunkHash:       map[int64]uint32{},
		ChunkBlockOwner: make(map[int64][]*Peer),
		conn:            make(map[string]net.Conn),
		memPool:         mempool,
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
	ck := chunk.NewFileChunk(f, s.memPool)
	n := ck.GetChunkNum()
	s.chunkProvider = *ck
	for i := int64(0); i < n; i++ {
		peer := &Peer{connAddr: "", connNum: 0}
		s.ChunkBlockOwner[i] = append(s.ChunkBlockOwner[i], peer)
	}
	return nil
}

// BeSendSub 作为发送从机
func (s *Session) BeSendSub(f *os.File) {
	ck := chunk.NewFileChunk(f, s.memPool)
	s.chunkProvider = *ck
	return
}

// AddChunk 添加文件块哈希
func (s *Session) AddChunk(i int64, checksum uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.chunkHash[i]; !ok {
		s.chunkHash[i] = checksum
	}
}

// AddBlockOwner 添加文件拥有
func (s *Session) AddBlockOwner(i int64, addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ChunkBlockOwner[i] = append(s.ChunkBlockOwner[i], &Peer{connAddr: addr})
}

// TODO 测试用队列
type queue2 struct {
	s *Session
}

func (q *queue2) Want(i int64, conn net.Conn) {
	// 直接把自己发出去
	c := gsp.Codec{}
	n := rand.IntN(len(q.s.ChunkBlockOwner[i]))
	jc, _ := json.Marshal(model.WantChunkResp{
		Index:    i,
		Addr:     q.s.ChunkBlockOwner[i][n].connAddr,
		CheckSum: 0,
	})
	resp := c.Encode(gsp.TypeJSON, jc)
	conn.Write(resp)
}

// GetQueue 获取队列
func (s *Session) GetQueue() queue.DownloadQueue {
	if s.queue == nil {
		s.queue = &queue2{s: s}
	}
	return s.queue
}

// handle 处理接收端的连接
func (s *Session) handle(conn net.Conn) {
	addr := conn.RemoteAddr()
	s.mu.Lock()
	s.conn[addr.String()] = conn
	s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.chunkHash[i]; ok {
		return true, v
	}
	if i < 0 || i >= int64(len(s.ChunkBlockOwner)) {
		return false, 0
	}
	buf := s.memPool.Get(_const.ChunkSize)
	c, _ := s.chunkProvider.ReadChunk(i, *buf)
	cm := crc32.ChecksumIEEE((*buf)[:c])
	s.chunkHash[i] = cm
	return true, cm
}

// CloseConn 关闭连接
func (s *Session) CloseConn(conn net.Conn) {
	_ = conn.Close()
	s.mu.Lock()
	if _, ok := s.conn[conn.RemoteAddr().String()]; ok {
		delete(s.conn, conn.RemoteAddr().String())
	}
	s.mu.Unlock()
}
