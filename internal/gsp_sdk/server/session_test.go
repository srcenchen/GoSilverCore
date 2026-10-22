package server

import (
	"encoding/json"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"net"
	"testing"
	"time"
)

func TestReusedSocketRefreshesResponseDeadline(t *testing.T) {
	s := NewGspSession("", nil)
	s.MarkSender()
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	done := make(chan struct{})
	go func() { defer close(done); s.handle(serverConn) }()
	defer func() { clientConn.Close(); <-done }()
	codec := gsp.Codec{}
	request := []byte(`{"operate":"getNodeStatus"}`)
	for i := 0; i < 2; i++ {
		if i == 1 {
			// Simulate the deadline left by an earlier response expiring
			// while the socket was idle in the client's pool.
			_ = serverConn.SetWriteDeadline(time.Now().Add(-time.Second))
		}
		_ = clientConn.SetDeadline(time.Now().Add(time.Second))
		if err := codec.EncodeTo(clientConn, gsp.TypeJSON, request); err != nil {
			t.Fatal(err)
		}
		packet, err := codec.Decode(clientConn, make([]byte, 1024))
		if err != nil {
			t.Fatalf("request %d lost its response: %v", i, err)
		}
		var node model.NodeStatus
		if err = json.Unmarshal(packet.Payload, &node); err != nil || node.UUID != s.UUID || !node.Sender {
			t.Fatalf("invalid node response: %+v, %v", node, err)
		}
	}
}
