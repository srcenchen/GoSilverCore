package chunk

func (f *FileChunk) Save(index int64, data []byte) error {
	_, err := f.file.WriteAt(data, index*f.chunkSize)
	return err
}
