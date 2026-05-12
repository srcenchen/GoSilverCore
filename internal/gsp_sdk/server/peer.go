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
