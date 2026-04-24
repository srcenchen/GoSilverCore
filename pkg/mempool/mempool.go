package mempool

import "sync"

type MemPool struct {
	size int64
	pool sync.Pool
}

func NewMemPool(size int64) MemPool {
	return MemPool{size: size, pool: sync.Pool{New: func() interface{} {
		b := make([]byte, size)
		return &b
	}}}
}

func (mp *MemPool) Get(size int64) *[]byte {
	if size > mp.size {
		p := make([]byte, size)
		return &p
	}
	p := mp.pool.Get().(*[]byte)
	*p = (*p)[:2]
	return p
}

func (mp *MemPool) Put(p *[]byte) {
	if int64(len(*p)) > mp.size {
		return
	}
	*p = (*p)[:mp.size]
	mp.pool.Put(p)
}
