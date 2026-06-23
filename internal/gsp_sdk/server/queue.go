package server

import (
	"encoding/json"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"log/slog"
	"math"
	"net"
	"sort"
	"time"
)

type queue2 struct {
	s *Session
}

// scoreSource 计算候选源 S 服务一个块给请求方的期望吞吐（调度打分核心）。
//
//	T(S) = U_eff(S) / (connNum(S)+1) × L(S,R) × Rel(S)
//
//   - up        : S 的有效上行估计 U_eff（Mbps，已含磁盘瓶颈）
//   - connNum   : S 当前未完成的调度租约数；线性分母 = 物理公平分享（吞吐/连接数），
//     使贪心 argmax 收敛到最大-最小公平/注水解，逼近全局 max-flow
//   - locality  : 局部性 L —— 同机房/中心=1.0，跨机房=β
//   - effFail   : 时间衰减后的失败分，Rel = 0.5^effFail
func scoreSource(up float64, connNum int, locality float64, effFail float64) float64 {
	rel := math.Pow(0.5, effFail)
	return up / float64(connNum+1) * locality * rel
}

// localityFactor 返回源机房 srcZone 相对请求方机房 reqZone 的局部性系数。
// 中心（zone="*"）对所有机房可达，记 1.0（其稀缺出口由 connNum 计入分享）。
func localityFactor(srcZone, reqZone string) float64 {
	if srcZone == zoneOfCentral {
		return 1.0
	}
	if reqZone == "" || srcZone == reqZone {
		return 1.0 // 无法判定机房时不惩罚
	}
	return _const.SchedCrossZoneFactor
}

// leaseTTL 估算一个调度租约的存活时间：约 3 倍单块传输时长，下限 5s。
// 死客户端的租约到期后自动回收，不会永久占用 connNum。
func leaseTTL(upMbps float64) time.Duration {
	if upMbps <= 0 {
		upMbps = _const.SchedColdStartMbps
	}
	chunkBits := float64(_const.ChunkSize) * 8
	secs := chunkBits / (upMbps * 1e6) // 单块传输秒数
	ttl := 3 * secs
	if ttl < _const.SchedLeaseMinTTLSec {
		ttl = _const.SchedLeaseMinTTLSec
	}
	return time.Duration(ttl * float64(time.Second))
}

type scoredCandidate struct {
	uuid  string
	addr  string
	score float64
}

// Want 为请求第 i 块的客户端选出一个按期望吞吐降序排好的候选源列表。
//
// 策略要点（hub-and-spoke 拓扑）：
//  1. 同机房 peer 优先（L=1），跨机房 peer 重罚（L=β），中心兜底；
//  2. 每机房稀缺度播种：若请求方机房尚无任何节点拥有该块，强制中心为 #1，
//     让中心给这个机房播第一份 → 中心出口 ≈ 块数×机房数，而非 块数×节点数；
//  3. 返回 Top-N，客户端可本地故障转移，无需为失败再回中心重问。
// buildCandidates 计算块 i 对请求方机房 reqZone 的候选源，按期望吞吐降序排好。
// 这是调度打分的纯逻辑（含每机房稀缺度播种），抽出以便单测。
// 调用方必须持有 q.s.mu（写锁，因为会 pruneLeases）。
func (q *queue2) buildCandidates(i int64, reqZone string, nowNano, nowSec int64) []scoredCandidate {
	owners := q.s.ChunkOwners[i]
	// 统计请求方机房内已拥有该块的节点数（不含中心），用于每机房稀缺度播种判定。
	sameZoneOwners := 0
	cands := make([]scoredCandidate, 0, len(owners)+1)
	for uid := range owners {
		if uid == q.s.UUID {
			continue // 中心单独作为兜底候选加入
		}
		peer, ok := q.s.Peers[uid]
		if !ok || peer == nil {
			continue
		}
		peer.pruneLeases(nowNano)
		if peer.zone == reqZone && reqZone != "" {
			sameZoneOwners++
		}
		loc := localityFactor(peer.zone, reqZone)
		score := scoreSource(peer.effUp(), peer.connNum(), loc, peer.effFail(nowSec))
		cands = append(cands, scoredCandidate{uuid: uid, addr: peer.connAddr, score: score})
	}

	// 中心服务端作为始终可用的候选（addr="" 表示中心）。
	if central, ok := q.s.Peers[q.s.UUID]; ok && central != nil {
		central.pruneLeases(nowNano)
		score := scoreSource(central.effUp(), central.connNum(), 1.0, central.effFail(nowSec))
		// 每机房稀缺度播种：该机房还没有这块 → 中心强制 #1，给机房播第一份。
		if sameZoneOwners == 0 {
			score *= 1e9
		}
		cands = append(cands, scoredCandidate{uuid: q.s.UUID, addr: "", score: score})
	}

	// 按期望吞吐降序排序。
	sort.SliceStable(cands, func(a, b int) bool { return cands[a].score > cands[b].score })
	return cands
}

func (q *queue2) Want(req model.WantChunkReq, conn net.Conn) {
	i := req.Index
	c := gsp.Codec{}

	reqZone := req.Zone
	if reqZone == "" {
		reqZone = zoneOf(conn.RemoteAddr().String(), "")
	}

	nowNano := time.Now().UnixNano()
	nowSec := time.Now().Unix()

	q.s.mu.Lock()

	cands := q.buildCandidates(i, reqZone, nowNano, nowSec)

	want := req.Want
	if want <= 0 {
		want = 1
	}
	if want > _const.SchedMaxSourcesPerWant {
		want = _const.SchedMaxSourcesPerWant
	}
	if want > len(cands) {
		want = len(cands)
	}

	sources := make([]model.SourceCandidate, 0, want)
	for k := 0; k < want; k++ {
		sources = append(sources, model.SourceCandidate{Addr: cands[k].addr, UUID: cands[k].uuid})
	}

	// 仅为首选记一个带 TTL 的租约（客户端优先用 #1）；故障转移到次选时，
	// 客户端会对 #1 上报 failed 释放该租约，次选的 connNum 由其后续上报自校正。
	if len(cands) > 0 {
		if p, ok := q.s.Peers[cands[0].uuid]; ok && p != nil {
			p.addLease(nowNano, int64(leaseTTL(p.effUp())))
		}
	}

	checkSum := q.s.chunkHash[i]
	q.s.mu.Unlock()

	// 极端兜底：理论上 central 一定在列表里，这里防御性补一个中心源。
	if len(sources) == 0 {
		sources = append(sources, model.SourceCandidate{Addr: "", UUID: q.s.UUID})
	}

	resp, _ := json.Marshal(model.WantChunkResp{
		Index:    i,
		CheckSum: checkSum,
		Sources:  sources,
	})
	if err := c.EncodeTo(conn, gsp.TypeJSON, resp); err != nil {
		slog.Error("发送 WantChunkResp 失败", "error", err)
	}
}
