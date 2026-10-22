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
	"strconv"
	"time"
)

const (
	leaseTTL    = 45 * time.Second
	maxInflight = 2
)

type queue2 struct {
	s *Session
}

func (q *queue2) Want(i int64, conn net.Conn) {
	q.WantFor(i, conn, "")
}

func (q *queue2) WantFor(i int64, conn net.Conn, requester string) {
	q.WantRequest(model.WantChunkReq{Index: i, UUID: requester, Want: 1}, conn)
}

func (q *queue2) WantRequest(req model.WantChunkReq, conn net.Conn) {
	q.s.mu.Lock()
	candidates := q.candidatesLocked(req, conn)
	n := req.Want
	if n <= 0 {
		n = 1
	}
	n = min(n, _const.SchedMaxSourcesPerWant, len(candidates))
	sources := make([]model.SourceCandidate, 0, n)
	for _, c := range candidates[:n] {
		sources = append(sources, model.SourceCandidate{Addr: c.addr, UUID: c.uuid})
	}
	first := candidates[0]
	q.leaseLocked(first.uuid, req.Index, req.UUID)
	checksum := q.s.chunkHash[req.Index]
	q.s.mu.Unlock()
	response, _ := json.Marshal(model.WantChunkResp{Index: req.Index, Addr: first.addr, UUID: first.uuid, CheckSum: checksum, Sources: sources})
	if err := (&gsp.Codec{}).EncodeTo(conn, gsp.TypeJSON, response); err != nil {
		slog.Error("发送 WantChunkResp 失败", "error", err)
		q.release(first.uuid, req.Index, req.UUID)
	}
}

type scoredCandidate struct {
	uuid, addr string
	score      float64
}

func scoreSource(up float64, connections int, locality, failures float64) float64 {
	return up / float64(connections+1) * locality * math.Pow(0.5, failures)
}
func localityFactor(source, requester string) float64 {
	if source == zoneOfCentral || requester == "" || source == requester {
		return 1
	}
	return _const.SchedCrossZoneFactor
}
func (q *queue2) candidatesLocked(req model.WantChunkReq, conn net.Conn) []scoredCandidate {
	now := time.Now()
	q.s.expireLeasesLocked(now)
	zone := req.Zone
	if zone == "" {
		if p := q.s.Peers[req.UUID]; p != nil {
			zone = p.zone
		}
	}
	if zone == "" && conn != nil {
		zone = zoneOf(conn.RemoteAddr().String(), "")
	}
	localOwners := 0
	var result []scoredCandidate
	for id := range q.s.ChunkOwners[req.Index] {
		if id == req.UUID {
			continue
		}
		p := q.s.Peers[id]
		if p == nil || id == q.s.UUID {
			continue
		}
		if p.zone == zone {
			localOwners++
		}
		if now.Before(p.cooldownTil) || p.connNum >= maxInflight {
			continue
		}
		failures := float64(p.failStreak)
		if !p.lastFailure.IsZero() {
			failures *= math.Pow(0.5, now.Sub(p.lastFailure).Seconds()/_const.SchedFailHalfLifeSec)
		}
		speed := p.speedEwma
		if speed <= 0 {
			speed = _const.SchedColdStartMbps
		}
		result = append(result, scoredCandidate{id, p.connAddr, scoreSource(speed, p.connNum, localityFactor(p.zone, zone), failures)})
	}
	central := q.s.Peers[q.s.UUID]
	speed := float64(_const.SchedColdStartMbps)
	connections := 0
	if central != nil {
		connections = central.connNum
		if central.speedEwma > 0 {
			speed = central.speedEwma
		}
	}
	score := scoreSource(speed, connections, 1, 0)
	if localOwners == 0 && zone != "" {
		score *= 1e9
	}
	result = append(result, scoredCandidate{q.s.UUID, "", score})
	sort.Slice(result, func(i, j int) bool {
		if result[i].score == result[j].score {
			return result[i].uuid < result[j].uuid
		}
		return result[i].score > result[j].score
	})
	return result
}
func (q *queue2) leaseLocked(id string, index int64, requester string) {
	p := q.s.Peers[id]
	if p == nil {
		p = &Peer{leases: map[string]time.Time{}}
		q.s.Peers[id] = p
	}
	if p.leases == nil {
		p.leases = map[string]time.Time{}
	}
	lease := leaseKey(requester, index)
	if _, exists := p.leases[lease]; !exists {
		p.connNum++
	}
	p.leases[lease] = time.Now().Add(leaseTTL)
}
func (q *queue2) pick(i int64, conn net.Conn) (string, string) { return q.pickFor(i, conn, "") }
func (q *queue2) pickFor(i int64, conn net.Conn, requester string) (string, string) {
	q.s.mu.Lock()
	defer q.s.mu.Unlock()
	if requester == "" {
		requester = q.s.peerByConn(conn)
	}
	candidate := q.candidatesLocked(model.WantChunkReq{Index: i, UUID: requester}, conn)[0]
	q.leaseLocked(candidate.uuid, i, requester)
	return candidate.uuid, candidate.addr
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
