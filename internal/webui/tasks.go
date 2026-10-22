package webui

import (
	"context"
	"errors"
	"fmt"
	"go-silver-core/internal/gsp_sdk/client"
	"sync"
	"time"
)

func (a *App) beginCommand(fn func(context.Context) error) (Operation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return Operation{}, context.Canceled
	}
	if a.commandOp != nil && a.commandOp.Status == "running" {
		return Operation{}, errors.New("已有命令正在执行")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.commandCancel = cancel
	a.commandDone = make(chan struct{})
	done := a.commandDone
	a.commandOp = &Operation{ID: key(), Kind: "command", Status: "running", Message: "正在执行命令", Started: time.Now()}
	copy := *a.commandOp
	go func() {
		defer close(done)
		defer cancel()
		err := fn(ctx)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.commandOp.Status = "completed"
		a.commandOp.Message = "命令执行完成"
		if err != nil {
			a.commandOp.Status = "failed"
			a.commandOp.Message = err.Error()
			if errors.Is(err, context.Canceled) {
				a.commandOp.Status = "cancelled"
			}
		}
	}()
	return copy, nil
}
func (a *App) storePeer(p PeerState, command bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.probes[p.UUID]
	old.PeerInfo = p.PeerInfo
	if command {
		old.Command = p.Command
	} else {
		old.Task = p.Task
		old.Ready = p.Ready
		old.Probe = p.Probe
		old.Warnings = p.Warnings
	}
	a.probes[p.UUID] = old
}
func (a *App) monitor(ctx context.Context, d Dispatch, peers map[string]PeerState, command ...bool) error {
	commands := len(command) > 0 && command[0]
	method := "status"
	if commands {
		method = "command.status"
	}
	clients := map[string]*client.GspSdk{}
	for id, p := range peers {
		sdk := client.NewGspSdk(p.Addr, nil).WithContext(ctx)
		clients[id] = &sdk
	}
	defer func() {
		for _, sdk := range clients {
			sdk.Close()
		}
	}()
	for {
		if ctx.Err() != nil {
			a.cancelRemoteKind(d, peers, commands)
			return ctx.Err()
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		sem := make(chan struct{}, 8)
		allDone := true
		for id, p := range clonePeers(peers) {
			task := p.Task
			if commands {
				task = p.Command
			}
			if task == nil || task.Status != "running" {
				continue
			}
			allDone = false
			wg.Add(1)
			go func(id string, p PeerState, task *TaskState) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()
				var state TaskState
				err := clients[id].Call(method, d.Token, nil, &state)
				if err != nil {
					state = TaskState{ID: task.ID, Kind: task.Kind, Status: "failed", Error: err.Error()}
				} else if state.ID != task.ID {
					state = TaskState{ID: task.ID, Kind: task.Kind, Status: "failed", Error: "设备已切换到其他任务"}
				}
				if commands {
					p.Command = &state
				} else {
					p.Task = &state
				}
				mu.Lock()
				peers[id] = p
				mu.Unlock()
			}(id, p, task)
		}
		wg.Wait()
		for _, p := range peers {
			a.storePeer(p, commands)
		}
		if allDone {
			failed := 0
			for _, p := range peers {
				task := p.Task
				if commands {
					task = p.Command
				}
				if task != nil && (task.Status == "failed" || task.Status == "cancelled") {
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d 台机器执行失败，请查看设备详情", failed)
			}
			return nil
		}
		timer := time.NewTimer(400 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			a.cancelRemoteKind(d, peers, commands)
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (a *App) cancelRemoteKind(d Dispatch, peers map[string]PeerState, commands bool) {
	if !commands {
		a.cancelRemote(d, peers)
		return
	}
	var wg sync.WaitGroup
	for _, p := range peers {
		if p.Command == nil || p.Command.ID == "" {
			continue
		}
		wg.Add(1)
		go func(p PeerState) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			sdk := client.NewGspSdk(p.Addr, nil).WithContext(ctx)
			defer sdk.Close()
			_ = sdk.Call("command.cancel", d.Token, map[string]string{"id": p.Command.ID}, nil)
		}(p)
	}
	wg.Wait()
}
