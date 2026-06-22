package server

// AddPeer 对端注册
func (s *Session) AddPeer(uuid string, addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Peers[uuid] = &Peer{
		connAddr: addr,
	}
}

// RemovePeer 移除 对端
func (s *Session) RemovePeer(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cks, ok := s.PeerOwners[uuid]; ok {
		for ckIndex := range cks {
			if _, ex := s.ChunkOwners[ckIndex][uuid]; ex {
				delete(s.ChunkOwners[ckIndex], uuid)
			}
		}
	}
	delete(s.PeerOwners, uuid)
	delete(s.Peers, uuid)
}

// PeerSnapshot 是某个对端的只读进度快照，供上层（如 TUI）渲染节点列表。
type PeerSnapshot struct {
	UUID     string
	Addr     string
	Owned    int64 // 该对端已拥有的块数
	Total    int64 // 总块数
	MaxSpeed int64 // 历史最大速度 Mbps
}

// Snapshot 返回当前所有对端（不含自身）的只读进度快照。
func (s *Session) Snapshot() []PeerSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := s.chunkProvider.GetChunkNum()
	out := make([]PeerSnapshot, 0, len(s.Peers))
	for uid, p := range s.Peers {
		if uid == s.UUID || p == nil {
			continue // 跳过服务端自身
		}
		out = append(out, PeerSnapshot{
			UUID:     uid,
			Addr:     p.connAddr,
			Owned:    int64(len(s.PeerOwners[uid])),
			Total:    total,
			MaxSpeed: p.maxSpeed,
		})
	}
	return out
}

// AddBlockOwner 添加文件拥有
func (s *Session) AddBlockOwner(i int64, uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ChunkOwners[i] == nil {
		s.ChunkOwners[i] = make(map[string]struct{})
	}
	if s.PeerOwners[uuid] == nil {
		s.PeerOwners[uuid] = make(map[int64]struct{})
	}
	s.PeerOwners[uuid][i] = struct{}{}
	s.ChunkOwners[i][uuid] = struct{}{}
}
