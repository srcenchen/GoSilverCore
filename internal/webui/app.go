package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/discover"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/internal/gsp_sdk/server"
	"go-silver-core/internal/transfer"
	"go-silver-core/pkg/mempool"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/hashicorp/mdns"
)

type Config struct {
	Discover      bool   `json:"discover,omitempty"`
	Mode          string `json:"mode"`
	File          string `json:"file"`
	SenderAddr    string `json:"senderAddr"`
	Listen        string `json:"listen"`
	WorkDir       string `json:"-"`
	Token         string `json:"token"`
	AllowCommands bool   `json:"allowCommands"`
}
type Operation struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Status  string    `json:"status"`
	Message string    `json:"message"`
	Started time.Time `json:"started"`
}
type PeerState struct {
	model.PeerInfo
	Ready    bool       `json:"ready"`
	Probe    string     `json:"probe"`
	Warnings []string   `json:"warnings,omitempty"`
	Task     *TaskState `json:"task,omitempty"`
	Command  *TaskState `json:"command,omitempty"`
}
type source struct {
	data       *transfer.Source
	session    *server.Session
	file       *os.File
	mdns       *mdns.Server
	generation string
}

func (s *source) close() {
	if s.mdns != nil {
		_ = s.mdns.Shutdown()
	}
	s.session.ClearContent()
	s.file.Close()
	s.data.Close()
}

type App struct {
	receiverMu          sync.Mutex
	discoveryCancel     context.CancelFunc
	discoveryDone       chan struct{}
	discoveryMessage    string
	lookupSender        func(time.Duration, string) (string, error)
	done                chan struct{}
	mu                  sync.Mutex
	hubMu               sync.Mutex
	hub                 *server.Session
	hubMDNS             *mdns.Server
	commandOp           *Operation
	commandCancel       context.CancelFunc
	commandDone         chan struct{}
	distribution        *Distribution
	distributionCancel  context.CancelFunc
	distributionDone    chan struct{}
	lastReceiverTask    *TaskState
	lastReceiverCommand *TaskState
	config              Config
	csrf                string
	source              *source
	agent               *agent
	op                  *Operation
	opCancel            context.CancelFunc
	opDone              chan struct{}
	closed              bool
	probes              map[string]PeerState
	probeKey            string
	notice              string
	uploads             []string
}

