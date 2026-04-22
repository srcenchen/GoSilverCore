package chunk

import "errors"

// ReadChunk 读取逻辑分块
func (f *FileChunk) ReadChunk(index int64) ([]byte, error) {
	if index >= f.GetChunkNum() || index < 0 {
		return nil, errors.New("目前分块数不合法")
	}
	readSize := int64(chunkSize)
	offset := int64(chunkSize * index)
	if offset+readSize > f.fileStat.Size() {
		readSize = f.fileStat.Size() - offset
	}
	buf := make([]byte, readSize)
	_, err := f.file.ReadAt(buf, offset)
	if err != nil {
		return nil, err
	}
	return buf, nil
}
