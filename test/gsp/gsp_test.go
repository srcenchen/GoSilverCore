package gsp

import (
	"bytes"
	"net"
	"testing"
	"time"

	gsp2 "go-silver-core/internal/gsp"
)

// TestCodecRoundTrip 自包含地验证 GSP 编解码：通过 net.Pipe 连续编码两种类型的帧，
// 在对端逐帧解码并校验类型与载荷一致。
func TestCodecRoundTrip(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()

	type frame struct {
		typ     uint8
		payload []byte
	}
	frames := []frame{
		{gsp2.TypeJSON, []byte(`{"operate":"getFileStatus"}`)},
		{gsp2.TypeFileChunk, bytes.Repeat([]byte{0xAB}, 4096)},
		{gsp2.TypeJSON, []byte(``)}, // 空载荷边界
	}

	go func() {
		c := gsp2.Codec{}
		for _, f := range frames {
			_ = c.EncodeTo(cli, f.typ, f.payload)
		}
	}()

	c := gsp2.Codec{}
	buf := make([]byte, 8192)
	_ = srv.SetReadDeadline(time.Now().Add(3 * time.Second))
	for i, want := range frames {
		pkt, err := c.Decode(srv, buf)
		if err != nil {
			t.Fatalf("解码第 %d 帧失败: %v", i, err)
		}
		if pkt.Type != want.typ {
			t.Fatalf("帧 %d 类型不符: got %d want %d", i, pkt.Type, want.typ)
		}
		if !bytes.Equal(pkt.Payload, want.payload) {
			t.Fatalf("帧 %d 载荷不符", i)
		}
	}
}