func key() string { var b [24]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func New(c Config) (*App, error) {
	if c.Mode != "" && c.Mode != "sender" && c.Mode != "receiver" {
		return nil, errors.New("mode 必须为 sender 或 receiver")
	}
	if c.Listen == "" {
		c.Listen = ":0"
	}
	if c.WorkDir == "" {
		var e error
		c.WorkDir, e = os.Getwd()
		if e != nil {
			return nil, e
		}
	}
	abs, e := filepath.Abs(c.WorkDir)
	if e != nil {
		return nil, e
	}
	c.WorkDir = abs
	return &App{lookupSender: discover.LookupSender, config: c, csrf: key(), done: make(chan struct{}), probes: map[string]PeerState{}}, nil
}
func (a *App) Close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	close(a.done)
	if a.opCancel != nil {
		a.opCancel()
	}
	done := a.opDone
	commandDone := a.commandDone
	if a.commandCancel != nil {
		a.commandCancel()
	}
	a.mu.Unlock()
	if done != nil {
		<-done
	}
	if commandDone != nil {
		<-commandDone
	}
	a.stopDistribution()
	a.stopDiscovery()
	a.mu.Lock()
	s, r := a.source, a.agent
	hub, md := a.hub, a.hubMDNS
	a.hub = nil
	a.hubMDNS = nil
	a.source = nil
	a.agent = nil
	uploads := a.uploads
	a.mu.Unlock()
	if r != nil {
		r.close()
	}
	if hub != nil {
		hub.Stop()
	}
	if md != nil {
		_ = md.Shutdown()
	}
	if s != nil {
		s.close()
	}
	for _, p := range uploads {
		os.RemoveAll(p)
	}
}
func (a *App) begin(kind string, fn func(context.Context) error) (Operation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return Operation{}, net.ErrClosed
	}
	if a.op != nil && a.op.Status == "running" {
		return Operation{}, errors.New("已有操作正在执行，可先取消")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.opCancel = cancel
	a.opDone = make(chan struct{})
	done := a.opDone
	a.op = &Operation{ID: key(), Kind: kind, Status: "running", Message: "正在处理…", Started: time.Now()}
	result := *a.op
	go func() {
		defer close(done)
		defer cancel()
		err := fn(ctx)
		a.mu.Lock()
		defer a.mu.Unlock()
		if err != nil {
			a.op.Status = "failed"
			a.op.Message = err.Error()
			if errors.Is(err, context.Canceled) {
				a.op.Status = "cancelled"
			}
		} else {
			a.op.Status = "completed"
			a.op.Message = "操作完成"
		}
	}()
	return result, nil
}
func (a *App) cancel(scope ...string) {
	if len(scope) > 0 && scope[0] == "command" {
		a.mu.Lock()
		if a.commandCancel != nil {
			a.commandCancel()
		}
		r := a.agent
		a.mu.Unlock()
		if r != nil {
			r.cancelCommand()
		}
		return
	}
	a.mu.Lock()
	if a.opCancel != nil {
		a.opCancel()
	}
	r := a.agent
	a.mu.Unlock()
	if r != nil {
		r.cancelTask()
	}
}
func (a *App) startSource(ctx context.Context, path, listen string) error {
	a.mu.Lock()
	if a.config.Mode == "receiver" {
		a.mu.Unlock()
		return errors.New("当前实例是接收模式，不能同时启动分发服务")
	}
	if a.source != nil {
		a.mu.Unlock()
		return errors.New("请先停止当前发送服务")
	}
	a.mu.Unlock()
	data, err := transfer.Prepare(ctx, path)
	if err != nil {
		return err
	}
	f, err := os.Open(data.Path)
	if err != nil {
		data.Close()
		return err
	}
	if err = a.ensureSender(ctx, listen); err != nil {
		f.Close()
		data.Close()
		return err
	}
	a.mu.Lock()
	s := a.hub
	a.mu.Unlock()
	if err = s.BeSendMain(f); err != nil {
		f.Close()
		data.Close()
		return err
	}
	item := &source{data: data, file: f, session: s, generation: key()}
	if err = ctx.Err(); err != nil {
		item.close()
		return err
	}
	a.mu.Lock()
	a.source = item
	for id, p := range a.probes {
		p.Ready = false
		p.Probe = ""
		p.Warnings = nil
		a.probes[id] = p
	}
	a.probeKey = ""
	a.config.File = path
	a.config.Listen = listen
	a.config.Mode = "sender"
	a.notice = "文件已准备，接收端可参与分发，也可独立执行命令"
	a.mu.Unlock()
	return nil
}
func (a *App) startAgent(ctx context.Context, c Config) error {
	a.receiverMu.Lock()
	defer a.receiverMu.Unlock()
	a.mu.Lock()
	if a.config.Mode == "sender" {
		a.mu.Unlock()
		return errors.New("当前实例是分发模式，不能同时启动接收服务")
	}
	previous := a.agent
	a.agent = nil
	a.mu.Unlock()
	if previous != nil {
		previous.close()
	}
	if c.Discover || c.SenderAddr == "" {
		addr, e := a.lookupSender(1500*time.Millisecond, "")
		if e != nil {
			return fmt.Errorf("自动发现失败，请填写发送端地址: %w", e)
		}
		c.SenderAddr = addr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	baseline := a.config.WorkDir
	a.mu.Unlock()
	root, err := filepath.Abs(baseline)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("程序工作目录不可用: %w", err)
	}
	st, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return errors.New("程序工作目录不是目录")
	}
	pool := mempool.NewMemPool(_const.ChunkSize)
	s := server.NewGspSession(":0", pool)
	sdk := client.NewGspSdk(c.SenderAddr, pool)
	agentCtx, agentCancel := context.WithCancel(context.Background())
	r := &agent{root: root, token: c.Token, allowCommands: c.AllowCommands, senderAddr: c.SenderAddr, session: s, sdk: sdk, ctx: agentCtx, stop: agentCancel, task: TaskState{Status: "idle"}, commandTask: TaskState{Status: "idle"}}
	s.SetControlHandler(r.control)
	if err = s.Start(); err != nil {
		r.close()
		return err
	}
	dialSDK := sdk.WithContext(ctx)
	if _, err = dialSDK.GetNodeStatus(); err != nil {
		r.close()
		return fmt.Errorf("连接发送端 %s 失败: %w", c.SenderAddr, err)
	}
	if err = dialSDK.PeerReg(s.Port(), s.UUID); err != nil {
		r.close()
		return err
	}
	if err = ctx.Err(); err != nil {
		r.close()
		return err
	}
	a.mu.Lock()
	a.agent = r
	a.config.Mode = "receiver"
	a.config.SenderAddr = c.SenderAddr
	a.config.WorkDir = root
	a.config.Token = c.Token
	a.config.AllowCommands = c.AllowCommands
	a.mu.Unlock()
	r.watchWG.Add(1)
	go func() {
		defer r.watchWG.Done()
		sdk.Watch(nil, func() { r.mu.Lock(); r.disconnected = true; r.mu.Unlock() })
	}()
	return nil
}
func (a *App) stopRole(_ context.Context, role string) error {
	if role == "sender" {
		a.stopDistribution()
	}
	if role != "sender" {
		a.stopDiscovery()
		a.receiverMu.Lock()
		defer a.receiverMu.Unlock()
	}
	a.mu.Lock()
	s, r := a.source, a.agent
	var hub *server.Session
	var md *mdns.Server
	if role == "sender" {
		hub, md = a.hub, a.hubMDNS
		a.hub = nil
		a.hubMDNS = nil
		a.source = nil
		a.probes = map[string]PeerState{}
		a.probeKey = ""
	} else {
		a.agent = nil
	}
	a.mu.Unlock()
	if role == "sender" {
		if hub != nil {
			hub.Stop()
		}
		if md != nil {
			_ = md.Shutdown()
		}
		if s != nil {
			s.close()
		}
	}
	if role != "sender" && r != nil {
		r.close()
		r.mu.Lock()
		task, command := r.task, r.commandTask
		r.mu.Unlock()
		a.mu.Lock()
		a.lastReceiverTask = &task
		a.lastReceiverCommand = &command
		a.mu.Unlock()
	}
	return nil
}

