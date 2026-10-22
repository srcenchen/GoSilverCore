package chunk_test

import (
	"bytes"
	"go-silver-core/internal/chunk"
	_const "go-silver-core/internal/const"
	"go-silver-core/pkg/mempool"
	"os"
	"path/filepath"
	"testing"
)

func TestChunkRoundTripAndBounds(t *testing.T) {
	data := bytes.Repeat([]byte("Silver"), _const.ChunkSize/6+31)
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err = out.Truncate(int64(len(data))); err != nil {
		t.Fatal(err)
	}
	pool := mempool.NewMemPool(_const.ChunkSize)
	a, b := chunk.NewFileChunk(in, pool), chunk.NewFileChunk(out, pool)
	buf := make([]byte, _const.ChunkSize)
	for i := int64(0); i < a.GetChunkNum(); i++ {
		n, err := a.ReadChunk(i, buf)
		if err != nil {
			t.Fatal(err)
		}
		if err = b.Save(i, buf[:n]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("file mismatch")
	}
	if _, err = a.ReadChunk(-1, buf); err == nil {
		t.Fatal("negative index accepted")
	}
	if _, err = a.ReadChunk(0, buf[:1]); err == nil {
		t.Fatal("short buffer accepted")
	}
	if err = b.Save(0, []byte{1}); err == nil {
		t.Fatal("short chunk accepted")
	}
	if err = b.Save(b.GetChunkNum(), buf); err == nil {
		t.Fatal("out of bounds write accepted")
	}
}
