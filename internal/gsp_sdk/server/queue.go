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

func (q *queue2) Want(i int64, conn net.Conn) {
	c := gsp.Codec{}

	// 锁定 Session 读取数据，保证线程安全
	q.s.mu.RLock()

	var bestUUID string
	var maxScore float64 = -1.0
	const baseWeight = 10.0
	// 1. 遍历所有拥有此分块的 Peer
	owners := q.s.ChunkOwners[i]
	if len(owners) == 0 {
		// 如果没人有，默认指向自己（或者报错）
		bestUUID = q.s.UUID
	} else {
		for uid := range owners {
			if uid == q.s.UUID {
				continue
			}

			peer, ok := q.s.Peers[uid]
			if !ok {
				continue
			}

			// 核心算法：基础权重+实际速度 / (连接数+1)的平方
			// 使用平方来快速衰减高并发节点的分数
			denominator := float64(peer.connNum + 1)
			score := (baseWeight + float64(peer.maxSpeed)) / (denominator * denominator)

			if score > maxScore {
				maxScore = score
				bestUUID = uid
			}
		}
	}

	// 如果遍历完发现没有合适的第三方节点，回退到自己
	if bestUUID == "" {
		bestUUID = q.s.UUID
	}

	// 获取最终选定的 Peer 信息
	targetPeer, ok := q.s.Peers[bestUUID]
	var targetAddr string
	if !ok || bestUUID == q.s.UUID {
		bestUUID = q.s.UUID
		targetAddr = q.s.addr // Session 自身的地址
	} else {
		targetAddr = targetPeer.connAddr
	}
	q.s.mu.RUnlock() // 释放读锁

	// 3. 更新连接数计数（需要写锁）
	q.s.mu.Lock()
	if p, ok := q.s.Peers[bestUUID]; ok {
		p.connNum++
	}
	q.s.mu.Unlock()

	// 4. 编码并发送响应
	jc, _ := json.Marshal(model.WantChunkResp{
		Index:    i,
		Addr:     targetAddr,
		CheckSum: 0, // 实际应用中应计算校验和
		UUID:     bestUUID,
	})

	if err := c.EncodeTo(conn, gsp.TypeJSON, jc); err != nil {
		slog.Error("发送 WantChunkResp 失败", "error", err)
	}
}
