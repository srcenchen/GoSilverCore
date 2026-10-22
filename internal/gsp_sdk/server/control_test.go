package server

import (
	"fmt"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/pkg/mempool"
	"os"
	"testing"
	"time"
)

func TestCoordinatorUsesRegisteredControlSocket(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "payload")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.Write([]byte("seed")); err != nil {
		t.Fatal(err)
	}
	s := NewGspSession("127.0.0.1:0", mempool.NewMemPool(4<<20))
	if err = s.BeSendMain(f); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	sdk := client.NewGspSdk(fmt.Sprintf("127.0.0.1:%d", s.Port()), nil, "receiver", "zone-a")
	defer sdk.Close()
	if err = sdk.PeerReg(s.Port(), "receiver"); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Second)
	for len(s.Snapshot()) < 2 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	msgs := make(chan model.ControlMsg, 8)
	down := make(chan struct{})
	go sdk.WatchControl(nil, func(msg model.ControlMsg) { msgs <- msg }, func() { close(down) })
	defer func() { sdk.Close(); <-down }()
	s.coordinateOnce()
	select {
	case msg := <-msgs:
		if msg.Cmd != "prefetch" || msg.Index != 0 {
			t.Fatal(msg)
		}
	case <-time.After(time.Second):
		t.Fatal("prefetch control was not delivered")
	}
}

func TestBackoffLimitsUploadsAndExpires(t *testing.T) {
	s := NewGspSession("", nil)
	defer s.Stop()
	s.ApplyControl(model.ControlMsg{Cmd: "backoff", Until: time.Now().Add(time.Minute).Unix()})
	if !s.AcquireUpload(time.Second) || !s.AcquireUpload(time.Second) {
		t.Fatal("backoff blocked permitted uploads")
	}
	if s.AcquireUpload(20 * time.Millisecond) {
		t.Fatal("backoff allowed a third upload")
	}
	s.mu.Lock()
	s.backoffUntil = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if !s.AcquireUpload(time.Second) {
		t.Fatal("expired backoff did not recover")
	}
	for range 3 {
		s.ReleaseUpload()
	}
}
