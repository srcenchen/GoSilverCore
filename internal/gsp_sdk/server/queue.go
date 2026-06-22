package server

import (
	"encoding/json"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"log/slog"
	"net"
)

// TODO 测试用队列
type queue2 struct {
	s *Session
}

func isReachableSubnet(reqIP, peerIP string) bool {
	netIP1 := net.ParseIP(reqIP)
	netIP2 := net.ParseIP(peerIP)
	if netIP1 == nil || netIP2 == nil {
		return false
	}
	ip1v4 := netIP1.To4()
	ip2v4 := netIP2.To4()
	if ip1v4 != nil && ip2v4 != nil {
		// Compare first 3 bytes (24-bit subnet mask / Class C)
		return ip1v4[0] == ip2v4[0] && ip1v4[1] == ip2v4[1] && ip1v4[2] == ip2v4[2]
	}
	// For IPv6, compare first 48 bits
	ip1v6 := netIP1.To16()
	ip2v6 := netIP2.To16()
	if ip1v6 != nil && ip2v6 != nil {
		return ip1v6[0] == ip2v6[0] && ip1v6[1] == ip2v6[1] && ip1v6[2] == ip2v6[2] &&
			ip1v6[3] == ip2v6[3] && ip1v6[4] == ip2v6[4] && ip1v6[5] == ip2v6[5]
	}
	return false
}

// Want 为请求第 i 块的客户端选出最优数据源。
//
// 调度策略（优先级依次降低）：
//  1. 同子网 Peer（isReachableSubnet 判定），得分不打折
//  2. 跨子网 Peer，得分乘以 0.3 惩罚（仍可用，但不优先）
//  3. 服务端自身（兜底，始终可用，但受 uploadSem 并发限制）
//
// 得分公式：(baseWeight + maxSpeed) / (connNum+1)² × subnetFactor / (failCount+1)
//   - connNum: 当前已分配给此 Peer 但未完成的连接数
//   - failCount: 连续下载失败次数，成功后清零
//   - subnetFactor: 同子网=1.0，跨子网=0.3
func (q *queue2) Want(i int64, conn net.Conn) {
	c := gsp.Codec{}

	reqIP := conn.RemoteAddr().String()
	if host, _, err := net.SplitHostPort(reqIP); err == nil {
		reqIP = host
	}

	q.s.mu.RLock()

	var bestUUID string
	var maxScore float64 = -1.0
	const (
		baseWeight          = 10.0
		crossSubnetPenalty  = 0.3 // 跨子网惩罚系数，>0 保证跨子网节点仍可使用
	)

	owners := q.s.ChunkOwners[i]
	if len(owners) == 0 {
		bestUUID = q.s.UUID
	} else {
		for uid := range owners {
			if uid == q.s.UUID {
				continue // 服务端本身作为兜底，不参与打分循环
			}
			peer, ok := q.s.Peers[uid]
			if !ok || peer == nil {
				continue
			}

			peerIP := peer.connAddr
			if host, _, err := net.SplitHostPort(peerIP); err == nil {
				peerIP = host
			}

			// 子网系数：同子网满分，跨子网打折但不排除
			subnetFactor := 1.0
			if !isReachableSubnet(reqIP, peerIP) {
				subnetFactor = crossSubnetPenalty
			}

			// 失败降权：每次连续失败分数减半（failCount+1 作分母）
			denominator := float64(peer.connNum+1) * float64(peer.failCount+1)
			score := (baseWeight + float64(peer.maxSpeed)) / (denominator * denominator) * subnetFactor

			if score > maxScore {
				maxScore = score
				bestUUID = uid
			}
		}
	}

	if bestUUID == "" {
		bestUUID = q.s.UUID
	}

	targetPeer, ok := q.s.Peers[bestUUID]
	var targetAddr string
	if !ok || bestUUID == q.s.UUID {
		bestUUID = q.s.UUID
		targetAddr = ""
	} else {
		targetAddr = targetPeer.connAddr
	}
	q.s.mu.RUnlock()

	// 为选定的 Peer 递增活跃连接数
	q.s.mu.Lock()
	if p, ok := q.s.Peers[bestUUID]; ok {
		p.connNum++
	}
	q.s.mu.Unlock()

	jc, _ := json.Marshal(model.WantChunkResp{
		Index:    i,
		Addr:     targetAddr,
		CheckSum: 0,
		UUID:     bestUUID,
	})
	if err := c.EncodeTo(conn, gsp.TypeJSON, jc); err != nil {
		slog.Error("发送 WantChunkResp 失败", "error", err)
	}
}
