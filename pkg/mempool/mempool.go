package mempool

// MemPool lazily caches buffers. Borrowing never blocks behind file transfers.
type MemPool struct {
	size int64
	ch   chan *[]byte
}

func NewMemPool(size int64) *MemPool { return &MemPool{size: size, ch: make(chan *[]byte, 16)} }
func (m *MemPool) Get(size int64) *[]byte {
	if size < 0 {
		size = 0
	}
	if size == m.size {
		select {
		case p := <-m.ch:
			*p = (*p)[:size]
			return p
		default:
		}
	}
	p := make([]byte, size)
	return &p
}
func (m *MemPool) Put(p *[]byte) {
	if p == nil || int64(cap(*p)) != m.size {
		return
	}
	*p = (*p)[:m.size]
	select {
	case m.ch <- p:
	default:
	}
}
