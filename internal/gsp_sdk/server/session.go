package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go-silver-core/internal/chunk"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/pkg/mempool"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Peer struct {
	sendMu      sync.Mutex
	connAddr    string
	conn        net.Conn
	connNum     int
	speedEwma   float64
	failStreak  int
	cooldownTil time.Time
	leases      map[string]time.Time
}

// Session 这里是发送端的Session
// 但每个节点都算一个发送端的，所以都会配备一个Session
type Session struct {
	mu            sync.RWMutex
	wg            sync.WaitGroup
	connections   map[net.Conn]struct{}
	files         []*os.File
	control       func(context.Context, json.RawMessage) (any, error)
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
	isMain        bool          // 是否为主发送端
	done          chan struct{} // 关闭通道
	uploadSem     chan struct{} // 并发上传限制 (限流信号量)
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
		uploadSem:   make(chan struct{}, 8),
		connections: make(map[net.Conn]struct{}),
	}
}

// Start 建立服务端监听
func (s *Session) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return net.ErrClosed
	default:
	}
	if s.lis != nil {
		return errors.New("session already started")
	}
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.lis, s.addr = lis, lis.Addr().String()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			select {
			case <-s.done:
				s.mu.Unlock()
				conn.Close()
				return
			default:
			}
			if len(s.connections) >= 512 {
				s.mu.Unlock()
				conn.Close()
				continue
			}
			s.connections[conn] = struct{}{}
			s.wg.Add(1)
			s.mu.Unlock()
			go func() { defer s.wg.Done(); s.handle(conn) }()
		}
	}()
	return nil
}

func (s *Session) Snapshot() []model.PeerInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := len(s.ChunkOwners)
	out := make([]model.PeerInfo, 0, len(s.Peers))
	for id, p := range s.Peers {
		if p == nil {
			continue
		}
		chunks := 0
		if owned := s.PeerOwners[id]; owned != nil {
			chunks = len(owned)
		} else if id == s.UUID {
			chunks = total
		}
		out = append(out, model.PeerInfo{
			UUID:     id,
			Addr:     p.connAddr,
			Chunks:   chunks,
			Total:    total,
			Speed:    p.speedEwma,
			Inflight: p.connNum,
			Failed:   p.failStreak,
			Self:     id == s.UUID,
			Online:   id == s.UUID || p.conn != nil,
		})
	}
	return out
}

func (s *Session) BroadcastStart() int {
	s.mu.RLock()
	if s.chunkProvider.FileStat == nil {
		s.mu.RUnlock()
		return 0
	}
	payload, _ := json.Marshal(model.PushStart{Operate: "pushStart", FileName: s.chunkProvider.FileStat.Name(), FileSize: s.chunkProvider.FileStat.Size(), ChunkNum: s.chunkProvider.GetChunkNum(), ChunkSize: s.chunkProvider.GetChunkSize()})
	var peers []*Peer
	for id, p := range s.Peers {
		if id != s.UUID && p != nil && p.conn != nil {
			peers = append(peers, p)
		}
	}
	s.mu.RUnlock()
	results := make(chan bool, len(peers))
	for _, peer := range peers {
		go func(p *Peer) {
			p.sendMu.Lock()
			defer p.sendMu.Unlock()
			s.mu.RLock()
			c := p.conn
			s.mu.RUnlock()
			if c == nil {
				results <- false
				return
			}
			_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
			err := (&gsp.Codec{}).EncodeTo(c, gsp.TypeJSON, payload)
			_ = c.SetWriteDeadline(time.Time{})
			results <- err == nil
		}(peer)
	}
	n := 0
	for range peers {
		if <-results {
			n++
		}
	}
	return n
}

func (s *Session) Port() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(port)
	return n
}

// Stop 停止监听并关闭所有连接
func (s *Session) Stop() {
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		s.wg.Wait()
		return
	default:
		close(s.done)
	}
	if s.lis != nil {
		_ = s.lis.Close()
		s.lis = nil
	}
	for c := range s.connections {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		_ = f.Close()
	}
	s.files = nil
}

// RetainFile keeps a completed download available to other peers until Stop.
func (s *Session) RetainFile(f *os.File) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files = append(s.files, f)
}
func (s *Session) SetControlHandler(h func(context.Context, json.RawMessage) (any, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.control = h
}
func (s *Session) Control(conn net.Conn, data []byte) {
	s.mu.RLock()
	h := s.control
	s.mu.RUnlock()
	var value any
	var err error
	if h == nil {
		err = errors.New("receiver control unavailable")
	} else {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			select {
			case <-s.done:
				cancel()
			case <-done:
			}
		}()
		value, err = h(ctx, data)
		close(done)
		cancel()
	}
	resp := model.ControlResponse{Value: value}
	if err != nil {
		resp.Error = err.Error()
	}
	payload, e := json.Marshal(resp)
	if e == nil {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = (&gsp.Codec{}).EncodeTo(conn, gsp.TypeJSON, payload)
	}
}

// BeSendMain 作为发送主机
func (s *Session) BeSendMain(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("source must be a regular file")
	}
	ck := chunk.NewFileChunk(f, s.memPool)
	s.mu.Lock()
	defer s.mu.Unlock()
	nums := ck.GetChunkNum()
	s.chunkProvider = *ck
	s.isMain = true
	// 把自己也作为一个 Peer
	s.Peers[s.UUID] = &Peer{
		connAddr: "",
		leases:   map[string]time.Time{},
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunkHash = make(map[int64]uint32)
	for _, old := range s.files {
		_ = old.Close()
	}
	s.files = nil
	ck := chunk.NewFileChunk(f, s.memPool)
	s.chunkProvider = *ck
	return
}

// handle 处理接收端的连接
func (s *Session) handle(conn net.Conn) {
	addr := conn.RemoteAddr()
	slog.Debug("peer connected", "address", addr.String())
	defer s.CloseConn(conn)
	buf := make([]byte, 64<<10)
	for {
		codec := gsp.Codec{}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		packet, err := codec.DecodeBounded(conn, buf, 4<<20)
		if err != nil {
			slog.Debug(fmt.Sprintf("接收端 %s 即将断开连接 %s. ", addr, err))
			s.CloseConn(conn)
			return
		}
		// Each response needs a fresh deadline. A previous control or chunk
		// response may have left an expired write deadline on this pooled socket.
		_ = conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
		if err := s.parsePacket(conn, packet); err != nil {
			slog.Debug(fmt.Sprintf("接收端 %s 即将断开连接 %s. ", addr, err))
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
	s.mu.Lock()
	delete(s.connections, conn)
	s.mu.Unlock()
	_ = conn.Close()
}

func (s *Session) ClearChunks() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunkHash = make(map[int64]uint32)
	s.chunkProvider = chunk.FileChunk{}
}

func (s *Session) MarkSender() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isMain = true
	if s.Peers[s.UUID] == nil {
		s.Peers[s.UUID] = &Peer{leases: map[string]time.Time{}}
	}
}
func (s *Session) NodeStatus() model.NodeStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return model.NodeStatus{UUID: s.UUID, Sender: s.isMain, HasFile: s.chunkProvider.FileStat != nil}
}

// ClearContent retires the source while keeping the command/control listener.
func (s *Session) ClearContent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunkProvider = chunk.FileChunk{}
	s.chunkHash = map[int64]uint32{}
	s.ChunkOwners = map[int64]map[string]struct{}{}
	s.PeerOwners = map[string]map[int64]struct{}{}
	for _, p := range s.Peers {
		p.leases = map[string]time.Time{}
		p.connNum = 0
	}
}
