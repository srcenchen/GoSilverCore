package server

import (
	"errors"
	"fmt"
	"go-silver-core/internal/chunk"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp"
	"go-silver-core/pkg/mempool"
	"log/slog"
	"math"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// zoneOfCentral 是中心服务端自身的机房标签，对所有机房可达（但出口稀缺）。
const zoneOfCentral = "*"

// zoneOf 计算一个节点的机房标签：显式标签优先，否则按 IP /24 前缀推断。
func zoneOf(ipPort, explicit string) string {
	if explicit != "" {
		return explicit
	}
	host := ipPort
	if h, _, err := net.SplitHostPort(ipPort); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		// 默认 /24：前三段相同即同机房
		return fmt.Sprintf("%d.%d.%d.0/%d", v4[0], v4[1], v4[2], _const.SchedDefaultZonePrefixBits)
	}
	v6 := ip.To16()
	return fmt.Sprintf("%x:%x:%x::/48", v6[0:2], v6[2:4], v6[4:6])
}

type Peer struct {
	connAddr string // 连接地址 ip:port
	zone     string // 所在机房标签（显式上报或按 IP 推断）

	// upMbpsEWMA 是该源有效服务速率的 EWMA 实测值（Mbps）。
	// 它天然等于 min(网络上行, 磁盘读)：HDD 节点会自然收敛到较低值。
	// 0 表示尚无实测样本，调度时按冷启动乐观默认值处理。
	upMbpsEWMA float64

	// failScore 是带时间衰减的连续失败分（Rel = 0.5^failScore）。
	// 失败时先衰减再 +1，成功时清零；配合 lastFailUnix 实现「好节点能从瞬时抖动恢复」。
	failScore   float64
	lastFailSec int64

	// leaseExpiry 保存当前所有未完成调度租约的到期时间（Unix 纳秒）。
	// connNum = 有效（未过期）租约数。租约带 TTL，死客户端不会永久占用名额。
	leaseExpiry []int64

	// controlConn 是该 peer 的 PeerReg 长连接，中心经此主动下发控制指令（统一指挥）。
	controlConn net.Conn
}

// pruneLeases 移除已过期的租约。调用方必须持有 Session 锁。
func (p *Peer) pruneLeases(nowNano int64) {
	if len(p.leaseExpiry) == 0 {
		return
	}
	kept := p.leaseExpiry[:0]
	for _, exp := range p.leaseExpiry {
		if exp > nowNano {
			kept = append(kept, exp)
		}
	}
	p.leaseExpiry = kept
}

// connNum 返回当前有效租约数（需先 pruneLeases）。
func (p *Peer) connNum() int { return len(p.leaseExpiry) }

// addLease 记一个带 TTL 的租约。调用方必须持有 Session 锁。
func (p *Peer) addLease(nowNano, ttlNano int64) {
	p.leaseExpiry = append(p.leaseExpiry, nowNano+ttlNano)
}

// releaseLease 释放一个租约（完成/失败时调用），优先释放最早到期的一个。
func (p *Peer) releaseLease() {
	if len(p.leaseExpiry) == 0 {
		return
	}
	minIdx := 0
	for i, exp := range p.leaseExpiry {
		if exp < p.leaseExpiry[minIdx] {
			minIdx = i
		}
	}
	p.leaseExpiry = append(p.leaseExpiry[:minIdx], p.leaseExpiry[minIdx+1:]...)
}

// effFail 返回按时间衰减后的失败分。调用方需提供当前 Unix 秒。
func (p *Peer) effFail(nowSec int64) float64 {
	if p.failScore <= 0 {
		return 0
	}
	elapsed := float64(nowSec - p.lastFailSec)
	if elapsed <= 0 {
		return p.failScore
	}
	return p.failScore * math.Pow(0.5, elapsed/_const.SchedFailHalfLifeSec)
}

// effUp 返回有效上行估计（Mbps），无实测样本时取冷启动乐观默认值。
func (p *Peer) effUp() float64 {
	if p.upMbpsEWMA <= 0 {
		return _const.SchedColdStartMbps
	}
	return p.upMbpsEWMA
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

	// 上传并发限流（动态可调，支持控制面 backoff 指令收缩）。
	// 用 atomic 计数 + 动态上限，而非固定容量 channel，便于运行时调整 max。
	uploadCur atomic.Int64
	uploadMax atomic.Int64
}

func NewGspSession(addr string, mempool *mempool.MemPool) *Session {
	uuidV7, _ := uuid.NewV7()
	s := &Session{
		addr:        addr,
		UUID:        uuidV7.String(),
		chunkHash:   map[int64]uint32{},
		ChunkOwners: make(map[int64]map[string]struct{}),
		Peers:       map[string]*Peer{},
		PeerOwners:  make(map[string]map[int64]struct{}),
		memPool:     mempool,
		done:        make(chan struct{}),
	}
	s.uploadMax.Store(_const.UploadConcurrencyDefault)
	s.queue = &queue2{s: s}
	return s
}

// AcquireUpload 尝试占用一个上传名额，最多等待 timeout。占用成功返回 true。
// 用于 handle.GetChunk 限制本节点并发上传，保护 HDD 源不被并发随机读拖垮。
func (s *Session) AcquireUpload(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		cur := s.uploadCur.Load()
		if cur < s.uploadMax.Load() {
			if s.uploadCur.CompareAndSwap(cur, cur+1) {
				return true
			}
			continue // CAS 失败，重试
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ReleaseUpload 释放一个上传名额。
func (s *Session) ReleaseUpload() {
	if s.uploadCur.Load() > 0 {
		s.uploadCur.Add(-1)
	}
}

// SetUploadMax 动态调整最大并发上传数（控制面 backoff / 恢复时调用）。
func (s *Session) SetUploadMax(n int64) {
	if n < 1 {
		n = 1
	}
	s.uploadMax.Store(n)
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
	s.chunkProvider = *ck
	s.isMain = true
	// 把自己也作为一个 Peer：zone="*" 对所有机房可达，作为每机房播种与最终兜底。
	s.Peers[s.UUID] = &Peer{
		connAddr: "",
		zone:     zoneOfCentral,
	}
	// 启动中心协调器：周期性向各机房补种缺块、对过载慢源下发 backoff。
	s.StartCoordinator()
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
