package chunk

import (
	"errors"
	"hash/crc32"
)

// ReadChunk 读取逻辑分块
func (f *FileChunk) ReadChunk(index int64, buf []byte) (int, error) {
	if index >= f.GetChunkNum() || index < 0 {
		return 0, errors.New("目前分块数不合法")
	}
	readSize := int64(chunkSize)
	offset := int64(chunkSize * index)
	if offset+readSize > f.FileStat.Size() {
		readSize = f.FileStat.Size() - offset
	}
	n, err := f.file.ReadAt(buf[:readSize], offset)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// CheckSum 计算指定块的哈希值
func (f *FileChunk) CheckSum(i int64) (uint32, error) {
	buf := f.memPool.Get(f.chunkSize)
	defer f.memPool.Put(buf)
	n, err := f.ReadChunk(i, *buf)
	if err != nil {
		return 0, err
	}
	return crc32.ChecksumIEEE((*buf)[:n]), nil
}
