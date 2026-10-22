package webui

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/internal/gsp_sdk/server"
	"go-silver-core/internal/receiver"
	"go-silver-core/internal/transfer"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type TaskSpec struct {
	ID        string                  `json:"id"`
	Target    string                  `json:"target"`
	Overwrite bool                    `json:"overwrite"`
	Archive   bool                    `json:"archive"`
	Entries   []transfer.Entry        `json:"entries"`
	Meta      model.GetFileStatusResp `json:"meta"`
}
type TaskState struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Status   string            `json:"status"`
	Message  string            `json:"message"`
	Error    string            `json:"error,omitempty"`
	Output   string            `json:"output,omitempty"`
	Progress receiver.Progress `json:"progress"`
}
type agent struct {
	mu                          sync.Mutex
	root, token, senderAddr     string
	allowCommands, disconnected bool
	session                     *server.Session
	sdk                         client.GspSdk
	ctx                         context.Context
	stop                        context.CancelFunc
	task                        TaskState
	commandTask                 TaskState
	commandCancel               context.CancelFunc
	taskCancel                  context.CancelFunc
	taskWG, watchWG             sync.WaitGroup
	cache                       []string
}

func (r *agent) close() {
	r.stop()
	r.cancelTask()
	r.cancelCommand()
	r.sdk.Close()
	r.taskWG.Wait()
	r.session.Stop()
	r.watchWG.Wait()
	r.mu.Lock()
	if r.task.ID != "" {
		r.task.Status = "ended"
		r.task.Message = "已停止做种并下线，任务结束"
	}
	r.mu.Unlock()
	for _, p := range r.cache {
		os.RemoveAll(p)
	}
}
func (r *agent) cancelTask() {
	r.mu.Lock()
	if r.taskCancel != nil {
		r.taskCancel()
	}
	r.mu.Unlock()
}
func (r *agent) control(ctx context.Context, raw json.RawMessage) (any, error) {
	var req model.ControlRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(r.token)) != 1 {
		return nil, errors.New("接收端控制密钥不匹配")
	}
	switch req.Method {
	case "probe", "start":
		var spec TaskSpec
		if err := json.Unmarshal(req.Payload, &spec); err != nil {
			return nil, err
		}
		r.mu.Lock()
		if req.Method == "start" && r.task.ID == spec.ID && (r.task.Status == "running" || r.task.Status == "seeding") {
			copy := r.task
			r.mu.Unlock()
			return copy, nil
		}
		busy := r.task.Status == "running"
		r.mu.Unlock()
		if busy {
			return nil, errors.New("接收端已有任务正在执行")
		}
		if err := validateSpec(spec); err != nil {
			return nil, err
		}
		extra := int64(0)
		if spec.Archive {
			extra = spec.Meta.FileSize
		}
		probe, err := r.probe(spec, extra)
		if err != nil {
			return nil, err
		}
		if req.Method == "probe" {
			return probe, nil
		}
		// Re-read source metadata so a stale preview cannot start a different file.
		sdk := r.sdk.WithContext(ctx)
		meta, err := sdk.GetFileStatus()
		if err != nil {
			return nil, err
		}
		if meta != spec.Meta {
			return nil, errors.New("发送端文件已变化，请重新预探测")
		}
		return r.launch("download", spec.ID, func(ctx context.Context) error { return r.download(ctx, spec) })
	case "browse":
		var q struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(req.Payload, &q); err != nil {
			return nil, err
		}
		p, err := r.resolve(q.Path)
		if err != nil {
			return nil, err
		}
		return listDirectory(p, "", true)
	case "status":
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.task, nil
	case "command.status":
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.commandTask, nil
	case "command.cancel":
		var q struct {
			ID string `json:"id"`
		}
		if len(req.Payload) > 0 && string(req.Payload) != "null" {
			if err := json.Unmarshal(req.Payload, &q); err != nil {
				return nil, err
			}
		}
		r.mu.Lock()
		if q.ID == "" || q.ID == r.commandTask.ID {
			if r.commandCancel != nil {
				r.commandCancel()
			}
		}
		r.mu.Unlock()
		return map[string]bool{"cancelled": true}, nil
	case "cancel":
		var q struct {
			ID string `json:"id"`
		}
		if len(req.Payload) > 0 && string(req.Payload) != "null" {
			if err := json.Unmarshal(req.Payload, &q); err != nil {
				return nil, err
			}
		}
		r.mu.Lock()
		if q.ID == "" || q.ID == r.task.ID {
			if r.taskCancel != nil {
				r.taskCancel()
			}
		}
		r.mu.Unlock()
		return map[string]bool{"cancelled": true}, nil
	case "command":
		if !r.allowCommands {
			return nil, errors.New("接收端未启用命令执行")
		}
		var c Command
		if err := json.Unmarshal(req.Payload, &c); err != nil {
			return nil, err
		}
		if strings.TrimSpace(c.Text) == "" {
			return nil, errors.New("命令不能为空")
		}
		dir, err := r.resolve(c.Directory)
		if err != nil {
			return nil, err
		}
		c.Directory = dir
		if c.ID == "" {
			c.ID = key()
		}
		return r.launchCommand(c)
	default:
		return nil, fmt.Errorf("unknown receiver operation %q", req.Method)
	}
}
func validateSpec(s TaskSpec) error {
	if s.ID == "" {
		return errors.New("缺少任务标识")
	}
	if err := transfer.ValidateEntries(s.Entries); err != nil {
		return err
	}
	if s.Meta.FileName == "" || filepath.Base(s.Meta.FileName) != s.Meta.FileName {
		return errors.New("无效文件名称")
	}
	if !s.Archive && (len(s.Entries) != 1 || s.Entries[0].Dir || s.Entries[0].Size != s.Meta.FileSize) {
		return errors.New("文件清单与元信息不符")
	}
	return nil
}
func (r *agent) launch(kind, id string, fn func(context.Context) error) (TaskState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return TaskState{}, context.Canceled
	}
	if r.task.Status == "running" {
		return TaskState{}, errors.New("接收端正忙")
	}
	ctx, cancel := context.WithCancel(r.ctx)
	r.taskCancel = cancel
	r.task = TaskState{ID: id, Kind: kind, Status: "running", Message: "任务已启动"}
	snapshot := r.task
	r.taskWG.Add(1)
	go func() {
		defer r.taskWG.Done()
		defer cancel()
		err := fn(ctx)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.task.Status = "seeding"
		r.task.Message = "接收完成，正在做种，为其他机器提供文件块"
		if err != nil {
			r.task.Status = "failed"
			r.task.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				r.task.Status = "cancelled"
			}
		}
	}()
	return snapshot, nil
}
func (r *agent) download(ctx context.Context, spec TaskSpec) error {
	dir, err := r.resolve(spec.Target)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	var target string
	var cachePath string
	if spec.Archive {
		cache, e := os.MkdirTemp(dir, ".gosilver-cache-*")
		if e != nil {
			return e
		}
		r.mu.Lock()
		r.cache = append(r.cache, cache)
		cachePath = cache
		r.mu.Unlock()
		target = filepath.Join(cache, spec.Meta.FileName)
	} else {
		target, err = transfer.Resolve("", filepath.Join(dir, filepath.FromSlash(spec.Entries[0].Name)))
		if err != nil {
			return err
		}
	}
	err = receiver.RunTo(ctx, r.senderAddr, target, r.session, r.sdk, spec.Meta, func(p receiver.Progress) {
		r.mu.Lock()
		r.task.Progress = p
		r.task.Message = "正在接收"
		if p.Finished && spec.Archive {
			r.task.Message = "正在展开文件夹"
		}
		r.mu.Unlock()
	}, spec.Overwrite)
	if err != nil {
		if cachePath != "" {
			r.removeCache(cachePath)
		}
		return err
	}
	r.pruneCaches(cachePath)
	if spec.Archive {
		if err = transfer.Extract(ctx, target, "", dir, spec.Entries, spec.Overwrite); err != nil {
			return err
		}
	}

	return nil
}

