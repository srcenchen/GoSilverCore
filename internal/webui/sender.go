package webui

import (
	"context"
	"errors"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/discover"
	"go-silver-core/internal/gsp_sdk/server"
	"go-silver-core/pkg/mempool"
)

// The sender control service exists even when there is no file to distribute.
func (a *App) ensureSender(ctx context.Context, listen string) error {
	a.hubMu.Lock()
	defer a.hubMu.Unlock()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return context.Canceled
	}
	if a.config.Mode == "receiver" {
		a.mu.Unlock()
		return errors.New("当前实例是接收模式")
	}
	existing := a.hub
	a.mu.Unlock()
	if existing != nil {
		return nil
	}
	if listen == "" {
		listen = ":0"
	}
	s := server.NewGspSession(listen, mempool.NewMemPool(_const.ChunkSize))
	s.MarkSender()
	if err := s.Start(); err != nil {
		return err
	}
	md, mdErr := discover.Publish(s.Port(), s.UUID)
	if err := ctx.Err(); err != nil {
		s.Stop()
		if md != nil {
			_ = md.Shutdown()
		}
		return err
	}
	a.mu.Lock()
	a.hub = s
	a.hubMDNS = md
	a.config.Mode = "sender"
	a.config.Listen = listen
	a.notice = "发送服务已就绪：可以直接执行命令，或选择文件分发"
	if mdErr != nil {
		a.notice += "；自动发现不可用，可手动直连"
	}
	a.mu.Unlock()
	return nil
}
func (a *App) stopFile() error {
	a.stopDistribution()
	a.mu.Lock()
	s := a.source
	a.source = nil
	a.probeKey = ""
	a.mu.Unlock()
	if s != nil {
		s.close()
	}
	return nil
}
