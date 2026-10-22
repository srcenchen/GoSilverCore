package gsp_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"go-silver-core/internal/chunk"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/internal/gsp_sdk/server"
	"go-silver-core/internal/receiver"
	"go-silver-core/pkg/gosilver"
	"go-silver-core/pkg/mempool"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodecRoundTripAndMalformedFrames(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	codec := gsp.Codec{}
	payload := []byte(`{"operate":"getFileStatus"}`)
	done := make(chan error, 1)
	go func() { done <- codec.EncodeTo(a, gsp.TypeJSON, payload) }()
	packet, err := codec.Decode(b, make([]byte, 1024))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packet.Payload, payload) || packet.Type != gsp.TypeJSON {
		t.Fatal("frame mismatch")
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	for _, typ := range []byte{gsp.TypeJSON, 0xff} {
		var h [5]byte
		h[0] = typ
		binary.LittleEndian.PutUint32(h[1:], 99999)
		if _, err = codec.Decode(bytes.NewReader(h[:]), make([]byte, 16)); err == nil {
			t.Fatal("malformed frame accepted")
		}
	}
}
func TestReceiverCompletesAndKeepsSeeding(t *testing.T) {
	for _, size := range []int{0, 31, _const.ChunkSize*2 + 117} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			data := bytes.Repeat([]byte{0x53}, size)
			path := filepath.Join(t.TempDir(), "source.bin")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			srv := gosilver.NewServer("127.0.0.1:0", path)
			if err := srv.Start(); err != nil {
				t.Fatal(err)
			}
			defer srv.Stop()
			addr := fmt.Sprintf("127.0.0.1:%d", srv.Port())
			dest := t.TempDir()
			receiver := gosilver.NewClient(addr, dest)
			defer receiver.CancelDownload()
			progress, err := receiver.StartDownload()
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
		loop:
			for {
				select {
				case p, ok := <-progress:
					if !ok {
						break loop
					}
					if p.Status == "failed" {
						t.Fatal(p.Error)
					}
				case <-deadline.C:
					t.Fatal("download hung")
				}
			}
			if receiver.GetStatus().Status != "completed" {
				t.Fatalf("status: %+v", receiver.GetStatus())
			}
			got, err := os.ReadFile(filepath.Join(dest, "source.bin"))
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("output mismatch: %v", err)
			}
		})
	}
}
func TestRegistrationWatchSameConnectionAndShutdown(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "source")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.Write([]byte("test"))
	pool := mempool.NewMemPool(_const.ChunkSize)
	s := server.NewGspSession("127.0.0.1:0", pool)
	if err = s.BeSendMain(f); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	sdk := client.NewGspSdk(fmt.Sprintf("127.0.0.1:%d", s.Port()), pool)
	defer sdk.Close()
	if err = sdk.PeerReg(48081, "receiver"); err != nil {
		t.Fatal(err)
	}
	push := make(chan model.PushStart, 1)
	down := make(chan struct{})
	go sdk.Watch(func(p model.PushStart) { push <- p }, func() { close(down) })
	until := time.Now().Add(time.Second)
	for len(s.Snapshot()) < 2 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if s.BroadcastStart() != 1 {
		t.Fatal("push not delivered to registered socket")
	}
	select {
	case p := <-push:
		if p.FileSize != 4 {
			t.Fatal(p)
		}
	case <-time.After(time.Second):
		t.Fatal("push hung")
	}
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop hung")
	}
	select {
	case <-down:
	case <-time.After(time.Second):
		t.Fatal("watch did not stop")
	}
}
func TestCancellationInterruptsUnresponsiveSender(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := lis.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	c := gosilver.NewClient(lis.Addr().String(), t.TempDir())
	_, err = c.StartDownload()
	if err != nil {
		t.Fatal(err)
	}
	conn := <-accepted
	defer conn.Close()
	done := make(chan struct{})
	go func() { c.CancelDownload(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel waited for network timeout")
	}
}
func TestSDKContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sdk := client.NewGspSdk("127.0.0.1:1", mempool.NewMemPool(_const.ChunkSize)).WithContext(ctx)
	defer sdk.Close()
	if _, err := sdk.GetFileStatus(); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestCompletedPeerStillServesVerifiedChunk(t *testing.T) {
	data := bytes.Repeat([]byte("p2p"), 30000)
	sourcePath := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(sourcePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pool := mempool.NewMemPool(_const.ChunkSize)
	source := server.NewGspSession("127.0.0.1:0", pool)
	if err = source.BeSendMain(f); err != nil {
		t.Fatal(err)
	}
	if err = source.Start(); err != nil {
		t.Fatal(err)
	}
	defer source.Stop()
	sourceAddr := fmt.Sprintf("127.0.0.1:%d", source.Port())
	peer := server.NewGspSession("127.0.0.1:0", pool)
	if err = peer.Start(); err != nil {
		t.Fatal(err)
	}
	defer peer.Stop()
	sdk := client.NewGspSdk(sourceAddr, pool)
	defer sdk.Close()
	meta, err := sdk.GetFileStatus()
	if err != nil {
		t.Fatal(err)
	}
	if err = sdk.PeerReg(peer.Port(), peer.UUID); err != nil {
		t.Fatal(err)
	}
	if err = receiver.RunTo(context.Background(), sourceAddr, filepath.Join(t.TempDir(), "complete.bin"), peer, sdk, meta, nil); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(t.TempDir(), "copy.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err = out.Truncate(meta.FileSize); err != nil {
		t.Fatal(err)
	}
	ck := chunk.NewFileChunk(out, pool)
	fromPeer := client.NewGspSdk(fmt.Sprintf("127.0.0.1:%d", peer.Port()), pool)
	defer fromPeer.Close()
	if _, _, err = fromPeer.FetchChunk(fmt.Sprintf("127.0.0.1:%d", peer.Port()), 0, ck); err != nil {
		t.Fatalf("completed receiver cannot seed: %v", err)
	}
	got, err := os.ReadFile(out.Name())
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("peer payload mismatch: %v", err)
	}
}

func TestUnreadyAndChangedSourcesReturnReasonInsteadOfEOF(t *testing.T) {
	pool := mempool.NewMemPool(_const.ChunkSize)
	s := server.NewGspSession("127.0.0.1:0", pool)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	sdk := client.NewGspSdk(fmt.Sprintf("127.0.0.1:%d", s.Port()), pool)
	defer sdk.Close()
	if _, err := sdk.GetFileStatus(); err == nil || !strings.Contains(err.Error(), "尚未就绪") {
		t.Fatalf("unready sender returned unclear error: %v", err)
	}
	path := filepath.Join(t.TempDir(), "changing.bin")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = s.BeSendMain(f); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("after-changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = sdk.GetFileStatus(); err == nil || !strings.Contains(err.Error(), "已变化") {
		t.Fatalf("source change returned unclear error: %v", err)
	}
}

func TestDownloadFailsOverToSecondPeerBeforeCenter(t *testing.T) {
	data := bytes.Repeat([]byte("candidate"), 1000)
	path := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pool := mempool.NewMemPool(_const.ChunkSize)
	center := server.NewGspSession("127.0.0.1:0", pool)
	if err = center.BeSendMain(f); err != nil {
		t.Fatal(err)
	}
	if err = center.Start(); err != nil {
		t.Fatal(err)
	}
	defer center.Stop()
	good := server.NewGspSession("127.0.0.1:0", pool)
	if err = good.BeSendMain(f); err != nil {
		t.Fatal(err)
	}
	if err = good.Start(); err != nil {
		t.Fatal(err)
	}
	defer good.Stop()
	goodAddr := fmt.Sprintf("127.0.0.1:%d", good.Port())
	center.AddPeer("bad", "127.0.0.1:1", nil)
	center.SetPeerZone("bad", "local")
	center.AddBlockOwner(0, "bad")
	center.AddPeer(good.UUID, goodAddr, nil)
	center.SetPeerZone(good.UUID, "local")
	center.AddBlockOwner(0, good.UUID)
	center.UpdatePeer(center.UUID, 1, "done", 0, "")
	center.UpdatePeer(good.UUID, 500, "done", 0, "")
	peer := server.NewGspSession("127.0.0.1:0", pool)
	if err = peer.Start(); err != nil {
		t.Fatal(err)
	}
	defer peer.Stop()
	addr := fmt.Sprintf("127.0.0.1:%d", center.Port())
	sdk := client.NewGspSdk(addr, pool, peer.UUID, "local")
	defer sdk.Close()
	if err = sdk.PeerReg(peer.Port(), peer.UUID); err != nil {
		t.Fatal(err)
	}
	meta, err := sdk.GetFileStatus()
	if err != nil {
		t.Fatal(err)
	}
	usedSecond := false
	target := filepath.Join(t.TempDir(), "payload.bin")
	err = receiver.RunTo(context.Background(), addr, target, peer, sdk, meta, func(p receiver.Progress) {
		for _, source := range p.Active {
			if source.Addr == goodAddr {
				usedSecond = true
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, data) || !usedSecond {
		t.Fatalf("second peer was not used correctly: %v, used=%v", err, usedSecond)
	}
}
