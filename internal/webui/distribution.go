package webui

import (
	"context"
	"errors"
	"fmt"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/internal/transfer"
	"sync"
	"time"
)

type Distribution struct {
	ID       string `json:"id"`
	Active   bool   `json:"active"`
	Name     string `json:"name"`
	Target   string `json:"target"`
	Message  string `json:"message"`
	Members  int    `json:"members"`
	Excluded int    `json:"excluded"`
}

func (a *App) startDistribution(d Dispatch) error {
	s, spec, _, err := a.spec(d)
	if err != nil {
		return err
	}
	a.mu.Lock()
	already := a.distribution != nil && a.distribution.Active
	old := a.distributionDone != nil
	a.mu.Unlock()
	if already {
		return errors.New("无条件分发已经开启，请先停止接纳新设备")
	}
	if old {
		a.stopDistribution()
	}
	spec.ID = key()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.mu.Lock()
	a.distributionCancel = cancel
	a.distributionDone = done
	a.distribution = &Distribution{ID: spec.ID, Active: true, Name: s.data.Name, Target: d.Target, Message: "持续接纳当前及后续上线设备"}
	a.probeKey = ""
	a.mu.Unlock()
	go func() { defer close(done); a.distributionLoop(ctx, s, spec, d) }()
	return nil
}
func (a *App) stopDistribution() {
	a.mu.Lock()
	cancel, done := a.distributionCancel, a.distributionDone
	a.distributionCancel = nil
	a.distributionDone = nil
	if a.distribution != nil {
		a.distribution.Active = false
		a.distribution.Message = "已停止接纳新设备，已接收节点继续做种"
	}
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
func (a *App) distributionLoop(ctx context.Context, s *source, spec TaskSpec, d Dispatch) {
	members := map[string]PeerState{}
	for ctx.Err() == nil {
		a.mu.Lock()
		accepting := a.distribution != nil && a.distribution.ID == spec.ID && a.distribution.Active
		a.mu.Unlock()
		peers := s.session.Snapshot()
		previous := clonePeers(members)
		var wg sync.WaitGroup
		var mu sync.Mutex
		sem := make(chan struct{}, 8)
		for _, peer := range peers {
			if peer.Self {
				continue
			}
			p, seen := previous[peer.UUID]
			if !seen && !accepting {
				continue
			}
			if seen {
				p.PeerInfo = peer
				if p.Task == nil || p.Task.Status != "running" {
					continue
				}
			} else {
				p = PeerState{PeerInfo: peer}
			}
			wg.Add(1)
			go func(p PeerState, seen bool) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()
				sdk := client.NewGspSdk(p.Addr, nil).WithContext(ctx)
				defer sdk.Close()
				if !seen {
					var probe transfer.Probe
					err := sdk.Call("probe", d.Token, spec, &probe)
					if err != nil {
						p.Probe = err.Error()
					} else {
						p.Ready = probe.Ready
						p.Probe = probe.Target
						p.Warnings = probe.Warnings
						var state TaskState
						if err = sdk.Call("start", d.Token, spec, &state); err != nil {
							p.Ready = false
							p.Probe = err.Error()
							p.Task = &TaskState{ID: spec.ID, Kind: "download", Status: "failed", Error: err.Error()}
						} else {
							p.Task = &state
						}
					}
				} else {
					var state TaskState
					if err := sdk.Call("status", d.Token, nil, &state); err != nil {
						p.Task = &TaskState{ID: spec.ID, Kind: "download", Status: "failed", Error: err.Error()}
					} else if state.ID == spec.ID {
						p.Task = &state
					}
				}
				if ctx.Err() != nil {
					return
				}
				mu.Lock()
				members[p.UUID] = p
				mu.Unlock()
				a.storePeer(p, false)
			}(p, seen)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return
		}
		excluded, admitted := 0, 0
		for _, p := range members {
			if p.Ready {
				admitted++
			} else {
				excluded++
			}
		}
		a.mu.Lock()
		if a.distribution != nil && a.distribution.ID == spec.ID {
			a.distribution.Members = admitted
			a.distribution.Excluded = excluded
			if a.distribution.Active {
				a.distribution.Message = fmt.Sprintf("持续接纳新设备：%d 台已加入，%d 台不可用已排除", admitted, excluded)
			} else {
				a.distribution.Message = fmt.Sprintf("停止接纳新设备；%d 台已加入，接收与做种继续", admitted)
			}
			a.notice = a.distribution.Message
		}
		a.mu.Unlock()
		timer := time.NewTimer(400 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (a *App) pauseDistribution() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.distribution != nil {
		a.distribution.Active = false
		a.distribution.Message = "停止接纳新设备，已参与机器继续接收与做种"
	}
}