type Command struct {
	ID        string `json:"id,omitempty"`
	Text      string `json:"text"`
	Directory string `json:"directory"`
	Timeout   int    `json:"timeout"`
}
type limitedOutput struct {
	mu        sync.Mutex
	b         bytes.Buffer
	truncated bool
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	left := (256 << 10) - w.b.Len()
	if len(p) > left {
		p = p[:max(0, left)]
		w.truncated = true
	}
	_, _ = w.b.Write(p)
	return n, nil
}
func (r *agent) execute(ctx context.Context, c Command) error {
	if !r.allowCommands {
		return errors.New("接收端未启用命令执行")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 60
	}
	if timeout > 300 {
		timeout = 300
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	shell, args := "/bin/sh", []string{"-c", c.Text}
	if runtime.GOOS == "windows" {
		shell, args = "cmd.exe", []string{"/C", c.Text}
	}
	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Dir = c.Directory
	cmd.WaitDelay = 2 * time.Second
	configureCommand(cmd)
	output := &limitedOutput{}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	text := output.b.String()
	if output.truncated {
		text += "\n[输出超过 256 KiB，已截断]"
	}
	r.mu.Lock()
	r.commandTask.Output = text
	r.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
func (a *App) command(ctx context.Context, d Dispatch, c Command) error {
	a.mu.Lock()
	s := a.hub
	a.mu.Unlock()
	if s == nil {
		return errors.New("请先启动发送服务并连接接收端")
	}
	if len(d.IDs) == 0 {
		return errors.New("请先选择接收机器")
	}
	peers := map[string]PeerState{}
	c.ID = key()
	for _, p := range s.Snapshot() {
		if p.Self || !selected(d.IDs, p.UUID) {
			continue
		}
		sdk := client.NewGspSdk(p.Addr, s.GetMemPool()).WithContext(ctx)
		var state TaskState
		err := sdk.Call("command", d.Token, c, &state)
		sdk.Close()
		ps := PeerState{PeerInfo: p, Command: &state}
		if err != nil {
			ps.Command = &TaskState{ID: c.ID, Kind: "command", Status: "failed", Error: err.Error()}
		}
		peers[p.UUID] = ps
	}
	if len(peers) == 0 {
		return errors.New("所选机器均已离线")
	}
	for _, p := range peers {
		a.storePeer(p, true)
	}
	return a.monitor(ctx, d, peers, true)
}

// The sender owns destination selection. Relative paths use the receiver's
// working directory; absolute paths may target any directory writable by it.
func (r *agent) resolve(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(r.root, dir)
	}
	dir = filepath.Clean(dir)
	ancestor := dir
	var suffix []string
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", errors.New("目标目录不可用")
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
	}
	base, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		base = filepath.Join(base, suffix[i])
	}
	return base, nil
}
func (r *agent) probe(spec TaskSpec, extra int64) (transfer.Probe, error) {
	dir, err := r.resolve(spec.Target)
	if err != nil {
		return transfer.Probe{}, err
	}
	return transfer.ProbeTarget("", dir, spec.Entries, spec.Overwrite, extra)
}

