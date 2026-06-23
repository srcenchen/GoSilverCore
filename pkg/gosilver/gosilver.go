package gosilver

import (
	"context"
	"fmt"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/internal/gsp_sdk/server"
	"go-silver-core/pkg/mempool"
	"hash/fnv"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BlockSource 描述一个正在进行中的分块下载：正在下载第 Index 块、数据源为 Addr。
// 供 UI 标注「当前正在从哪个 peer 下载哪个块」。
type BlockSource struct {
	Index int64  // 块序号
	Addr  string // 数据源地址 ip:port（中心源亦以其实际地址呈现）
}

// ProgressInfo 包含当前的下载进度状态
type ProgressInfo struct {
	TotalChunks int64         // 总分块数
	Downloaded  int64         // 已下载的分块数
	Percentage  float64       // 下载百分比 (0.0 到 100.0)
	SpeedMbps   int64         // 当前下载速度 (Mbps)
	Status      string        // 状态: "idle", "downloading", "completed", "failed", "cancelled"
	Active      []BlockSource // 当前正在下载中的块及其数据源（按块序号升序）
	Error       error         // 错误信息 (如果失败)
}

// Server 用于管理文件分发主服务端 (Sender) 的启动和停止
type Server struct {
	addr     string
	filePath string
	mp       *mempool.MemPool
	session  *server.Session
	file     *os.File
}

// NewServer 创建一个新的服务端实例
// addr: 服务端监听地址，例如 ":48080"
// filePath: 要分发的文件路径
func NewServer(addr string, filePath string) *Server {
	return &Server{
		addr:     addr,
		filePath: filePath,
	}
}

// Start 启动服务端并开始监听和分块
func (s *Server) Start() error {
	s.mp = mempool.NewMemPool(_const.ChunkSize)
	s.session = server.NewGspSession(s.addr, s.mp)

	if err := s.session.Start(); err != nil {
		return err
	}

	f, err := os.Open(s.filePath)
	if err != nil {
		s.session.Stop()
		return err
	}
	s.file = f

	if err := s.session.BeSendMain(f); err != nil {
		f.Close()
		s.session.Stop()
		return err
	}

	return nil
}

// PeerProgress 是一个接收端在本服务端视角下的下载进度（供 UI 渲染节点列表）。
type PeerProgress struct {
	UUID       string  // 对端 UUID
	Addr       string  // 对端地址 ip:port
	Owned      int64   // 已拥有的分块数
	Total      int64   // 总分块数
	Percentage float64 // 进度百分比 0.0~100.0
	MaxSpeed   int64   // 历史最大速度 Mbps
}

// Snapshot 返回当前所有接收端（不含自身）的下载进度快照。服务端未启动时返回 nil。
func (s *Server) Snapshot() []PeerProgress {
	if s.session == nil {
		return nil
	}
	raw := s.session.Snapshot()
	out := make([]PeerProgress, 0, len(raw))
	for _, p := range raw {
		pct := 0.0
		if p.Total > 0 {
			pct = float64(p.Owned) / float64(p.Total) * 100
		}
		out = append(out, PeerProgress{
			UUID:       p.UUID,
			Addr:       p.Addr,
			Owned:      p.Owned,
			Total:      p.Total,
			Percentage: pct,
			MaxSpeed:   p.MaxSpeed,
		})
	}
	return out
}

// Stop 停止服务端监听并关闭文件
func (s *Server) Stop() {
	if s.session != nil {
		s.session.Stop()
	}
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

// Client 用于管理文件接收客户端 (Receiver) 的下载和 P2P 上报
type Client struct {
	senderAddr string
	saveDir    string
	Zone       string // 本节点机房标签（可空，空则由中心按 IP 推断），需在 StartDownload 前设置
	peerPort   int
	mp         *mempool.MemPool
	session    *server.Session
	file       *os.File

	mu         sync.Mutex
	status     ProgressInfo
	active     map[int64]string // 进行中的块 -> 数据源地址（受 mu 保护）
	progressCh chan ProgressInfo
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

// activeSnapshotLocked 返回当前进行中下载的有序快照。调用方必须持有 c.mu。
func (c *Client) activeSnapshotLocked() []BlockSource {
	if len(c.active) == 0 {
		return nil
	}
	out := make([]BlockSource, 0, len(c.active))
	for idx, addr := range c.active {
		out = append(out, BlockSource{Index: idx, Addr: addr})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// setActive 登记/更新「正在从 addr 下载第 i 块」，并推送一次进度供 UI 实时刷新。
func (c *Client) setActive(i int64, addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active[i] = addr
	c.status.Active = c.activeSnapshotLocked()
	c.updateProgress(c.status)
}

// clearActive 移除一条进行中的下载（块完成或失败时调用）。
func (c *Client) clearActive(i int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.active, i)
	c.status.Active = c.activeSnapshotLocked()
}

// NewClient 创建一个新的客户端实例
// senderAddr: 主发送端/服务端的地址，例如 "192.168.1.10:48080"
// saveDir: 文件保存目录，如果为空则保存在当前目录
func NewClient(senderAddr string, saveDir string) *Client {
	return &Client{
		senderAddr: senderAddr,
		saveDir:    saveDir,
		progressCh: make(chan ProgressInfo, 100),
		status: ProgressInfo{
			Status: "idle",
		},
	}
}

// StartDownload 启动非阻塞的文件下载过程，返回一个用于接收进度反馈的通道
func (c *Client) StartDownload() (<-chan ProgressInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status.Status == "downloading" {
		return nil, fmt.Errorf("download already in progress")
	}

	c.status = ProgressInfo{
		Status: "downloading",
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	c.wg.Add(1)
	go c.runDownload(ctx)

	return c.progressCh, nil
}

// CancelDownload 取消当前的下载，并同步阻塞直到协程完全退出并释放所有资源
func (c *Client) CancelDownload() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	c.wg.Wait()
}

// GetStatus 获取当前的下载状态副本
func (c *Client) GetStatus() ProgressInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Client) updateProgress(info ProgressInfo) {
	select {
	case c.progressCh <- info:
	default:
		// 如果通道满了，移除旧消息放入新消息，防止阻塞下载过程
		select {
		case <-c.progressCh:
		default:
		}
		select {
		case c.progressCh <- info:
		default:
		}
	}
}

func (c *Client) finishWithError(err error, contextMsg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Status = "failed"
	c.status.Error = fmt.Errorf("%s: %w", contextMsg, err)
	c.updateProgress(c.status)
}

func (c *Client) finishCancelled() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Status = "cancelled"
	c.status.Error = context.Canceled
	c.updateProgress(c.status)
}

func (c *Client) runDownload(ctx context.Context) {
	defer c.wg.Done()

	c.mu.Lock()
	c.active = make(map[int64]string)
	c.mu.Unlock()

	c.mp = mempool.NewMemPool(_const.ChunkSize)

	// 随机分配子节点端口
	c.peerPort = rand.IntN(999) + 3000
	c.session = server.NewGspSession(":"+strconv.Itoa(c.peerPort), c.mp)
	if err := c.session.Start(); err != nil {
		c.finishWithError(err, "failed to start peer server")
		return
	}
	defer c.session.Stop()

	gspC := client.NewGspSdk(c.senderAddr, c.mp, c.session.UUID, c.Zone)
	var status model.GetFileStatusResp
	for {
		var err error
		status, err = gspC.GetFileStatus()
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			c.finishCancelled()
			return
		default:
		}
		c.mu.Lock()
		c.status.Error = fmt.Errorf("正在重连服务端 (每5秒重试): %w", err)
		c.updateProgress(c.status)
		c.mu.Unlock()
		time.Sleep(5 * time.Second)
	}

	c.mu.Lock()
	c.status.TotalChunks = status.ChunkNum
	c.mu.Unlock()

	// 解析保存路径：保存目录由调用方（可由发送端推送）指定，为空时落到可定位的默认目录；
	// 目录不存在则自动创建；文件名做跨平台清洗，避免 Windows 非法字符/路径穿越/产物找不到。
	savePath, err := resolveSavePath(c.saveDir, status.FileName)
	if err != nil {
		c.finishWithError(err, "failed to prepare save directory")
		return
	}

	f, err := os.Create(savePath)
	if err != nil {
		c.finishWithError(err, "failed to create local file")
		return
	}
	c.file = f
	defer func() {
		if c.file != nil {
			_ = c.file.Close()
			c.file = nil
		}

		c.mu.Lock()
		statusStr := c.status.Status
		c.mu.Unlock()

		if statusStr != "completed" {
			log.Printf("[gosilver] 下载未完成 (状态: %s)，清理半成品文件: %s", statusStr, savePath)
			_ = os.Remove(savePath)
		}
	}()
	log.Printf("[gosilver] 文件将保存至: %s", savePath)

	if err := f.Truncate(status.FileSize); err != nil {
		c.finishWithError(err, "failed to truncate file")
		return
	}

	c.session.BeSendSub(f)
	ck := c.session.GetChunk()

	// 控制面处理器：响应中心的统一指挥（prefetch 补种 / backoff 降并发）。
	onControl := func(msg model.ControlMsg) {
		switch msg.Cmd {
		case "prefetch":
			if c.session.HasLocalChunk(msg.Index) {
				return
			}
			re, err := gspC.WantChunk(msg.Index, _const.SchedMaxSourcesPerWant)
			if err != nil {
				return
			}
			if cm, _, _, ok := gspC.FetchChunk(msg.Index, &ck, re.Sources, c.senderAddr, nil); ok {
				c.session.AddChunk(msg.Index, cm)
				_ = gspC.ReportChunk(c.session.UUID, msg.Index)
				log.Printf("[gosilver] 已按中心指令预取块 %d 补种", msg.Index)
			}
		case "backoff":
			c.session.SetUploadMax(_const.UploadConcurrencyHDD)
			go func(until int64) {
				if d := time.Until(time.Unix(until, 0)); d > 0 {
					time.Sleep(d)
				}
				c.session.SetUploadMax(_const.UploadConcurrencyDefault)
			}(msg.Until)
		}
	}

	// 注册 Peer
	for {
		err := gspC.PeerReg(c.peerPort, c.session.UUID, onControl)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			c.finishCancelled()
			return
		default:
		}
		c.mu.Lock()
		c.status.Error = fmt.Errorf("注册对端失败，正在重试 (每5秒重试): %w", err)
		c.updateProgress(c.status)
		c.mu.Unlock()
		time.Sleep(5 * time.Second)
	}

	// 准备分块索引 —— UUID 哈希错峰策略
	//
	// 问题：若所有客户端都从第0块开始（或纯随机），早期大家同时抢相同的块，
	// P2P 在分发中段才能体现价值。
	//
	// 方案：用本节点 UUID 的 FNV 哈希计算一个固定偏移量，使不同客户端的起始
	// 块均匀分散在 [0, ChunkNum) 区间内。起始块各不相同 → 较早的客户端优先
	// 下载前段、较晚的客户端下载后段，彼此拥有对方缺少的块，P2P 价值更早出现。
	//
	// 每个客户端的下载序列是：[offset, offset+1, ..., ChunkNum-1, 0, 1, ..., offset-1]
	// 这是一个循环偏移，保证所有块都会被下载。
	// 空文件（ChunkNum==0）：无需下载，直接标记完成，避免对 0 取模 panic。
	if status.ChunkNum <= 0 {
		c.mu.Lock()
		c.status.Status = "completed"
		c.status.Percentage = 100.0
		c.status.SpeedMbps = 0
		c.updateProgress(c.status)
		c.mu.Unlock()
		return
	}

	h := fnv.New32a()
	h.Write([]byte(c.session.UUID))
	offset := int64(h.Sum32()) % int64(status.ChunkNum)

	indices := make([]int64, status.ChunkNum)
	for i := range indices {
		indices[i] = (offset + int64(i)) % int64(status.ChunkNum)
	}

	var downloadedCount int64

	for len(indices) > 0 {
		select {
		case <-ctx.Done():
			c.finishCancelled()
			return
		default:
		}

		var failedList []int64
		var mu sync.Mutex
		var wg sync.WaitGroup
		limit := make(chan struct{}, 5) // 控制并发数

	OuterLoop:
		for _, idx := range indices {
			select {
			case <-ctx.Done():
				break OuterLoop
			default:
			}

			wg.Add(1)
			limit <- struct{}{}

			go func(i int64) {
				defer wg.Done()
				defer func() { <-limit }()

				select {
				case <-ctx.Done():
					return
				default:
				}

				// 询问调度中心，拿到按吞吐排序的候选源列表
				reChunk, err := gspC.WantChunk(i, _const.SchedMaxSourcesPerWant)
				if err != nil {
					mu.Lock()
					failedList = append(failedList, i)
					mu.Unlock()
					return
				}

				// 按候选列表本地故障转移（内部完成 ReportPeer 上报）。
				// onAttempt 实时记录「正在从哪个 peer 下载本块」供 TUI 标注；故障转移时会更新为次选源。
				cm, speedMbps, _, ok := gspC.FetchChunk(i, &ck, reChunk.Sources, c.senderAddr, func(addr string) {
					c.setActive(i, addr)
				})
				c.clearActive(i)
				if !ok {
					mu.Lock()
					failedList = append(failedList, i)
					mu.Unlock()
					return
				}

				c.session.AddChunk(i, cm)
				_ = gspC.ReportChunk(c.session.UUID, i)

				c.mu.Lock()
				downloadedCount++
				c.status.Downloaded = downloadedCount
				if status.ChunkNum > 0 {
					c.status.Percentage = float64(downloadedCount) / float64(status.ChunkNum) * 100
				}
				c.status.SpeedMbps = speedMbps
				c.updateProgress(c.status)
				c.mu.Unlock()
			}(idx)
		}
		wg.Wait()

		select {
		case <-ctx.Done():
			c.finishCancelled()
			return
		default:
		}

		if len(failedList) > 0 {
			if len(failedList) == len(indices) {
				select {
				case <-ctx.Done():
					c.finishCancelled()
					return
				case <-time.After(5 * time.Second):
				}
			} else {
				time.Sleep(500 * time.Millisecond)
			}
		}
		indices = failedList
	}

	c.mu.Lock()
	c.status.Status = "completed"
	c.status.Percentage = 100.0
	c.status.SpeedMbps = 0
	c.updateProgress(c.status)
	c.mu.Unlock()
}

