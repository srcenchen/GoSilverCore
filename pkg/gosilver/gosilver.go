package gosilver

import (
	"context"
	"fmt"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/internal/gsp_sdk/server"
	"go-silver-core/internal/receiver"
	"go-silver-core/pkg/mempool"
	"os"
	"path/filepath"
	"sync"
)

type ProgressInfo struct {
	TotalChunks int64
	Downloaded  int64
	Percentage  float64
	SpeedMbps   int64
	Status      string
	Error       error
}
type Server struct {
	mu             sync.Mutex
	addr, filePath string
	session        *server.Session
	file           *os.File
}

func NewServer(addr, path string) *Server { return &Server{addr: addr, filePath: path} }
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		return fmt.Errorf("server already started")
	}
	f, err := os.Open(s.filePath)
	if err != nil {
		return err
	}
	session := server.NewGspSession(s.addr, mempool.NewMemPool(_const.ChunkSize))
	if err = session.BeSendMain(f); err == nil {
		err = session.Start()
	}
	if err != nil {
		session.Stop()
		f.Close()
		return err
	}
	s.session, s.file = session, f
	return nil
}
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return 0
	}
	return s.session.Port()
}
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		s.session.Stop()
		s.session = nil
	}
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}
}

type Client struct {
	lifecycle           sync.Mutex
	mu                  sync.Mutex
	senderAddr, saveDir string
	status              ProgressInfo
	progressCh          chan ProgressInfo
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	session             *server.Session
	sdk                 *client.GspSdk
}

func NewClient(addr, dir string) *Client {
	return &Client{senderAddr: addr, saveDir: dir, status: ProgressInfo{Status: "idle"}}
}
func (c *Client) StartDownload() (<-chan ProgressInfo, error) {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status.Status != "idle" {
		return nil, fmt.Errorf("create a new client for another download")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.progressCh = make(chan ProgressInfo, 16)
	c.status = ProgressInfo{Status: "downloading"}
	c.wg.Add(1)
	go c.run(ctx)
	return c.progressCh, nil
}
func (c *Client) CancelDownload() {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	c.wg.Wait()
	if c.sdk != nil {
		c.sdk.Close()
	}
	if c.session != nil {
		c.session.Stop()
	}
}
func (c *Client) GetStatus() ProgressInfo { c.mu.Lock(); defer c.mu.Unlock(); return c.status }
func (c *Client) emit(p ProgressInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = p
	select {
	case c.progressCh <- p:
	default:
		select {
		case <-c.progressCh:
		default:
		}
		c.progressCh <- p
	}
}
func (c *Client) run(ctx context.Context) {
	defer c.wg.Done()
	defer close(c.progressCh)
	err := c.download(ctx)
	if err != nil {
		state := "failed"
		if ctx.Err() != nil {
			state = "cancelled"
		}
		p := c.GetStatus()
		p.Status = state
		p.Error = err
		c.emit(p)
		if c.sdk != nil {
			c.sdk.Close()
		}
		if c.session != nil {
			c.session.Stop()
		}
	}
}
func (c *Client) download(ctx context.Context) error {
	pool := mempool.NewMemPool(_const.ChunkSize)
	s := server.NewGspSession(":0", pool)
	c.session = s
	if err := s.Start(); err != nil {
		return err
	}
	sdk := client.NewGspSdk(c.senderAddr, pool).WithContext(ctx)
	c.sdk = &sdk
	meta, err := sdk.GetFileStatus()
	if err != nil {
		return fmt.Errorf("get sender file status: %w", err)
	}
	if err = sdk.PeerReg(s.Port(), s.UUID); err != nil {
		return err
	}
	dir := c.saveDir
	if dir == "" {
		dir = "."
	}
	if err = os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return receiver.RunTo(ctx, c.senderAddr, filepath.Join(dir, filepath.Base(meta.FileName)), s, sdk, meta, func(p receiver.Progress) {
		state := "downloading"
		if p.Finished {
			state = "completed"
		}
		percent := 100.0
		if p.Total > 0 {
			percent = float64(p.Done) * 100 / float64(p.Total)
		}
		c.emit(ProgressInfo{TotalChunks: p.Total, Downloaded: p.Done, Percentage: percent, SpeedMbps: p.Speed, Status: state})
	})
}
