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

	var bestUUID string = q.s.UUID
	// 设置主节点的保底得分为 0.5
	// 当所有可用子节点的得分（因连续失败或负载过高）降到 0.5 以下时，调度中心将直接派主节点上场兜底
	var maxScore float64 = 0.5
	const (
		baseWeight = 10.0
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

			// 如果不在同一个子网，直接跳过，不再分配该节点
			if !isReachableSubnet(reqIP, peerIP) {
				continue
			}

			// 失败降权：每次连续失败分数减半（failCount+1 作分母）
			denominator := float64(peer.connNum+1) * float64(peer.failCount+1)
			score := (baseWeight + float64(peer.maxSpeed)) / (denominator * denominator)

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
	// 若已缓存该块的校验值则一并返回，供接收端在下载前预知期望校验和（缺失时为 0，接收端仍会二次 CRC32 校验）
	checkSum := q.s.chunkHash[i]
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
		CheckSum: checkSum,
		UUID:     bestUUID,
	})
	if err := c.EncodeTo(conn, gsp.TypeJSON, jc); err != nil {
		slog.Error("发送 WantChunkResp 失败", "error", err)
	}
}