func (r *agent) removeCache(path string) {
	r.mu.Lock()
	var keep []string
	for _, p := range r.cache {
		if p != path {
			keep = append(keep, p)
		}
	}
	r.cache = keep
	r.mu.Unlock()
	_ = os.RemoveAll(path)
}

// The previous seed descriptor is closed by BeSendSub before pruning its cache.
func (r *agent) pruneCaches(current string) {
	r.mu.Lock()
	var old []string
	var keep []string
	for _, p := range r.cache {
		if p == current {
			keep = append(keep, p)
		} else {
			old = append(old, p)
		}
	}
	r.cache = keep
	r.mu.Unlock()
	for _, p := range old {
		_ = os.RemoveAll(p)
	}
}

func (r *agent) cancelCommand() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.commandCancel != nil {
		r.commandCancel()
	}
}
func (r *agent) launchCommand(c Command) (TaskState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return TaskState{}, context.Canceled
	}
	if r.commandTask.Status == "running" {
		return TaskState{}, errors.New("已有命令正在执行")
	}
	ctx, cancel := context.WithCancel(r.ctx)
	r.commandCancel = cancel
	r.commandTask = TaskState{ID: c.ID, Kind: "command", Status: "running", Message: "正在执行命令"}
	copy := r.commandTask
	r.taskWG.Add(1)
	go func() {
		defer r.taskWG.Done()
		defer cancel()
		err := r.execute(ctx, c)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.commandTask.Status = "completed"
		r.commandTask.Message = "命令执行完成"
		if err != nil {
			r.commandTask.Status = "failed"
			r.commandTask.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				r.commandTask.Status = "cancelled"
			}
		}
	}()
	return copy, nil
}
