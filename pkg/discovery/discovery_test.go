package discovery

import (
	"net"
	"testing"
	"time"
)

// TestAnnounceListenRoundTrip 验证本机多播自发自收的发现链路。
// 注意：依赖多播回环（IP_MULTICAST_LOOP，默认开启）；若 CI 网络受限可能跳过。
func TestAnnounceListenRoundTrip(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)

	got := make(chan AnnounceInfo, 1)
	gotAddr := make(chan net.Addr, 1)
	if err := Listen(stop, func(info AnnounceInfo, src net.Addr) {
		select {
		case got <- info:
			gotAddr <- src
		default:
		}
	}); err != nil {
		t.Skipf("无法监听多播（环境可能不支持）: %v", err)
	}

	// 给监听端一点时间完成 join
	time.Sleep(100 * time.Millisecond)

	want := AnnounceInfo{
		Port:     48080,
		FileName: "contest.zip",
		FileSize: 12345,
		SaveDir:  "./downloads",
		Token:    "tok-1",
	}
	if err := Announce(stop, want, 200*time.Millisecond); err != nil {
		t.Fatalf("Announce 失败: %v", err)
	}

	select {
	case info := <-got:
		if info.FileName != want.FileName || info.Port != want.Port ||
			info.FileSize != want.FileSize || info.SaveDir != want.SaveDir || info.Token != want.Token {
			t.Fatalf("收到的通告与发送不一致: %+v", info)
		}
		if info.Magic != Magic {
			t.Fatalf("Magic 不匹配: %q", info.Magic)
		}
		src := <-gotAddr
		if addr := info.SenderAddr(src); addr == "" {
			t.Fatalf("SenderAddr 解析为空, src=%v", src)
		}
	case <-time.After(3 * time.Second):
		t.Skip("3s 内未收到多播通告，可能为受限网络环境，跳过")
	}
}
