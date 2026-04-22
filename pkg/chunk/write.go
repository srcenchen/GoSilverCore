package chunk

func (f *FileChunk) Save(index int64, data []byte) {
	f.file.WriteAt(data, index*f.chunkSize)
}
