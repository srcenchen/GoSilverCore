package server

import (
	"encoding/json"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"log/slog"
	"math"
	"net"
	"strconv"
	"time"
)

const (
	leaseTTL    = 45 * time.Second
	maxInflight = 2
	baseWeight  = 20.0
)

type queue2 struct {
	s *Session
}

func (q *queue2) Want(i int64, conn net.Conn) {
	q.WantFor(i, conn, "")
}

func (q *queue2) WantFor(i int64, conn net.Conn, requester string) {
	bestUUID, targetAddr := q.pickFor(i, conn, requester)

	jc, err := json.Marshal(model.WantChunkResp{
		Index:    i,
		Addr:     targetAddr,
		CheckSum: 0,
		UUID:     bestUUID,
	})
	if err != nil {
		return
	}
	c := gsp.Codec{}
	if err := c.EncodeTo(conn, gsp.TypeJSON, jc); err != nil {
		slog.Error("发送 WantChunkResp 失败", "error", err)
		q.release(bestUUID, i, requester)
	}
}

func (q *queue2) pick(i int64, conn net.Conn) (string, string) { return q.pickFor(i, conn, "") }
func (q *queue2) pickFor(i int64, conn net.Conn, requester string) (string, string) {
	now := time.Now()
	q.s.mu.Lock()
	defer q.s.mu.Unlock()

	if requester == "" {
		requester = q.s.peerByConn(conn)
	}
	q.s.expireLeasesLocked(now)

	var bestUUID string
	var maxScore float64 = -1
	for uid := range q.s.ChunkOwners[i] {
		if uid == requester {
			continue
		}
		peer := q.s.Peers[uid]
		if peer == nil || now.Before(peer.cooldownTil) || peer.connNum >= maxInflight {
			continue
		}
		speed := peer.speedEwma
		if speed <= 0 {
			speed = baseWeight
		}
		failPenalty := math.Pow(2, float64(peer.failStreak))
		score := speed / (float64(peer.connNum+1) * failPenalty)
		if score > maxScore {
			maxScore = score
			bestUUID = uid
		}
	}
	if bestUUID == "" {
		bestUUID = q.s.UUID
	}
	peer := q.s.Peers[bestUUID]
	if peer == nil {
		peer = &Peer{leases: map[string]time.Time{}}
		q.s.Peers[bestUUID] = peer
	}
	if peer.leases == nil {
		peer.leases = map[string]time.Time{}
	}
	lease := leaseKey(requester, i)
	if _, ok := peer.leases[lease]; !ok {
		peer.connNum++
	}
	peer.leases[lease] = now.Add(leaseTTL)

	if bestUUID == q.s.UUID {
		return bestUUID, ""
	}
	return bestUUID, peer.connAddr
}

func (q *queue2) release(uuid string, index int64, requester string) {
	lease := leaseKey(requester, index)
	q.s.mu.Lock()
	defer q.s.mu.Unlock()
	peer := q.s.Peers[uuid]
	if peer == nil || peer.leases == nil {
		return
	}
	if _, ok := peer.leases[lease]; !ok {
		return
	}
	delete(peer.leases, lease)
	if peer.connNum > 0 {
		peer.connNum--
	}
}

func (s *Session) expireLeasesLocked(now time.Time) {
	for _, peer := range s.Peers {
		if peer == nil || len(peer.leases) == 0 {
			continue
		}
		for idx, exp := range peer.leases {
			if now.After(exp) {
				delete(peer.leases, idx)
				if peer.connNum > 0 {
					peer.connNum--
				}
			}
		}
	}
}

func (s *Session) peerByConn(conn net.Conn) string {
	if conn == nil || conn.RemoteAddr() == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return ""
	}
	var found string
	for uid, peer := range s.Peers {
		if peer == nil || peer.connAddr == "" {
			continue
		}
		ph, _, err := net.SplitHostPort(peer.connAddr)
		if err != nil || ph != host {
			continue
		}
		if found != "" {
			return ""
		}
		found = uid
	}
	return found
}

func leaseKey(requester string, index int64) string {
	return requester + ":" + strconv.FormatInt(index, 10)
}
