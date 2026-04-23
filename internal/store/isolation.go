package store

import (
	"sort"
	"time"
)

// IsolatedNode is one node classified by how many distinct paths we hear it on.
type IsolatedNode struct {
	NodeNum    uint32  `json:"node_num"`
	ID         string  `json:"id"`
	LongName   string  `json:"long_name"`
	ShortName  string  `json:"short_name"`
	PacketsSeen int    `json:"packets_seen"`
	DirectHits int     `json:"direct_hits"` // pkts heard with no relay (hop_used = 0)
	RelayCount int     `json:"relay_count"` // number of distinct relays used
	Relays     []string `json:"relays"`     // "..XX" labels of relays
	BestRSSI   int32   `json:"best_rssi"`
	BestSNR    float32 `json:"best_snr"`
	LastHeard  int64   `json:"last_heard"`
	// Classification: "direct-only", "spof" (single point of failure),
	// "weak" (direct-only + marginal signal), "healthy"
	Risk       string  `json:"risk"`
	RiskReason string  `json:"risk_reason,omitempty"`
}

// IsolatedNodesReport analyzes the ring buffer to find nodes with fragile
// connectivity. "Isolated" here means reachable only through few distinct paths,
// so a relay failure would cut them off from our node.
//
// Classification (worst first):
//   - "weak"       : direct-only AND best RSSI < -115 or best SNR < -12 (hear it by luck)
//   - "direct-only": only reachable directly (no relay has forwarded for them)
//   - "spof"       : reachable only via 1 single relay (single point of failure)
//   - "healthy"    : 2+ distinct relays observed
//
// Only nodes seen at least minPackets times are included (default 3).
func (s *Store) IsolatedNodesReport(minPackets int) []IsolatedNode {
	if minPackets < 1 {
		minPackets = 3
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	type agg struct {
		packets    int
		direct     int
		relays     map[uint32]struct{}
		bestRSSI   int32
		bestSNR    float32
		lastHeard  time.Time
	}
	byNode := make(map[uint32]*agg)

	if s.maxEvents <= 0 {
		return nil
	}
	total := s.count
	if total > s.maxEvents {
		total = s.maxEvents
	}
	for i := 0; i < total; i++ {
		idx := (s.head - 1 - i + s.maxEvents) % s.maxEvents
		if idx < 0 || idx >= len(s.events) {
			continue
		}
		ev := s.events[idx]
		if ev == nil || ev.FromNode == 0 {
			continue
		}
		// Exclude our own node
		if ev.FromNode == s.myNodeNum {
			continue
		}
		a, ok := byNode[ev.FromNode]
		if !ok {
			a = &agg{relays: make(map[uint32]struct{}), bestRSSI: -9999}
			byNode[ev.FromNode] = a
		}
		a.packets++
		// RelayNode = 0 means we heard it directly (no relay last-byte advertised)
		if ev.RelayNode == 0 {
			a.direct++
		} else {
			a.relays[ev.RelayNode] = struct{}{}
		}
		if ev.RSSI != 0 && ev.RSSI > a.bestRSSI {
			a.bestRSSI = ev.RSSI
		}
		if ev.SNR != 0 && (a.bestSNR == 0 || ev.SNR > a.bestSNR) {
			a.bestSNR = ev.SNR
		}
		if ev.Time.After(a.lastHeard) {
			a.lastHeard = ev.Time
		}
	}

	out := make([]IsolatedNode, 0, len(byNode))
	for num, a := range byNode {
		if a.packets < minPackets {
			continue
		}
		n, _ := s.nodes[num]
		row := IsolatedNode{
			NodeNum:     num,
			PacketsSeen: a.packets,
			DirectHits:  a.direct,
			RelayCount:  len(a.relays),
			Relays:      nil,
			BestRSSI:    a.bestRSSI,
			BestSNR:     a.bestSNR,
			LastHeard:   a.lastHeard.Unix(),
		}
		if row.BestRSSI == -9999 {
			row.BestRSSI = 0
		}
		if n != nil {
			row.ID = n.ID
			row.LongName = n.LongName
			row.ShortName = n.ShortName
		}
		// Collect relay labels
		for rb := range a.relays {
			row.Relays = append(row.Relays, relayLabel(rb))
		}
		sort.Strings(row.Relays)
		// Classify
		switch {
		case row.RelayCount == 0 && row.DirectHits > 0 &&
			(row.BestRSSI < -115 || (row.BestSNR != 0 && row.BestSNR < -12)):
			row.Risk = "weak"
			row.RiskReason = "direct-only with marginal signal"
		case row.RelayCount == 0 && row.DirectHits > 0:
			row.Risk = "direct-only"
			row.RiskReason = "no relay ever forwarded its packets"
		case row.RelayCount == 1:
			row.Risk = "spof"
			row.RiskReason = "only 1 relay path observed"
		default:
			row.Risk = "healthy"
		}
		out = append(out, row)
	}

	// Sort: riskier first, then by packets desc
	risks := map[string]int{"weak": 0, "direct-only": 1, "spof": 2, "healthy": 3}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := risks[out[i].Risk], risks[out[j].Risk]
		if ri != rj {
			return ri < rj
		}
		return out[i].PacketsSeen > out[j].PacketsSeen
	})
	return out
}

func relayLabel(b uint32) string {
	const hex = "0123456789abcdef"
	return ".." + string([]byte{hex[(b>>4)&0xF], hex[b&0xF]})
}
