package conn_pool

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestExpiredIdleSocketIsReplaced(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	accepted := make(chan net.Conn, 2)
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	p := NewConnPool(1)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	addr := lis.Addr().String()
	first, err := p.GetConnContext(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-accepted
	defer peer.Close()
	p.PutConn(addr, first)
	p.mu.Lock()
	b := p.buckets[addr]
	idle := <-b.idle
	idle.since = time.Now().Add(-maxIdleTime - time.Second)
	b.idle <- idle
	p.mu.Unlock()
	second, err := p.GetConnContext(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("expired socket was reused")
	}
	peer2 := <-accepted
	defer peer2.Close()
	p.PutConn(addr, second)
	third, err := p.GetConnContext(ctx, addr)
	if err != nil || third != second {
		t.Fatalf("fresh idle socket was not reused: %v", err)
	}
	p.PutConn(addr, third)
}
