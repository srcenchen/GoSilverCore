package server

import (
	"encoding/json"
	"fmt"
	_const "go-silver-core/internal/const"
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
		p.conn = conn
	}
}

// sendControl 向指定 peer 下发一条控制指令。由单一协调器 goroutine 调用，写入串行。
func (s *Session) sendControl(uuid string, msg model.ControlMsg) {
	s.mu.RLock()
	var conn net.Conn
	if p, ok := s.Peers[uuid]; ok && p != nil {
		conn = p.conn
	}
	s.mu.RUnlock()
	if conn == nil {
		return
	}
	s.mu.RLock()
	peer := s.Peers[uuid]
	s.mu.RUnlock()
	if peer == nil {
		return
	}
	peer.sendMu.Lock()
	defer peer.sendMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})
	b, _ := json.Marshal(msg)
	codec := gsp.Codec{}
	if err := codec.EncodeTo(conn, gsp.TypeJSON, b); err != nil {
		slog.Warn("下发控制指令失败", "uuid", uuid, "cmd", msg.Cmd, "err", err)
	}
}

// StartCoordinator 启动中心协调器（仅主发送端调用）。
func (s *Session) StartCoordinator() {
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		return
	default:
	}
	if s.coordinating {
		s.mu.Unlock()
		return
	}
	s.coordinating = true
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
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
	s.mu.RLock()
	if s.chunkProvider.FileStat == nil {
		s.mu.RUnlock()
		return
	}
	total := s.chunkProvider.GetChunkNum()
	if total <= 0 {
		s.mu.RUnlock()
		return
	}

	type zoneInfo struct {
		owned     map[int64]struct{} // 该机房（任一节点）已拥有的块
		bestUUID  string             // 该机房内最接近做种的节点（owned 最多）
		bestOwned int
	}

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
		if p.speedEwma > 0 && p.speedEwma < backoffSlowMbps && p.connNum > backoffOverloadConn {
			cmds = append(cmds, outCmd{uid, model.ControlMsg{Cmd: "backoff", Until: until}})
		}
	}
	s.mu.RUnlock()

	for _, c := range cmds {
		s.sendControl(c.uuid, c.msg)
	}
}

const zoneOfCentral = "*"

func zoneOf(ipPort, explicit string) string {
	if explicit != "" {
		return explicit
	}
	host := ipPort
	if h, _, err := net.SplitHostPort(ipPort); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.0/%d", v4[0], v4[1], v4[2], _const.SchedDefaultZonePrefixBits)
	}
	v6 := ip.To16()
	return fmt.Sprintf("%x:%x:%x::/48", v6[0:2], v6[2:4], v6[4:6])
}
func (s *Session) SetPeerZone(uuid, zone string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.Peers[uuid]; p != nil {
		p.zone = zoneOf(p.connAddr, zone)
	}
}

// SetPrefetchHandler feeds the bounded download workers, so coordinator requests
// never create duplicate writers or an unbounded number of goroutines.
func (s *Session) SetPrefetchHandler(fn func(int64)) { s.mu.Lock(); s.prefetch = fn; s.mu.Unlock() }
func (s *Session) ApplyControl(msg model.ControlMsg) {
	s.mu.Lock()
	prefetch := s.prefetch
	if msg.Cmd == "backoff" {
		until := time.Unix(msg.Until, 0)
		if until.After(s.backoffUntil) {
			s.backoffUntil = until
		}
	}
	s.mu.Unlock()
	if msg.Cmd == "prefetch" && prefetch != nil {
		prefetch(msg.Index)
	}
}
