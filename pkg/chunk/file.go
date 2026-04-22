package chunk

import (
	"os"
)

const chunkSize = 16 * (1 << 20)

// FileChunk 文件逻辑分块
type FileChunk struct {
	file      *os.File
	fileStat  os.FileInfo
	chunkSize int64 // 以 Byte 为单位
}

// GetChunkNum 获取文件的分块数
func (f *FileChunk) GetChunkNum() int64 {
	if f.fileStat.Size() == 0 {
		return 0
	}
	return (f.fileStat.Size() + f.chunkSize - 1) / f.chunkSize
}

// GetChunkSize 获取单块大小
func (f *FileChunk) GetChunkSize() int64 {
	return f.chunkSize
}

func NewFileChunk(f *os.File) *FileChunk {
	fs, _ := f.Stat()
	return &FileChunk{
		fileStat:  fs,
		file:      f,
		chunkSize: chunkSize,
	}
}
