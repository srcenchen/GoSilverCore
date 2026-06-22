package chunk

import (
	"bytes"
	"crypto/rand"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"go-silver-core/internal/chunk"
	_const "go-silver-core/internal/const"
	"go-silver-core/pkg/mempool"
)

// TestFileChunkRoundTrip 自包含地验证分块读写：
// 随机生成一个跨多块的文件，逐块读取并写入新文件，再校验逐块 CRC 与整体字节一致。
func TestFileChunkRoundTrip(t *testing.T) {
	dir := t.TempDir()
	size := int64(_const.ChunkSize*2 + 1234) // 约 2.x 个分块，含不满一块的尾部
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(srcPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	pool := mempool.NewMemPool(_const.ChunkSize)

	src, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	cSrc := chunk.NewFileChunk(src, pool)

	dstPath := filepath.Join(dir, "dst.bin")
	dst, err := os.Create(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := dst.Truncate(size); err != nil {
		t.Fatal(err)
	}
	cDst := chunk.NewFileChunk(dst, pool)

	if cSrc.GetChunkNum() != cDst.GetChunkNum() {
		t.Fatalf("源/目标块数不一致 src=%d dst=%d", cSrc.GetChunkNum(), cDst.GetChunkNum())
	}

	hashes := make(map[int64]uint32)
	for i := int64(0); i < cSrc.GetChunkNum(); i++ {
		buf := pool.Get(_const.ChunkSize)
		n, err := cSrc.ReadChunk(i, *buf)
		if err != nil {
			pool.Put(buf)
			t.Fatalf("读取块 %d 失败: %v", i, err)
		}
		hashes[i] = crc32.ChecksumIEEE((*buf)[:n])
		if err := cDst.Save(i, (*buf)[:n]); err != nil {
			pool.Put(buf)
			t.Fatalf("写入块 %d 失败: %v", i, err)
		}
		pool.Put(buf)
	}

	for i := int64(0); i < cDst.GetChunkNum(); i++ {
		buf := pool.Get(_const.ChunkSize)
		n, err := cDst.ReadChunk(i, *buf)
		if err != nil {
			pool.Put(buf)
			t.Fatalf("回读块 %d 失败: %v", i, err)
		}
		if got := crc32.ChecksumIEEE((*buf)[:n]); got != hashes[i] {
			pool.Put(buf)
			t.Fatalf("块 %d CRC 校验不一致", i)
		}
		pool.Put(buf)
	}

	out, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("产物与源文件字节不一致")
	}
}
