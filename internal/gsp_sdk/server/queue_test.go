package server

import (
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/pkg/mempool"
	"testing"
	"time"
)

func TestZoneLocalityAndCandidateLeases(t *testing.T) {
	s := NewGspSession("127.0.0.1:0", nil)
	s.Peers[s.UUID] = &Peer{speedEwma: 1}
	s.Peers["local"] = &Peer{zone: "a", connAddr: "127.0.0.1:1", speedEwma: 100}
	s.Peers["remote"] = &Peer{zone: "b", connAddr: "127.0.0.1:2", speedEwma: 200}
	s.ChunkOwners[0] = map[string]struct{}{"local": {}, "remote": {}}
	q := &queue2{s: s}
	s.mu.Lock()
	candidates := q.candidatesLocked(model.WantChunkReq{Index: 0, UUID: "receiver", Zone: "a"}, nil)
	s.mu.Unlock()
	if len(candidates) != 3 || candidates[0].uuid != "local" {
		t.Fatalf("local source not preferred: %+v", candidates)
	}
	s.mu.Lock()
	candidates = q.candidatesLocked(model.WantChunkReq{Index: 0, UUID: "receiver", Zone: "c"}, nil)
	s.mu.Unlock()
	if candidates[0].uuid != s.UUID {
		t.Fatal("new zone did not receive a central seed")
	}
}

func TestLeasesArePerReceiverAndChunk(t *testing.T) {
	s := NewGspSession("127.0.0.1:0", mempool.NewMemPool(4096))
	s.Peers[s.UUID] = &Peer{leases: map[string]time.Time{}}
	s.ChunkOwners[0] = map[string]struct{}{s.UUID: {}}
	q := &queue2{s: s}
	q.pickFor(0, nil, "receiver-a")
	q.pickFor(0, nil, "receiver-b")
	if s.Peers[s.UUID].connNum != 2 {
		t.Fatal("same chunk for distinct receivers shared one lease")
	}
	s.UpdatePeer(s.UUID, 100, "done", 0, "receiver-a")
	if s.Peers[s.UUID].connNum != 1 {
		t.Fatal("first report removed other receiver's lease")
	}
	s.UpdatePeer(s.UUID, 100, "done", 0, "receiver-b")
	if s.Peers[s.UUID].connNum != 0 {
		t.Fatal("lease leak")
	}
}