type Dispatch struct {
	IDs       []string `json:"ids"`
	Target    string   `json:"target"`
	Token     string   `json:"token"`
	Overwrite bool     `json:"overwrite"`
}

func (a *App) spec(d Dispatch) (*source, TaskSpec, string, error) {
	a.mu.Lock()
	s := a.source
	a.mu.Unlock()
	if s == nil {
		return nil, TaskSpec{}, "", errors.New("发送服务尚未启动")
	}
	ck := s.session.GetChunk()
	spec := TaskSpec{ID: s.generation, Target: d.Target, Overwrite: d.Overwrite, Archive: s.data.Archive, Entries: s.data.Entries, Meta: model.GetFileStatusResp{FileName: ck.FileStat.Name(), FileSize: ck.FileStat.Size(), ChunkSize: ck.GetChunkSize(), ChunkNum: ck.GetChunkNum()}}
	// Canonical JSON binds probes to the exact source, destination and permissions.
	d.IDs = append([]string(nil), d.IDs...)
	sort.Strings(d.IDs)
	raw, _ := json.Marshal(struct {
		Spec     TaskSpec
		Dispatch Dispatch
	}{spec, d})
	return s, spec, string(raw), nil
}
func selected(ids []string, id string) bool {
	for _, v := range ids {
		if id == v {
			return true
		}
	}
	return false
}
func (a *App) probe(ctx context.Context, d Dispatch) error {
	s, spec, fingerprint, err := a.spec(d)
	if err != nil {
		return err
	}
	if len(d.IDs) == 0 {
		return errors.New("请至少选择一台接收机器")
	}
	peers := s.session.Snapshot()
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	results := map[string]PeerState{}
	for _, p := range peers {
		if p.Self || !selected(d.IDs, p.UUID) {
			continue
		}
		wg.Add(1)
		go func(p model.PeerInfo) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			ps := PeerState{PeerInfo: p}
			sdk := client.NewGspSdk(p.Addr, mempool.NewMemPool(_const.ChunkSize)).WithContext(ctx)
			defer sdk.Close()
			var probe transfer.Probe
			e := sdk.Call("probe", d.Token, spec, &probe)
			if e != nil {
				ps.Probe = e.Error()
			} else {
				ps.Ready = probe.Ready
				ps.Probe = probe.Target
				ps.Warnings = probe.Warnings
			}
			mu.Lock()
			results[p.UUID] = ps
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	if err = ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	for id, p := range results {
		old := a.probes[id]
		p.Command = old.Command
		p.Task = old.Task
		a.probes[id] = p
	}
	a.probeKey = fingerprint
	a.mu.Unlock()
	ready := 0
	for _, p := range results {
		if p.Ready {
			ready++
		}
	}
	a.mu.Lock()
	a.notice = fmt.Sprintf("预探测完成：%d 台可参与，%d 台排除", ready, len(d.IDs)-ready)
	a.mu.Unlock()
	return nil
}
func (a *App) dispatch(ctx context.Context, d Dispatch) error {
	_, spec, fingerprint, err := a.spec(d)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if a.distribution != nil && a.distribution.Active {
		a.mu.Unlock()
		return errors.New("无条件分发已开启，请先停止接纳新设备")
	}
	valid := a.probeKey == fingerprint
	probes := make(map[string]PeerState, len(a.probes))
	for id, p := range a.probes {
		probes[id] = p
	}
	a.mu.Unlock()
	if !valid {
		return errors.New("文件、目标位置或选择已变化，请先重新预探测")
	}
	spec.ID = key()
	sent := 0
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 8)
	for id, p := range clonePeers(probes) {
		if !p.Ready || !selected(d.IDs, id) {
			continue
		}
		wg.Add(1)
		go func(id string, p PeerState) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			sdk := client.NewGspSdk(p.Addr, mempool.NewMemPool(_const.ChunkSize)).WithContext(ctx)
			defer sdk.Close()
			var state TaskState
			e := sdk.Call("start", d.Token, spec, &state)
			mu.Lock()
			if e == nil {
				sent++
				p.Task = &state
			} else {
				p.Ready = false
				p.Probe = e.Error()
				p.Task = &TaskState{ID: spec.ID, Status: "failed", Error: e.Error()}
			}
			probes[id] = p
			mu.Unlock()
		}(id, p)
	}
	wg.Wait()
	if err = ctx.Err(); err != nil {
		a.cancelRemote(d, probes)
		return err
	}
	a.mu.Lock()
	for id, p := range probes {
		p.Command = a.probes[id].Command
		a.probes[id] = p
	}
	a.probeKey = ""
	a.notice = fmt.Sprintf("已启动 %d 台机器；未通过预探测的机器不参与", sent)
	a.mu.Unlock()
	if sent == 0 {
		return errors.New("没有可启动的机器，请查看预探测结果")
	}
	return a.monitor(ctx, d, probes)
}
func (a *App) cancelRemote(d Dispatch, peers map[string]PeerState) {
	var wg sync.WaitGroup
	for _, p := range peers {
		if p.Task == nil || p.Task.ID == "" {
			continue
		}
		wg.Add(1)
		go func(p PeerState) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			sdk := client.NewGspSdk(p.Addr, mempool.NewMemPool(_const.ChunkSize)).WithContext(ctx)
			defer sdk.Close()
			_ = sdk.Call("cancel", d.Token, map[string]string{"id": p.Task.ID}, nil)
		}(p)
	}
	wg.Wait()
}
func (a *App) state() any {
	a.mu.Lock()
	defer a.mu.Unlock()
	var operation *Operation
	if a.op != nil {
		copy := *a.op
		operation = &copy
	}
	out := map[string]any{"config": a.config, "operation": operation, "notice": a.notice, "peers": []PeerState{}, "addresses": localAddresses(), "discovery": map[string]any{"active": a.discoveryDone != nil, "message": a.discoveryMessage}}
	if a.commandOp != nil {
		copy := *a.commandOp
		out["commandOperation"] = &copy
	}
	if a.distribution != nil {
		copy := *a.distribution
		out["distribution"] = &copy
	}
	if a.source != nil {
		s := a.source
		out["source"] = map[string]any{"name": s.data.Name, "size": s.data.Size, "archive": s.data.Archive, "count": len(s.data.Entries), "port": s.session.Port()}
	}
	if a.hub != nil {
		out["sender"] = map[string]any{"port": a.hub.Port()}
		peers := []PeerState{}
		seen := map[string]bool{}
		for _, p := range a.hub.Snapshot() {
			if p.Self {
				continue
			}
			ps := a.probes[p.UUID]
			ps.PeerInfo = p
			peers = append(peers, ps)
			seen[p.UUID] = true
		}
		for id, ps := range a.probes {
			if seen[id] {
				continue
			}
			ps.Online = false
			if ps.Task != nil {
				task := *ps.Task
				task.Status = "ended"
				task.Message = "节点已下线，任务结束"
				ps.Task = &task
			}
			peers = append(peers, ps)
		}
		sort.Slice(peers, func(i, j int) bool { return peers[i].UUID < peers[j].UUID })
		out["peers"] = peers
	}
	if a.agent != nil {
		r := a.agent
		r.mu.Lock()
		out["receiver"] = map[string]any{"workingDir": r.root, "address": r.senderAddr, "port": r.session.Port(), "online": true, "disconnected": r.disconnected, "task": r.task, "command": r.commandTask}
		r.mu.Unlock()
	} else if a.lastReceiverTask != nil {
		out["receiver"] = map[string]any{"online": false, "address": a.config.SenderAddr, "task": *a.lastReceiverTask, "command": a.lastReceiverCommand}
	}

	return out
}
func localAddresses() []string {
	out := []string{}
	ifaces, _ := net.InterfaceAddrs()
	for _, addr := range ifaces {
		ip, _, e := net.ParseCIDR(addr.String())
		if e == nil && !ip.IsLoopback() && ip.To4() != nil {
			out = append(out, ip.String())
		}
	}
	return out
}

