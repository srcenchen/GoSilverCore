package server

import (
	"errors"
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.chunkProvider
}

// ReadChunk 获取Chunk块
func (s *Session) ReadChunk(i int64, buf []byte) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ck := s.chunkProvider
	if s.isMain && !ck.Unchanged() {
		return 0, errors.New("source file changed during transfer")
	}
	n, err := ck.ReadChunk(i, buf)
	if err == nil && s.isMain && !ck.Unchanged() {
		return 0, errors.New("source file changed during transfer")
	}
	return n, err
}

// GetQueue 获取队列
func (s *Session) GetQueue() queue.DownloadQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queue == nil {
		s.queue = &queue2{s: s}
	}
	return s.queue
}

// IndexValid 校验 index 下标这个块是合法的，当前拥有这个块
// 返回 存在与否、哈希校验值
func (s *Session) IndexValid(i int64) (bool, uint32) {
	s.mu.RLock()
	if v, ok := s.chunkHash[i]; ok {
		s.mu.RUnlock()
		return true, v
	}
	isMain := s.isMain
	s.mu.RUnlock()
	ck := s.GetChunk()
	if ck.FileStat == nil || i < 0 || i >= ck.GetChunkNum() || !isMain {
		return false, 0
	}
	buf := s.memPool.Get(_const.ChunkSize)
	defer s.memPool.Put(buf)
	c, err := ck.ReadChunk(i, *buf)
	if err != nil || c <= 0 {
		return false, 0
	}
	cm := crc32.ChecksumIEEE((*buf)[:c])
	s.mu.Lock()
	s.chunkHash[i] = cm
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

// UpdatePeer 归还租约，并用本次结果更新速度和冷却
func (s *Session) UpdatePeer(providerUuid string, speed int64, status string, index int64, requester string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	peer, ok := s.Peers[providerUuid]
	if !ok || peer == nil {
		return
	}
	lease := leaseKey(requester, index)
	if peer.leases != nil {
		if _, leased := peer.leases[lease]; leased {
			delete(peer.leases, lease)
			if peer.connNum > 0 {
				peer.connNum--
			}
		}
	}
	if status == "failed" {
		if providerUuid != s.UUID {
			delete(s.ChunkOwners[index], providerUuid)
			delete(s.PeerOwners[providerUuid], index)
		}
		peer.failStreak++
		backoff := time.Duration(1<<min(peer.failStreak, 4)) * time.Second
		peer.cooldownTil = time.Now().Add(backoff)
		peer.speedEwma *= 0.5
		return
	}
	peer.failStreak = 0
	peer.cooldownTil = time.Time{}
	if peer.speedEwma <= 0 {
		peer.speedEwma = float64(speed)
		return
	}
	peer.speedEwma = peer.speedEwma*0.7 + float64(speed)*0.3
}

// IsMain 是否为主发送端
func (s *Session) IsMain() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isMain
}

// AcquireUploadSlot 申请一个并发上传槽，如果满了会阻塞以防止服务被拖垮
func (s *Session) AcquireUploadSlot() error {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case s.uploadSem <- struct{}{}:
		return nil
	case <-s.done:
		return errors.New("session stopped")
	case <-timer.C:
		return errors.New("upload slot timeout")
	}
}

// ReleaseUploadSlot 释放并发上传槽
func (s *Session) ReleaseUploadSlot() {
	select {
	case <-s.uploadSem:
	default:
	}
}

func (s *Session) HasChunk(i int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.chunkProvider.FileStat == nil || i < 0 || i >= s.chunkProvider.GetChunkNum() {
		return false
	}
	if s.isMain {
		return true
	}
	_, ok := s.chunkHash[i]
	return ok
}
