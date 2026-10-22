package webui

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/internal/transfer"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed static/*
var assets embed.FS

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"token": a.csrf}) })
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.state()) })
	mux.HandleFunc("GET /api/events", a.events)
	mux.HandleFunc("GET /api/local", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if p == "" {
			a.mu.Lock()
			p = a.config.WorkDir
			a.mu.Unlock()
		}
		listing, err := listDirectory(p, "", false)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		writeJSON(w, 200, listing)
	})
	mux.HandleFunc("POST /api/upload", a.upload)
	mux.HandleFunc("POST /api/cancel", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Scope string `json:"scope"`
		}
		if r.ContentLength != 0 {
			if err := decode(w, r, &req); err != nil {
				writeError(w, 400, err)
				return
			}
		}
		a.cancel(req.Scope)
		writeJSON(w, 202, map[string]string{"status": "cancelling"})
	})
	mux.HandleFunc("POST /api/action", a.action)
	mux.HandleFunc("POST /api/browse", a.browse)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, _ := assets.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(string(data), "__CSRF__", a.csrf)))
	})
	sub, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		host := r.Host
		if h, _, e := net.SplitHostPort(host); e == nil {
			host = h
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			writeError(w, 403, errors.New("WebUI 仅允许本机访问"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host {
				writeError(w, 403, errors.New("跨站请求已拒绝"))
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, 403, errors.New("跨站请求已拒绝"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/session" {
			token := r.Header.Get("X-GoSilver-Token")
			if r.URL.Path == "/api/events" {
				token = r.URL.Query().Get("token")
			}
			if subtle.ConstantTimeCompare([]byte(token), []byte(a.csrf)) != 1 {
				writeJSON(w, 403, map[string]string{"code": "session_expired", "error": "正在恢复页面会话，请稍候"})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}

type actionRequest struct {
	Mode     string   `json:"mode"`
	Action   string   `json:"action"`
	Path     string   `json:"path"`
	Listen   string   `json:"listen"`
	Config   Config   `json:"config"`
	Dispatch Dispatch `json:"dispatch"`
	Command  Command  `json:"command"`
}

func (a *App) action(w http.ResponseWriter, r *http.Request) {
	var req actionRequest
	if err := decode(w, r, &req); err != nil {
		writeError(w, 400, err)
		return
	}
	var fn func(context.Context) error
	switch req.Action {
	case "mode.select":
		fn = func(context.Context) error { return a.selectMode(req.Mode) }
	case "mode.reset":
		fn = func(context.Context) error { return a.selectMode("") }
	case "sender.start":
		listen := req.Listen
		if listen == "" {
			listen = ":0"
		}
		fn = func(ctx context.Context) error {
			if req.Path == "" {
				return a.ensureSender(ctx, listen)
			}
			return a.startSource(ctx, req.Path, listen)
		}
	case "receiver.start":
		fn = func(context.Context) error { return a.startDiscovery(req.Config) }
	case "receiver.manual":
		fn = func(ctx context.Context) error {
			a.stopDiscovery()
			req.Config.Discover = false
			if err := a.startAgent(ctx, req.Config); err != nil {
				return err
			}
			return a.startDiscovery(req.Config)
		}
	case "file.stop":
		fn = func(context.Context) error { return a.stopFile() }
	case "dispatch.auto":
		fn = func(context.Context) error { return a.startDistribution(req.Dispatch) }
	case "dispatch.stop":
		fn = func(context.Context) error { a.pauseDistribution(); return nil }
	case "sender.stop":
		fn = func(ctx context.Context) error { return a.stopRole(ctx, "sender") }
	case "receiver.stop":
		fn = func(ctx context.Context) error { return a.stopRole(ctx, "receiver") }
	case "probe":
		fn = func(ctx context.Context) error { return a.probe(ctx, req.Dispatch) }
	case "dispatch":
		fn = func(ctx context.Context) error { return a.dispatch(ctx, req.Dispatch) }
	case "command":
		fn = func(ctx context.Context) error { return a.command(ctx, req.Dispatch, req.Command) }
	default:
		writeError(w, 400, errors.New("未知操作"))
		return
	}
	var op Operation
	var err error
	if req.Action == "command" {
		op, err = a.beginCommand(fn)
	} else {
		op, err = a.begin(req.Action, fn)
	}
	if err != nil {
		writeError(w, 409, err)
		return
	}
	writeJSON(w, 202, op)
}
func (a *App) browse(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peer  string `json:"peer"`
		Token string `json:"token"`
		Path  string `json:"path"`
	}
	if err := decode(w, r, &req); err != nil {
		writeError(w, 400, err)
		return
	}
	a.mu.Lock()
	s := a.hub
	a.mu.Unlock()
	if s == nil {
		writeError(w, 400, errors.New("发送服务未启动"))
		return
	}
	var address string
	for _, p := range s.Snapshot() {
		if p.UUID == req.Peer && !p.Self {
			address = p.Addr
			break
		}
	}
	if address == "" {
		writeError(w, 400, errors.New("机器已离线"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	sdk := client.NewGspSdk(address, s.GetMemPool()).WithContext(ctx)
	defer sdk.Close()
	var out Listing
	if err := sdk.Call("browse", req.Token, map[string]string{"path": req.Path}, &out); err != nil {
		writeError(w, 400, err)
		return
	}
	writeJSON(w, 200, out)
}
func (a *App) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	var last string
	for {
		data, err := json.Marshal(a.state())
		if err != nil {
			return
		}
		if string(data) != last {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err = fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
			last = string(data)
		}
		select {
		case <-a.done:
			return
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

type FileEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}
type Listing struct {
	Path      string      `json:"path"`
	Parent    string      `json:"parent"`
	Root      string      `json:"root"`
	Entries   []FileEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

func listDirectory(p, root string, onlyDirs bool) (Listing, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return Listing{}, err
	}
	if st, e := os.Stat(abs); e == nil && !st.IsDir() {
		abs = filepath.Dir(abs)
	}
	dir, err := os.Open(abs)
	if err != nil {
		return Listing{}, err
	}
	defer dir.Close()
	out := Listing{Path: abs, Root: root, Parent: filepath.Dir(abs), Entries: []FileEntry{}}
	if root != "" && abs == root {
		out.Parent = ""
	}
	// Read in bounded batches instead of allocating a directory of arbitrary size.
	for {
		entries, e := dir.ReadDir(128)
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".gosilver-") || onlyDirs && !entry.IsDir() {
				continue
			}
			info, e := entry.Info()
			if e != nil {
				continue
			}
			out.Entries = append(out.Entries, FileEntry{Name: entry.Name(), Path: filepath.Join(abs, entry.Name()), Dir: entry.IsDir(), Size: info.Size()})
			if len(out.Entries) >= 1000 {
				out.Truncated = true
				break
			}
		}
		if out.Truncated || e == io.EOF {
			break
		}
		if e != nil {
			return Listing{}, e
		}
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].Dir != out.Entries[j].Dir {
			return out.Entries[i].Dir
		}
		return out.Entries[i].Name < out.Entries[j].Name
	})
	return out, nil
}
func (a *App) upload(w http.ResponseWriter, r *http.Request) {
	// Stream multipart contents to disk; never load a folder into RAM.
	r.Body = http.MaxBytesReader(w, r.Body, 64<<30)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, 400, err)
		return
	}
	temp, err := os.MkdirTemp("", "gosilver-upload-")
	if err != nil {
		writeError(w, 500, err)
		return
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(temp)
		}
	}()
	count := 0
	seen := map[string]bool{}
	for {
		part, e := mr.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			writeError(w, 400, e)
			return
		}
		name := part.FormName()
		if !transfer.SafeName(name) || seen[name] {
			part.Close()
			writeError(w, 400, errors.New("上传路径无效或重复"))
			return
		}
		seen[name] = true
		count++
		if count > transfer.MaxEntries {
			writeError(w, 400, errors.New("文件数量过多"))
			return
		}
		target := filepath.Join(temp, filepath.FromSlash(name))
		if e = os.MkdirAll(filepath.Dir(target), 0755); e != nil {
			writeError(w, 400, e)
			return
		}
		f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if e != nil {
			writeError(w, 400, e)
			return
		}
		_, copyErr := transfer.CopyContext(r.Context(), f, part)
		closeErr := f.Close()
		part.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			writeError(w, 400, copyErr)
			return
		}
	}
	if count == 0 {
		writeError(w, 400, errors.New("未选择任何文件"))
		return
	}
	entries, err := os.ReadDir(temp)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	selected := temp
	if len(entries) == 1 {
		selected = filepath.Join(temp, entries[0].Name())
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		writeError(w, 503, net.ErrClosed)
		return
	}
	a.uploads = append(a.uploads, temp)
	a.mu.Unlock()
	keep = true
	writeJSON(w, 200, map[string]any{"path": selected, "count": count})
}
