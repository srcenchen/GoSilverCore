package server

import (
	"errors"
	"go-silver-core/internal/chunk"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/queue"
	"go-silver-core/pkg/mempool"
	"hash/crc32"
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
	if s.queue == nil {
		s.queue = &queue2{s: s}
	}
	return s.queue
}

// IndexValid 校验 index 下标这个块是合法的，当前拥有这个块
// 返回 存在与否、哈希校验值
func (s *Session) IndexValid(i int64) (bool, uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.chunkHash[i]; ok {
		return true, v
	}
	if i < 0 || i >= s.chunkProvider.GetChunkNum() {
		return false, 0
	}
	if !s.isMain {
		// 接收端/子发送端只能分发缓存(chunkHash)中已下载完的块，避免把全零文件块发送出去
		return false, 0
	}
	buf := s.memPool.Get(_const.ChunkSize)
	defer s.memPool.Put(buf)
	c, _ := s.chunkProvider.ReadChunk(i, *buf)
	cm := crc32.ChecksumIEEE((*buf)[:c])
	s.chunkHash[i] = cm
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

// UpdatePeer 更新Peer信息：减少活跃连接数、更新速度、维护失败计数。
// failed=true 时累加 failCount，会在调度得分中指数降权；成功时清零 failCount。
func (s *Session) UpdatePeer(providerUuid string, speed int64, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	peer, ok := s.Peers[providerUuid]
	if !ok || peer == nil {
		return
	}
	if peer.connNum > 0 {
		peer.connNum--
	}
	if failed {
		peer.failCount++
	} else {
		// 成功一次清零失败计数，给节点重新赢得调度机会
		peer.failCount = 0
		if speed > 0 {
			peer.maxSpeed = max(peer.maxSpeed, speed)
		}
	}
}

// IsMain 是否为主发送端
func (s *Session) IsMain() bool {
	return s.isMain
}

// AcquireUploadSlot 申请一个并发上传槽，如果满了会阻塞以防止服务被拖垮
func (s *Session) AcquireUploadSlot() error {
	select {
	case s.uploadSem <- struct{}{}:
		return nil
	case <-s.done:
		return errors.New("session stopped")
	}
}

// ReleaseUploadSlot 释放并发上传槽
func (s *Session) ReleaseUploadSlot() {
	select {
	case <-s.uploadSem:
	default:
	}
}
