package mempool

type MemPool struct {
	size int64
	// 使用 chan 代替 sync.Pool 来实现硬上限
	ch chan *[]byte
}

func NewMemPool(size int64) *MemPool {
	poolCount := 50
	m := &MemPool{
		size: size,
		ch:   make(chan *[]byte, poolCount),
	}
	for i := 0; i < poolCount; i++ {
		t := make([]byte, size)
		m.ch <- &t
	}
	return m
}

func (mp *MemPool) Get(size int64) *[]byte {
	// 非合法大小
	if size > mp.size {
		t := make([]byte, size)
		return &t
	}
	t := <-mp.ch
	p := (*t)[:size]
	return &p
}

func (mp *MemPool) Put(p *[]byte) {
	// 检查大小是否正确
	if p == nil || int64(cap(*p)) != mp.size {
		return
	}
	// 恢复长度，写回 Channel 供其他线程复用
	*p = (*p)[:mp.size]
	select {
	case mp.ch <- p:
	default:
		// 如果池子满了还往里塞，说明归还逻辑有问题，直接丢弃即可
	}
}
