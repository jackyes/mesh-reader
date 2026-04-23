// Package store — DX (long-distance reception) leaderboard.
//
// For each remote node we keep the longest direct (hop=0) reception ever
// observed: distance from us in km, the RSSI/SNR at that moment, and time.
// If no direct reception is known we fall back to the longest relayed
// reception so the leaderboard isn't empty for distant relayed-only nodes.
//
// Requires our own node position (from MyInfo + Position) AND the remote
// node's position. trackDX is a no-op until both are known.
package store

import (
	"sort"

	"mesh-reader/internal/decoder"
)

// DXRecord is the best long-distance reception observed for a node.
type DXRecord struct {
	NodeNum    uint32  `json:"node_num"`
	NodeName   string  `json:"node_name"`
	DistanceKm float64 `json:"distance_km"`
	RSSI       int32   `json:"rssi"`
	SNR        float32 `json:"snr"`
	Time       int64   `json:"time"`
	Direct     bool    `json:"direct"`    // true if hops_used == 0
	HopsUsed   uint32  `json:"hops_used"` // 0 if direct (or unknown)
}

// trackDX updates the per-node DX record from this event if it's a better
// reception than what we already have. MUST be called with s.mu held.
func (s *Store) trackDX(ev *decoder.Event) {
	if ev == nil || ev.FromNode == 0 || ev.FromNode == s.myNodeNum {
		return
	}
	// Need air-RX evidence (RSSI or SNR) — telemetry without signal is useless here.
	if ev.RSSI == 0 && ev.SNR == 0 {
		return
	}
	if s.dx == nil {
		s.dx = make(map[uint32]DXRecord)
	}
	me, okMe := s.nodes[s.myNodeNum]
	if !okMe || !me.HasPos {
		return
	}
	src, okSrc := s.nodes[ev.FromNode]
	if !okSrc || !src.HasPos {
		return
	}
	km := haversineKmInternal(me.Lat, me.Lon, src.Lat, src.Lon)
	if km <= 0 {
		return
	}

	// Hop accounting: HopStart - HopLimit = hops actually used. Only trust
	// direct (=0) when HopStart > 0; if HopStart is 0 we don't know if it
	// was direct or just missing metadata, so treat as relayed.
	var hopsUsed uint32
	direct := false
	if ev.HopStart > 0 {
		if ev.HopLimit <= ev.HopStart {
			hopsUsed = ev.HopStart - ev.HopLimit
		}
		direct = (hopsUsed == 0)
	}

	cur, exists := s.dx[ev.FromNode]
	better := !exists ||
		(direct && !cur.Direct) || // any direct beats any relayed
		(direct == cur.Direct && km > cur.DistanceKm)
	if !better {
		return
	}
	s.dx[ev.FromNode] = DXRecord{
		NodeNum:    ev.FromNode,
		NodeName:   s.resolveNodeName(ev.FromNode),
		DistanceKm: km,
		RSSI:       ev.RSSI,
		SNR:        ev.SNR,
		Time:       ev.Time.Unix(),
		Direct:     direct,
		HopsUsed:   hopsUsed,
	}
}

// DXLeaderboard returns the top-N DX records sorted by SNR descending
// (ties broken by distance descending). If directOnly is true, relayed
// records are excluded.
func (s *Store) DXLeaderboard(limit int, directOnly bool) []DXRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.dx == nil {
		return []DXRecord{}
	}
	out := make([]DXRecord, 0, len(s.dx))
	for _, r := range s.dx {
		if directOnly && !r.Direct {
			continue
		}
		// Refresh name (may have changed since the record was set).
		r.NodeName = s.resolveNodeName(r.NodeNum)
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SNR != out[j].SNR {
			return out[i].SNR > out[j].SNR
		}
		return out[i].DistanceKm > out[j].DistanceKm
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// BackfillDX replays the ring buffer to populate DX records on startup
// (after LoadEvents). Useful so the leaderboard is non-empty after restart.
func (s *Store) BackfillDX() {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := s.count
	if total > s.maxEvents {
		total = s.maxEvents
	}
	for i := 0; i < total; i++ {
		idx := (s.head - 1 - i + s.maxEvents) % s.maxEvents
		ev := s.events[idx]
		if ev != nil {
			s.trackDX(ev)
		}
	}
}
