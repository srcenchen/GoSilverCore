package chunk

import (
	"fmt"
	"go-silver-core/internal/chunk"
	"hash/crc32"
	"os"
	"testing"
)

func TestFileChunk(t *testing.T) {
	f, err := os.Open("test.apk")
	if err != nil {
		t.Fatal("文件打开失败")
	}
	defer f.Close()
	c := chunk.NewFileChunk(f)
	fileHash := map[int64]string{}
	for i := int64(0); i < c.GetChunkNum(); i++ {
		buf, _ := c.ReadChunk(i)
		checksum := crc32.ChecksumIEEE(buf)
		fileHash[i] = fmt.Sprintf("%x", checksum)
		t.Logf("第 %d / %d 块的 crc32 值为 %x", i+1, c.GetChunkNum(), checksum)
	}
	fs, _ := f.Stat()
	fNew, _ := os.Create("out.apk")
	fNew.Truncate(fs.Size())
	cNew := chunk.NewFileChunk(fNew)
	for i := int64(0); i < c.GetChunkNum(); i++ {
		buf, _ := c.ReadChunk(i)
		cNew.Save(i, buf)
	}
	// 判断哈希值是否相同
	t.Logf("准备校验Hash")
	for i := int64(0); i < cNew.GetChunkNum(); i++ {
		buf, _ := cNew.ReadChunk(i)
		checksum := crc32.ChecksumIEEE(buf)
		t.Logf("第 %d / %d 块的 crc32 值为 %x", i+1, cNew.GetChunkNum(), checksum)
		if fmt.Sprintf("%x", checksum) != fileHash[i] {
			t.Fatal("哈希值不同！")
		}
	}
	fNew.Close()

}