// resolveSavePath 根据保存目录与远端文件名计算最终落盘路径。
// saveDir 为空时使用可定位的默认目录；目录不存在则创建；文件名做跨平台清洗。
func resolveSavePath(saveDir, rawName string) (string, error) {
	dir := saveDir
	if dir == "" {
		dir = defaultSaveDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, SanitizeFileName(rawName)), nil
}

// defaultSaveDir 返回一个可定位的默认保存目录：可执行文件同级的 GoSilverDownloads。
// 这样即使在 Windows 下双击运行（工作目录不确定），用户也能稳定地在程序旁找到下载产物。
func defaultSaveDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "GoSilverDownloads")
	}
	return "GoSilverDownloads"
}

// SanitizeFileName 清洗远端传来的文件名：剥离路径成分、替换各平台非法字符、
// 处理 Windows 结尾点/空格与保留设备名，保证在 Windows/macOS/Linux 上都能安全创建文件。
// 导出以便其他接收路径（如 internal/receiver）复用同一套防路径穿越逻辑，避免行为不一致。
func SanitizeFileName(name string) string {
	// 替换 Windows 非法字符 \ / : * ? " < > | 与控制字符；同时消除路径分隔符以防目录穿越
	name = strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}, name)
	// Windows 不允许文件名以点或空格结尾
	name = strings.TrimRight(name, " .")
	if name == "" {
		return "download"
	}
	// 规避 Windows 保留设备名（CON/PRN/AUX/NUL/COM1-9/LPT1-9）
	stem := name
	if i := strings.IndexByte(name, '.'); i >= 0 {
		stem = name[:i]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "_" + name
	}
	return name
}
