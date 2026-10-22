package mempool

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestBorrowDoesNotDeadlockWhenCacheIsEmpty(t *testing.T) {
	pool := NewMemPool(128)
	var wg sync.WaitGroup
	release := make(chan struct{})
	acquired := make(chan struct{}, 64)
	for range 64 {
		wg.Add(1)
		go func() { defer wg.Done(); p := pool.Get(128); acquired <- struct{}{}; <-release; pool.Put(p) }()
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for range 64 {
		select {
		case <-acquired:
		case <-timer.C:
			close(release)
			wg.Wait()
			t.Fatal("buffer borrowing blocked on another borrower")
		}
	}
	close(release)
	wg.Wait()
}
func BenchmarkPoolStartup(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		p := NewMemPool(4 << 20)
		runtime.KeepAlive(p)
	}
}
func BenchmarkCachedBuffer(b *testing.B) {
	p := NewMemPool(4 << 20)
	buf := p.Get(4 << 20)
	p.Put(buf)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf := p.Get(4 << 20)
		p.Put(buf)
	}
}
