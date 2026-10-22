package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go-silver-core/internal/chunk"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/pkg/mempool"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitFor(t *testing.T, description string, check func() bool) {
	t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(description)
}
func attachReceiver(t *testing.T, sender *App, token string) *App {
	t.Helper()
	r := newTestApp(t)
	addr := fmt.Sprintf("127.0.0.1:%d", sender.hub.Port())
	if err := r.startAgent(context.Background(), Config{SenderAddr: addr, Token: token, AllowCommands: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "receiver registration missing", func() bool {
		for _, p := range sender.hub.Snapshot() {
			if p.UUID == r.agent.session.UUID {
				return true
			}
		}
		return false
	})
	return r
}
func TestCommandsWithoutFileAndDefaultRandomPorts(t *testing.T) {
	a := newTestApp(t)
	if a.config.Listen != ":0" {
		t.Fatal("default TCP port is fixed")
	}
	if err := a.ensureSender(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if a.hub.Port() == 0 {
		t.Fatal("actual port not assigned")
	}
	r := attachReceiver(t, a, "")
	if err := a.command(context.Background(), Dispatch{IDs: []string{r.agent.session.UUID}}, Command{Text: "printf independent-command", Directory: ".", Timeout: 5}); err != nil {
		t.Fatal(err)
	}
	if a.source != nil || taskSnapshot(r.agent).Status != "idle" || commandSnapshot(r.agent).Output != "independent-command" {
		t.Fatal("command depended on or modified file task")
	}
}
func TestUnconditionalDistributionAdmitsLatePeersAndKeepsSeeders(t *testing.T) {
	a := newTestApp(t)
	path := filepath.Join(t.TempDir(), "payload.bin")
	data := bytes.Repeat([]byte("automatic"), 30000)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.startSource(context.Background(), path, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := a.startDistribution(Dispatch{Target: "dest"}); err != nil {
		t.Fatal(err)
	}
	first := attachReceiver(t, a, "")
	waitFor(t, "first peer never seeded", func() bool { return taskSnapshot(first.agent).Status == "seeding" })
	id := taskSnapshot(first.agent).ID
	bad := newTestApp(t)
	if err := os.WriteFile(filepath.Join(bad.config.WorkDir, "dest"), []byte("not-directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := bad.startAgent(context.Background(), Config{SenderAddr: fmt.Sprintf("127.0.0.1:%d", a.hub.Port()), AllowCommands: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "invalid late receiver not excluded", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		p, ok := a.probes[bad.agent.session.UUID]
		return ok && !p.Ready && p.Probe != ""
	})
	if taskSnapshot(bad.agent).Status != "idle" {
		t.Fatal("invalid peer received file")
	}
	late := attachReceiver(t, a, "")
	waitFor(t, "late peer never joined the existing task", func() bool { return taskSnapshot(late.agent).Status == "seeding" })
	if taskSnapshot(late.agent).ID != id {
		t.Fatal("late receiver entered a different task")
	}
	for _, r := range []*App{first, late} {
		got, err := os.ReadFile(filepath.Join(r.config.WorkDir, "dest", "payload.bin"))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("payload mismatch: %v", err)
		}
	}
	a.pauseDistribution()
	waitFor(t, "paused distribution stopped tracking active receivers", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		p := a.probes[first.agent.session.UUID]
		return p.Task != nil && p.Task.Status == "seeding"
	})
	if taskSnapshot(first.agent).Status != "seeding" {
		t.Fatal("stopping admission ended seeding")
	}
	after := attachReceiver(t, a, "")
	time.Sleep(500 * time.Millisecond)
	if taskSnapshot(after.agent).Status != "idle" {
		t.Fatal("new peer admitted after stop")
	}
}
func TestCommandAndTransferHaveIndependentStateAndCancellation(t *testing.T) {
	a, receivers, d, data := startPair(t, false)
	r := receivers[0]
	d.IDs = d.IDs[:1]
	_, err := a.beginCommand(func(ctx context.Context) error {
		return a.command(ctx, d, Command{Text: "sleep 2; printf concurrent-command", Directory: ".", Timeout: 5})
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "command did not start", func() bool { return commandSnapshot(r.agent).Status == "running" })
	if err = a.probe(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err = a.dispatch(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if taskSnapshot(r.agent).Status != "seeding" {
		t.Fatal("transfer did not keep seeding")
	}
	id := taskSnapshot(r.agent).ID
	waitFor(t, "command did not finish", func() bool { return commandSnapshot(r.agent).Status == "completed" })
	if taskSnapshot(r.agent).ID != id || taskSnapshot(r.agent).Status != "seeding" {
		t.Fatal("command replaced file state")
	}
	// Verify a real chunk after both jobs finish, not just an in-memory status.
	out, err := os.Create(filepath.Join(t.TempDir(), "chunk-copy"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err = out.Truncate(int64(len(data))); err != nil {
		t.Fatal(err)
	}
	pool := mempool.NewMemPool(_const.ChunkSize)
	sdk := client.NewGspSdk(fmt.Sprintf("127.0.0.1:%d", r.agent.session.Port()), pool)
	defer sdk.Close()
	ck := chunk.NewFileChunk(out, pool)
	if _, _, err = sdk.FetchChunk(fmt.Sprintf("127.0.0.1:%d", r.agent.session.Port()), 0, ck); err != nil {
		t.Fatalf("seed unavailable after independent command: %v", err)
	}
	got := make([]byte, _const.ChunkSize)
	if _, err = out.ReadAt(got, 0); err != nil || !bytes.Equal(got, data[:len(got)]) {
		t.Fatal("seed chunk corrupted")
	}
	agent := r.agent
	addr := fmt.Sprintf("127.0.0.1:%d", agent.session.Port())
	if err = r.stopRole(context.Background(), "receiver"); err != nil {
		t.Fatal(err)
	}
	if taskSnapshot(agent).Status != "ended" {
		t.Fatal("offline node was not marked ended")
	}
	connection, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("ended node still serving")
	}
	state := r.state().(map[string]any)
	if state["receiver"].(map[string]any)["task"].(TaskState).Status != "ended" {
		t.Fatal("finished task disappeared from receiver view")
	}
}
func TestSessionRecoveryEndpointKeepsOriginProtection(t *testing.T) {
	a := newTestApp(t)
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	response, err := http.Get(srv.URL + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	var session map[string]string
	_ = json.NewDecoder(response.Body).Decode(&session)
	response.Body.Close()
	if session["token"] != a.csrf {
		t.Fatal("session could not be recovered")
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.Header.Set("X-GoSilver-Token", "previous-process-token")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var rejected map[string]string
	_ = json.NewDecoder(response.Body).Decode(&rejected)
	response.Body.Close()
	if response.StatusCode != 403 || rejected["code"] != "session_expired" {
		t.Fatal("expired session missing recovery code")
	}
	req, _ = http.NewRequest("GET", srv.URL+"/api/session", nil)
	req.Header.Set("Origin", "https://external.example")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross origin session disclosure")
	}
}
