package chunk

import (
	_const "go-silver-core/internal/const"
	"go-silver-core/pkg/mempool"
	"os"
)

const chunkSize = _const.ChunkSize

// FileChunk 文件逻辑分块
type FileChunk struct {
	file      *os.File
	FileStat  os.FileInfo
	chunkSize int64 // 以 Byte 为单位
	memPool   *mempool.MemPool
}

// GetChunkNum 获取文件的分块数
func (f *FileChunk) GetChunkNum() int64 {
	if f.FileStat == nil || f.FileStat.Size() == 0 {
		return 0
	}
	return (f.FileStat.Size() + f.chunkSize - 1) / f.chunkSize
}

// GetChunkSize 获取单块大小
func (f *FileChunk) GetChunkSize() int64 {
	return f.chunkSize
}

func NewFileChunk(f *os.File, pool *mempool.MemPool) *FileChunk {
	fs, _ := f.Stat()
	return &FileChunk{
		FileStat:  fs,
		file:      f,
		chunkSize: chunkSize,
		memPool:   pool,
	}
}

// Unchanged protects main-source transfers from silently mixing file versions.
func (f *FileChunk) Unchanged() bool {
	if f.FileStat == nil {
		return false
	}
	now, err := f.file.Stat()
	return err == nil && now.Size() == f.FileStat.Size() && now.ModTime().Equal(f.FileStat.ModTime()) && os.SameFile(now, f.FileStat)
}
