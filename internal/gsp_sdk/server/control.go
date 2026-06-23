package server

import (
	"encoding/json"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"log/slog"
	"net"
	"time"
)

// 控制面（统一指挥）：中心经各 peer 的 PeerReg 长连接主动下发指令。
// 核心职责是「每机房各保留一份完整副本」——周期性扫描每个机房缺失的块，
// 命令该机房内最接近做种的节点 prefetch 补齐，从而把中心出口需求压到 O(机房数)。

const (
	coordinatorInterval       = 5 * time.Second // 协调器扫描周期
	maxPrefetchPerZonePerTick = 4               // 每机房每轮最多下发的 prefetch 数（节流）
	backoffSlowMbps           = 250.0           // 低于此上行视为 100M/HDD 慢源
	backoffOverloadConn       = 4               // 慢源活跃租约超过此值则下发 backoff
	backoffSeconds            = 15              // backoff 持续秒数
)

// SetControlConn 记录某 peer 的控制长连接（在 PeerReg 时调用）。
func (s *Session) SetControlConn(uuid string, conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.Peers[uuid]; ok && p != nil {
		p.controlConn = conn
	}
}

// sendControl 向指定 peer 下发一条控制指令。由单一协调器 goroutine 调用，写入串行。
func (s *Session) sendControl(uuid string, msg model.ControlMsg) {
	s.mu.RLock()
	var conn net.Conn
	if p, ok := s.Peers[uuid]; ok && p != nil {
		conn = p.controlConn
	}
	s.mu.RUnlock()
	if conn == nil {
		return
	}
	b, _ := json.Marshal(msg)
	codec := gsp.Codec{}
	if err := codec.EncodeTo(conn, gsp.TypeJSON, b); err != nil {
		slog.Warn("下发控制指令失败", "uuid", uuid, "cmd", msg.Cmd, "err", err)
	}
}

// StartCoordinator 启动中心协调器（仅主发送端调用）。
func (s *Session) StartCoordinator() {
	go func() {
		ticker := time.NewTicker(coordinatorInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
				s.coordinateOnce()
			}
		}
	}()
}

// coordinateOnce 执行一轮协调：补齐各机房缺块 + 对过载慢源下发 backoff。
func (s *Session) coordinateOnce() {
	total := s.chunkProvider.GetChunkNum()
	if total <= 0 {
		return
	}

	type zoneInfo struct {
		owned     map[int64]struct{} // 该机房（任一节点）已拥有的块
		bestUUID  string             // 该机房内最接近做种的节点（owned 最多）
		bestOwned int
	}

	s.mu.RLock()
	zones := map[string]*zoneInfo{}
	for uid, p := range s.Peers {
		if uid == s.UUID || p == nil || p.zone == "" {
			continue
		}
		zi := zones[p.zone]
		if zi == nil {
			zi = &zoneInfo{owned: map[int64]struct{}{}}
			zones[p.zone] = zi
		}
		ownedCnt := len(s.PeerOwners[uid])
		for c := range s.PeerOwners[uid] {
			zi.owned[c] = struct{}{}
		}
		if zi.bestUUID == "" || ownedCnt > zi.bestOwned {
			zi.bestUUID = uid
			zi.bestOwned = ownedCnt
		}
	}

	type outCmd struct {
		uuid string
		msg  model.ControlMsg
	}
	var cmds []outCmd

	// 每机房缺块 prefetch：命令该机房最接近做种的节点补齐缺失块。
	for _, zi := range zones {
		if zi.bestUUID == "" {
			continue
		}
		sent := 0
		for c := int64(0); c < total && sent < maxPrefetchPerZonePerTick; c++ {
			if _, ok := zi.owned[c]; ok {
				continue
			}
			cmds = append(cmds, outCmd{zi.bestUUID, model.ControlMsg{Cmd: "prefetch", Index: c}})
			sent++
		}
	}

	// 过载慢源 backoff：低上行(100M/HDD) 且活跃租约偏高 → 命令其降并发。
	until := time.Now().Add(backoffSeconds * time.Second).Unix()
	for uid, p := range s.Peers {
		if uid == s.UUID || p == nil {
			continue
		}
		if p.upMbpsEWMA > 0 && p.upMbpsEWMA < backoffSlowMbps && p.connNum() > backoffOverloadConn {
			cmds = append(cmds, outCmd{uid, model.ControlMsg{Cmd: "backoff", Until: until}})
		}
	}
	s.mu.RUnlock()

	for _, c := range cmds {
		s.sendControl(c.uuid, c.msg)
	}
}
