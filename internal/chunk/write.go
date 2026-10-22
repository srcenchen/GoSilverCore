package chunk

import (
	"fmt"
	"io"
)

func (f *FileChunk) Save(index int64, data []byte) error {
	if index < 0 || index >= f.GetChunkNum() {
		return fmt.Errorf("invalid chunk index %d", index)
	}
	want := min(f.chunkSize, f.FileStat.Size()-index*f.chunkSize)
	if int64(len(data)) != want {
		return fmt.Errorf("chunk %d: expected %d bytes, got %d", index, want, len(data))
	}
	n, err := f.file.WriteAt(data, index*f.chunkSize)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}