// AutoStart preserves command-line workflows while always exposing the WebUI.
func (a *App) AutoStart() error {
	c := a.config
	if c.Mode == "sender" {
		_, err := a.begin("sender.start", func(ctx context.Context) error {
			if err := a.ensureSender(ctx, c.Listen); err != nil {
				return err
			}
			if c.File != "" {
				return a.startSource(ctx, c.File, c.Listen)
			}
			return nil
		})
		return err
	}
	if c.Mode == "receiver" {
		return a.startDiscovery(c)
	}

	return nil
}

func clonePeers(peers map[string]PeerState) map[string]PeerState {
	out := make(map[string]PeerState, len(peers))
	for id, p := range peers {
		out[id] = p
	}
	return out
}

func (a *App) selectMode(mode string) error {
	if mode != "" && mode != "sender" && mode != "receiver" {
		return errors.New("请选择分发或接收模式")
	}
	a.mu.Lock()
	if a.hub != nil || a.source != nil || a.agent != nil || a.discoveryDone != nil {
		a.mu.Unlock()
		return errors.New("请先停止当前服务，再切换模式")
	}
	a.config.Mode = mode
	a.probes = map[string]PeerState{}
	a.probeKey = ""
	a.notice = ""
	c := a.config
	a.mu.Unlock()
	if mode == "receiver" {
		return a.startDiscovery(c)
	}
	if mode == "sender" {
		return a.ensureSender(context.Background(), c.Listen)
	}
	return nil
}
