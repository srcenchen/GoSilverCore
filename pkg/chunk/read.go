package chunk

import (
	"errors"
	"hash/crc32"
)

// ReadChunk 读取逻辑分块
func (f *FileChunk) ReadChunk(index int64) ([]byte, error) {
	if index >= f.GetChunkNum() || index < 0 {
		return nil, errors.New("目前分块数不合法")
	}
	readSize := int64(chunkSize)
	offset := int64(chunkSize * index)
	if offset+readSize > f.FileStat.Size() {
		readSize = f.FileStat.Size() - offset
	}
	buf := make([]byte, readSize)
	_, err := f.file.ReadAt(buf, offset)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// CheckSum 计算指定块的哈希值
func (f *FileChunk) CheckSum(i int64) (uint32, error) {
	c, err := f.ReadChunk(i)
	if err != nil {
		return 0, err
	}
	return crc32.ChecksumIEEE(c), nil
}
