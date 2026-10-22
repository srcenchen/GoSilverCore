package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	a, err := New(Config{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}
func startPair(t *testing.T, folder bool) (*App, []*App, Dispatch, []byte) {
	t.Helper()
	sender := newTestApp(t)
	root := t.TempDir()
	path := filepath.Join(root, "payload.bin")
	data := bytes.Repeat([]byte("silver"), 1500000)
	if folder {
		path = filepath.Join(root, "folder")
		if err := os.MkdirAll(filepath.Join(path, "empty"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "data.bin"), data, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "empty.txt"), nil, 0644); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := sender.startSource(ctx, path, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", sender.source.session.Port())
	var receivers []*App
	d := Dispatch{Target: "target", Token: "test-token"}
	for range 3 {
		r := newTestApp(t)
		if err := r.startAgent(ctx, Config{SenderAddr: addr, WorkDir: r.config.WorkDir, Token: d.Token, AllowCommands: true}); err != nil {
			t.Fatal(err)
		}
		receivers = append(receivers, r)
		d.IDs = append(d.IDs, r.agent.session.UUID)
	}
	// Wait for registration to become visible on the tracker.
	until := time.Now().Add(time.Second)
	for len(sender.source.session.Snapshot()) < 4 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	return sender, receivers, d, data
}
func TestPreflightExcludesInvalidTargetAndTransfersToOtherPeers(t *testing.T) {
	sender, receivers, d, data := startPair(t, false)
	bad := filepath.Join(receivers[2].config.WorkDir, "target")
	if err := os.WriteFile(bad, []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := sender.probe(ctx, d); err != nil {
		t.Fatal(err)
	}
	if sender.probes[d.IDs[2]].Ready {
		t.Fatal("invalid target passed probe")
	}
	if err := sender.dispatch(ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, r := range receivers[:2] {
		got, err := os.ReadFile(filepath.Join(r.config.WorkDir, "target", "payload.bin"))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("payload mismatch: %v", err)
		}
		if taskSnapshot(r.agent).Status != "seeding" {
			t.Fatal(taskSnapshot(r.agent))
		}
	}
	if taskSnapshot(receivers[2].agent).Status != "idle" {
		t.Fatal("excluded peer participated")
	}
}
func TestFolderRoundTripKeepsSeeding(t *testing.T) {
	sender, receivers, d, data := startPair(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := sender.probe(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := sender.dispatch(ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, r := range receivers {
		got, err := os.ReadFile(filepath.Join(r.config.WorkDir, "target", "folder", "data.bin"))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("folder data mismatch: %v", err)
		}
		info, err := os.Stat(filepath.Join(r.config.WorkDir, "target", "folder", "empty"))
		if err != nil || !info.IsDir() {
			t.Fatalf("empty directory lost: %v", err)
		}
	}
	// A completed receiver must still serve chunks to later peers.
	if !receivers[0].agent.session.HasChunk(0) {
		t.Fatal("completed receiver lost its chunks")
	}
}
func TestProbeInvalidatedByTargetChange(t *testing.T) {
	sender, _, d, _ := startPair(t, false)
	ctx := context.Background()
	if err := sender.probe(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Target = "different"
	if err := sender.dispatch(ctx, d); err == nil {
		t.Fatal("stale probe accepted")
	}
}
func TestCommandAndCancellation(t *testing.T) {
	sender, receivers, d, _ := startPair(t, false)
	d.IDs = d.IDs[:1]
	ctx := context.Background()
	if err := sender.command(ctx, d, Command{Text: "printf 'command-ok'", Directory: ".", Timeout: 5}); err != nil {
		t.Fatal(err)
	}
	r := receivers[0].agent
	if commandSnapshot(r).Output != "command-ok" {
		t.Fatal(commandSnapshot(r).Output)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sender.command(ctx, d, Command{Text: "sleep 20", Directory: ".", Timeout: 30}) }()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		r.mu.Lock()
		running := r.commandTask.Status == "running"
		r.mu.Unlock()
		if running {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("remote command cancellation hung")
	}
	until = time.Now().Add(time.Second)
	for time.Now().Before(until) {
		r.mu.Lock()
		running := r.commandTask.Status == "running"
		r.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("remote command still running")
}
func TestHTTPProtectionAndStreamingFolderUpload(t *testing.T) {
	a := newTestApp(t)
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("unprotected API")
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.Header.Set("X-GoSilver-Token", a.csrf)
	req.Header.Set("Origin", "https://hostile.example")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross origin accepted")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("folder/sub/file.txt", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, "uploaded")
	_ = mw.Close()
	req, _ = http.NewRequest("POST", srv.URL+"/api/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-GoSilver-Token", a.csrf)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		t.Fatal(string(data))
	}
	var out struct {
		Path string `json:"path"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out.Path, "sub", "file.txt"))
	if err != nil || string(got) != "uploaded" {
		t.Fatalf("folder upload mismatch: %s %v", got, err)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/api/action", strings.NewReader(`{"action":"sender.start","path":"`+filepath.ToSlash(out.Path)+`","listen":"127.0.0.1:0"}`))
	req.Header.Set("X-GoSilver-Token", a.csrf)
	started := time.Now()
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 || time.Since(started) > time.Second {
		t.Fatalf("action not asynchronous: %d", resp.StatusCode)
	}
}

func taskSnapshot(r *agent) TaskState { r.mu.Lock(); defer r.mu.Unlock(); return r.task }

func TestStateSnapshotsDoNotRaceWithOperationCompletion(t *testing.T) {
	a := newTestApp(t)
	done := make(chan struct{})
	_, err := a.begin("test", func(context.Context) error { <-done; return nil })
	if err != nil {
		t.Fatal(err)
	}
	snapshot := a.state()
	close(done)
	for range 100 {
		if _, err = json.Marshal(snapshot); err != nil {
			t.Fatal(err)
		}
		if _, err = json.Marshal(a.state()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFreshDiscoveryReconnectsAndModesAreExclusive(t *testing.T) {
	sender, receivers, _, _ := startPair(t, false)
	r := receivers[0]
	oldID := r.agent.session.UUID
	calls := 0
	addr := fmt.Sprintf("127.0.0.1:%d", sender.source.session.Port())
	r.lookupSender = func(time.Duration, string) (string, error) { calls++; return addr, nil }
	if err := r.startAgent(context.Background(), Config{Discover: true, SenderAddr: "127.0.0.1:1", AllowCommands: true}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || r.agent.senderAddr != addr || r.agent.session.UUID == oldID {
		t.Fatal("reconnect reused the old discovery or receiver")
	}
	if err := sender.startAgent(context.Background(), Config{SenderAddr: addr}); err == nil {
		t.Fatal("sender simultaneously entered receiver mode")
	}
	if err := r.startSource(context.Background(), sender.config.File, "127.0.0.1:0"); err == nil {
		t.Fatal("receiver simultaneously entered sender mode")
	}
	if err := sender.selectMode("receiver"); err == nil {
		t.Fatal("switched mode while source active")
	}
	if err := sender.stopRole(context.Background(), "sender"); err != nil {
		t.Fatal(err)
	}
	if err := sender.selectMode("receiver"); err != nil {
		t.Fatal(err)
	}
}
func TestSenderChoosesAbsoluteDestinationAndCommandNeedsNoToken(t *testing.T) {
	sender, receivers, d, data := startPair(t, false)
	r := receivers[0]
	addr := fmt.Sprintf("127.0.0.1:%d", sender.source.session.Port())
	if err := r.startAgent(context.Background(), Config{SenderAddr: addr, AllowCommands: true}); err != nil {
		t.Fatal(err)
	}
	d.Token = ""
	d.IDs = []string{r.agent.session.UUID}
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		found := false
		for _, p := range sender.source.session.Snapshot() {
			if p.UUID == d.IDs[0] {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(time.Millisecond)
	}
	d.Target = t.TempDir()
	if err := sender.probe(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := sender.dispatch(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(d.Target, "payload.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("sender-selected absolute path failed: %v", err)
	}
	if err := sender.command(context.Background(), d, Command{Text: "printf 'default-command-ok'", Directory: d.Target, Timeout: 5}); err != nil {
		t.Fatal(err)
	}
	if commandSnapshot(r.agent).Output != "default-command-ok" {
		t.Fatal(taskSnapshot(r.agent))
	}
}

func TestContinuousDiscoveryConnectsAfterSenderAppearsAndReconnects(t *testing.T) {
	receiver := newTestApp(t)
	sender := newTestApp(t)
	path := filepath.Join(t.TempDir(), "auto.txt")
	if err := os.WriteFile(path, []byte("automatic"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	address := ""
	searches := 0
	receiver.lookupSender = func(time.Duration, string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		searches++
		if address == "" {
			return "", errors.New("not online yet")
		}
		return address, nil
	}
	if err := receiver.selectMode("receiver"); err != nil {
		t.Fatal(err)
	}
	waitUntil := func(message string, test func() bool) {
		t.Helper()
		until := time.Now().Add(8 * time.Second)
		for time.Now().Before(until) {
			if test() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal(message)
	}
	waitUntil("receiver did not search automatically", func() bool { mu.Lock(); defer mu.Unlock(); return searches > 0 })
	if err := sender.startSource(context.Background(), path, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	address = fmt.Sprintf("127.0.0.1:%d", sender.source.session.Port())
	mu.Unlock()
	waitUntil("receiver did not connect when sender appeared", func() bool { receiver.mu.Lock(); defer receiver.mu.Unlock(); return receiver.agent != nil })
	receiver.mu.Lock()
	oldID := receiver.agent.session.UUID
	receiver.mu.Unlock()
	if err := sender.stopRole(context.Background(), "sender"); err != nil {
		t.Fatal(err)
	}
	if err := sender.startSource(context.Background(), path, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	address = fmt.Sprintf("127.0.0.1:%d", sender.source.session.Port())
	mu.Unlock()
	waitUntil("receiver did not reconnect automatically", func() bool {
		receiver.mu.Lock()
		defer receiver.mu.Unlock()
		return receiver.agent != nil && receiver.agent.session.UUID != oldID
	})
	if err := receiver.stopRole(context.Background(), "receiver"); err != nil {
		t.Fatal(err)
	}
	receiver.mu.Lock()
	active := receiver.discoveryDone != nil
	agent := receiver.agent
	receiver.mu.Unlock()
	if active || agent != nil {
		t.Fatal("stop did not disable polling and disconnect")
	}
}

func commandSnapshot(r *agent) TaskState { r.mu.Lock(); defer r.mu.Unlock(); return r.commandTask }
