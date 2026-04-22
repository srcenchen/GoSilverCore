package chunk

import (
	"fmt"
	"hash/crc32"
	"log"
	"os"
	"testing"
)

func TestFileChunk(t *testing.T) {
	f, err := os.Open("test.apk")
	if err != nil {
		t.Fatal("文件打开失败")
	}
	defer f.Close()
	c := NewFileChunk(f)
	fileHash := map[int64]string{}
	for i := int64(0); i < c.GetChunkNum(); i++ {
		buf, _ := c.ReadChunk(i)
		checksum := crc32.ChecksumIEEE(buf)
		fileHash[i] = fmt.Sprintf("%x", checksum)
		log.Printf("第 %d / %d 块的 crc32 值为 %x", i+1, c.GetChunkNum(), checksum)
	}
	fs, _ := f.Stat()
	fNew, _ := os.Create("out.apk")
	fNew.Truncate(fs.Size())
	cNew := NewFileChunk(fNew)
	for i := int64(0); i < c.GetChunkNum(); i++ {
		buf, _ := c.ReadChunk(i)
		cNew.Save(i, buf)
	}
	fNew.Close()
}
