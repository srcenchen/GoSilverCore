package webui

import (
	"context"
	"errors"
	"time"
)

const discoveryInterval = 2 * time.Second

// startDiscovery owns one continuous discovery loop per receiver instance.
func (a *App) startDiscovery(c Config) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return context.Canceled
	}
	if a.config.Mode == "sender" {
		a.mu.Unlock()
		return errors.New("当前实例是分发模式")
	}
	a.mu.Unlock()
	a.stopDiscovery()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		cancel()
		return context.Canceled
	}
	a.config.Mode = "receiver"
	a.config.Token = c.Token
	a.config.AllowCommands = c.AllowCommands
	a.discoveryCancel = cancel
	a.discoveryDone = done
	a.discoveryMessage = "正在自动发现发送端…"
	a.mu.Unlock()
	go func() { defer close(done); a.discoverLoop(ctx, c) }()
	return nil
}
func (a *App) stopDiscovery() {
	a.mu.Lock()
	cancel, done := a.discoveryCancel, a.discoveryDone
	a.discoveryCancel = nil
	a.discoveryDone = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	a.mu.Lock()
	a.discoveryMessage = "自动发现已停止"
	a.mu.Unlock()
}
func (a *App) discoveryNote(note string) { a.mu.Lock(); a.discoveryMessage = note; a.mu.Unlock() }
func (a *App) discoverLoop(ctx context.Context, c Config) {
	// An explicit startup address is tried once, then discovery continues.
	a.mu.Lock()
	initial := a.agent
	a.mu.Unlock()
	if initial == nil && c.SenderAddr != "" && !c.Discover {
		direct := c
		direct.Discover = false
		if err := a.startAgent(ctx, direct); err != nil && ctx.Err() == nil {
			a.discoveryNote("直连未成功，将继续自动发现: " + err.Error())
		}
	}
	for ctx.Err() == nil {
		a.mu.Lock()
		current := a.agent
		a.mu.Unlock()
		connected := false
		if current != nil {
			current.mu.Lock()
			connected = !current.disconnected && current.token == c.Token && current.allowCommands == c.AllowCommands
			current.mu.Unlock()
		}
		if !connected {
			a.discoveryNote("正在搜索发送端；找到后将自动连接…")
		}
		addr, err := a.lookupSender(1200*time.Millisecond, "")
		if ctx.Err() != nil {
			return
		}
		if connected {
			healthCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			sdk := current.sdk.WithContext(healthCtx)
			_, healthErr := sdk.GetNodeStatus()
			cancel()
			if ctx.Err() != nil {
				return
			}
			if healthErr != nil {
				current.mu.Lock()
				current.disconnected = true
				current.mu.Unlock()
				a.discoveryNote("连接已失效，将自动重新发现: " + healthErr.Error())
			} else if err != nil {
				a.discoveryNote("已保持当前连接，持续轮询发现发送端")
			} else {
				a.discoveryNote("已连接，持续检测发送端在线状态")
			}
		} else if err != nil {
			a.discoveryNote("等待发送端上线；2 秒后继续发现。" + err.Error())
		} else {
			next := c
			next.SenderAddr = addr
			next.Discover = false
			if err = a.startAgent(ctx, next); err != nil {
				if ctx.Err() != nil {
					return
				}
				a.discoveryNote("连接暂未成功，将自动重试: " + err.Error())
			} else {
				a.discoveryNote("已自动连接 " + addr + "；持续发现，断开后自动重连")
			}
		}
		timer := time.NewTimer(discoveryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
