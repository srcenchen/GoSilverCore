package gosilver

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"normal.zip":      "normal.zip",
		"a/b\\c:d.txt":    "a_b_c_d.txt",
		"trail. ":         "trail",
		`bad*?"<>|.bin`:   "bad______.bin",
		"":                "download",
		"CON":             "_CON",
		"nul.txt":         "_nul.txt",
		"../../etc/passwd": ".._.._etc_passwd",
	}
	for in, want := range cases {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEndToEndLoopback 在本机回环上跑通完整链路，并验证保存到不存在的嵌套目录会被自动创建。
func TestEndToEndLoopback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "payload.bin")
	data := make([]byte, 5<<20+777) // ~1.25 个分块
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}

	const addr = "127.0.0.1:48911"
	srv := NewServer(addr, src)
	if err := srv.Start(); err != nil {
		t.Fatalf("启动发送端失败: %v", err)
	}
	defer srv.Stop()

	// 故意指向一个尚不存在的嵌套目录，验证 os.MkdirAll 自动创建
	saveDir := filepath.Join(dir, "out", "nested")
	cli := NewClient(addr, saveDir)
	ch, err := cli.StartDownload()
	if err != nil {
		t.Fatalf("启动下载失败: %v", err)
	}
	defer cli.CancelDownload()

	deadline := time.After(30 * time.Second)
	for done := false; !done; {
		select {
		case p, ok := <-ch:
			if !ok {
				t.Fatal("进度通道关闭但未完成")
			}
			switch p.Status {
			case "completed":
				done = true
			case "failed":
				t.Fatalf("下载失败: %v", p.Error)
			}
		case <-deadline:
			t.Fatal("30s 内未完成下载")
		}
	}

	got, err := os.ReadFile(filepath.Join(saveDir, "payload.bin"))
	if err != nil {
		t.Fatalf("读取产物失败（嵌套目录应被自动创建）: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("下载产物与源文件内容不一致")
	}
}
