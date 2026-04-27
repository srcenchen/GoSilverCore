package chunk

func (f *FileChunk) Save(index int64, data []byte) error {
	f.ioPermit <- struct{}{}
	defer func() { <-f.ioPermit }()
	_, err := f.file.WriteAt(data, index*f.chunkSize)
	return err
}
