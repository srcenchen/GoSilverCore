package server

import (
	"go-silver-core/pkg/mempool"
	"testing"
	"time"
)

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
