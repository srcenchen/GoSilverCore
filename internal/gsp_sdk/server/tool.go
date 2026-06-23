package server

import (
	"go-silver-core/internal/chunk"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/queue"
	"go-silver-core/pkg/mempool"
	"hash/crc32"
	"time"
)

// GetMemPool 获取内存池
func (s *Session) GetMemPool() *mempool.MemPool {
	return s.memPool
}

// GetChunk 获取块实体
func (s *Session) GetChunk() chunk.FileChunk {
	return s.chunkProvider
}

// ReadChunk 获取Chunk块
func (s *Session) ReadChunk(i int64, buf []byte) (int, error) {
	return s.chunkProvider.ReadChunk(i, buf)
}

// GetQueue 获取队列
func (s *Session) GetQueue() queue.DownloadQueue {
	return s.queue
}

// IndexValid 校验 index 下标这个块是合法的，当前拥有这个块
// 返回 存在与否、哈希校验值
//
// 磁盘读取 + CRC32 在锁外完成：否则主发送端并发处理大量 getChunk 时，
// 所有 Want/AddBlockOwner/AddPeer 都会被这把全局锁串行化（磁盘 IO 成为瓶颈）。
func (s *Session) IndexValid(i int64) (bool, uint32) {
	s.mu.RLock()
	v, cached := s.chunkHash[i]
	isMain := s.isMain
	s.mu.RUnlock()
	if cached {
		return true, v
	}
	// chunkProvider/FileStat 在 BeSendMain/BeSendSub 后即不可变，可在锁外读取。
	if i < 0 || i >= s.chunkProvider.GetChunkNum() {
		return false, 0
	}
	if !isMain {
		// 接收端/子发送端只能分发缓存(chunkHash)中已下载完的块，避免把全零文件块发送出去
		return false, 0
	}
	// 锁外做磁盘读 + 哈希，避免持有全局锁阻塞其他调度操作。
	buf := s.memPool.Get(_const.ChunkSize)
	defer s.memPool.Put(buf)
	c, _ := s.chunkProvider.ReadChunk(i, *buf)
	cm := crc32.ChecksumIEEE((*buf)[:c])
	// 回填缓存；并发下若已被他人填入则以已有值为准（同一块哈希相同，竞争无害）。
	s.mu.Lock()
	if v, ok := s.chunkHash[i]; ok {
		cm = v
	} else {
		s.chunkHash[i] = cm
	}
	s.mu.Unlock()
	return true, cm
}

// AddChunk 添加文件块哈希
func (s *Session) AddChunk(i int64, checksum uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.chunkHash[i]; !ok {
		s.chunkHash[i] = checksum
	}
}

// UpdatePeer 更新Peer信息：释放一个调度租约、用 EWMA 更新上行实测、维护带时间衰减的失败分。
// status 可以是 "failed", "busy", "done"
func (s *Session) UpdatePeer(providerUuid string, speed int64, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	peer, ok := s.Peers[providerUuid]
	if !ok || peer == nil {
		return
	}
	// 一次上报对应一个已完成（或失败）的传输，释放其租约。
	peer.releaseLease()

	nowSec := time.Now().Unix()
	switch status {
	case "failed":
		// 先按时间衰减历史失败分，再 +1，避免久远的失败被永久累加。
		peer.failScore = peer.effFail(nowSec) + 1
		peer.lastFailSec = nowSec
	case "done":
		// 成功一次清零失败分，给节点重新赢得调度机会。
		peer.failScore = 0
		if speed > 0 {
			if peer.upMbpsEWMA <= 0 {
				peer.upMbpsEWMA = float64(speed) // 首个样本直接采用
			} else {
				a := _const.SchedEWMAAlpha
				peer.upMbpsEWMA = a*float64(speed) + (1-a)*peer.upMbpsEWMA
			}
		}
	}
	// "busy" 只释放租约，不动失败分与速度。
}

// IsMain 是否为主发送端
func (s *Session) IsMain() bool {
	return s.isMain
}

// HasLocalChunk 本节点是否已缓存（可对外提供）第 i 块。
// 用于 prefetch 控制指令去重，避免重复下载已拥有的块。
func (s *Session) HasLocalChunk(i int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.isMain {
		return i >= 0 && i < s.chunkProvider.GetChunkNum()
	}
	_, ok := s.chunkHash[i]
	return ok
}
