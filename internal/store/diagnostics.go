// diagnostics.go — node-availability tracking and channel-utilization aggregation.
// All methods share Store.mu for thread safety.

package store

import (
	"sort"
	"time"
)

const (
	// offlineThresholdSecs: a node not heard for this long is considered offline.
	offlineThresholdSecs = 30 * 60 // 30 min
	// checkIntervalSecs: how often the scanner looks for offline transitions.
	checkIntervalSecs = 60
)

// AvailTransition is an online/offline event emitted by the scanner.
type AvailTransition struct {
	Time    int64
	NodeNum uint32
	Event   string // "online" or "offline"
}

// nodeAvailState is the per-node internal state for the scanner.
type nodeAvailState struct {
	lastHeard int64
	isOnline  bool
}

// availData stores the scanner's state. Lazily initialised on first call.
type availData struct {
	nodes map[uint32]*nodeAvailState
}

// MarkNodeHeard updates the availability tracker when a packet arrives from
// a node. Returns a transition (or nil if state didn't change).
// Must be called with s.mu held (called from Add/AddSilent).
func (s *Store) MarkNodeHeard(nodeNum uint32, t time.Time) *AvailTransition {
	if nodeNum == 0 {
		return nil
	}
	if s.avail == nil {
		s.avail = &availData{nodes: make(map[uint32]*nodeAvailState)}
	}
	now := t.Unix()
	st := s.avail.nodes[nodeNum]
	if st == nil {
		st = &nodeAvailState{}
		s.avail.nodes[nodeNum] = st
	}
	st.lastHeard = now
	if !st.isOnline {
		st.isOnline = true
		return &AvailTransition{Time: now, NodeNum: nodeNum, Event: "online"}
	}
	return nil
}

// ScanOffline checks all tracked nodes for offline transitions. Returns any
// new "offline" transitions. Call this periodically (e.g. every 60 s) from a
// goroutine that holds NO lock — the method acquires s.mu internally.
func (s *Store) ScanOffline() []AvailTransition {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.avail == nil {
		return nil
	}
	now := time.Now().Unix()
	var out []AvailTransition
	for nodeNum, st := range s.avail.nodes {
		if st.isOnline && (now-st.lastHeard) > offlineThresholdSecs {
			st.isOnline = false
			out = append(out, AvailTransition{
				Time:    st.lastHeard + offlineThresholdSecs,
				NodeNum: nodeNum,
				Event:   "offline",
			})
		}
	}
	return out
}

// NodeUptime computes the uptime percentage for a node over a time window
// using the provided availability events (assumed oldest-first, already
// loaded from DB). Returns uptime as a fraction in [0,1].
func NodeUptime(events []AvailTransition, windowStart, windowEnd int64) float64 {
	if windowEnd <= windowStart {
		return 0
	}
	total := float64(windowEnd - windowStart)
	online := float64(0)
	// Walk the events and accumulate online time.
	lastOn := int64(0)
	wasOnline := false
	for _, e := range events {
		ts := e.Time
		if ts < windowStart {
			// If it happened before our window, just record the state.
			wasOnline = e.Event == "online"
			lastOn = ts
			continue
		}
		if ts > windowEnd {
			break
		}
		if wasOnline {
			start := lastOn
			if start < windowStart {
				start = windowStart
			}
			online += float64(ts - start)
		}
		wasOnline = e.Event == "online"
		lastOn = ts
	}
	// Tail: if last state was online, count until windowEnd.
	if wasOnline {
		start := lastOn
		if start < windowStart {
			start = windowStart
		}
		online += float64(windowEnd - start)
	}
	if online > total {
		online = total
	}
	return online / total
}

// ---- Channel Utilization Aggregation ----

// ChannelUtilAgg is a snapshot of the mesh-wide channel utilization.
type ChannelUtilAgg struct {
	Time           int64   `json:"time"`
	NodesReporting int     `json:"nodes_reporting"`
	AvgChanUtil    float64 `json:"avg_chan_util"`
	MaxChanUtil    float64 `json:"max_chan_util"`
	AvgAirUtil     float64 `json:"avg_air_util"`
	MaxAirUtil     float64 `json:"max_air_util"`
	TopTalkerNum   uint32  `json:"top_talker_num"`
	TopTalkerName  string  `json:"top_talker_name,omitempty"`
	TopTalkerUtil  float64 `json:"top_talker_util"`
	// Alert threshold breached.
	Congested bool `json:"congested"` // avg_chan_util > 25%
}

// AggregateChannelUtil computes a mesh-wide snapshot from nodes heard in the
// last 30 minutes. Safe to call from any goroutine; acquires s.mu.
func (s *Store) AggregateChannelUtil() ChannelUtilAgg {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now().Unix()
	var (
		sumChan, sumAir   float64
		maxChan, maxAir   float64
		topNum            uint32
		topAir            float64
		count             int
	)
	for _, n := range s.nodes {
		if (now - n.LastHeard) > offlineThresholdSecs {
			continue
		}
		cu := float64(n.ChannelUtilization)
		au := float64(n.AirUtilTx)
		if cu == 0 && au == 0 {
			continue // no telemetry received yet
		}
		count++
		sumChan += cu
		sumAir += au
		if cu > maxChan {
			maxChan = cu
		}
		if au > maxAir {
			maxAir = au
		}
		if au > topAir {
			topAir = au
			topNum = n.NodeNum
		}
	}
	agg := ChannelUtilAgg{
		Time:           now,
		NodesReporting: count,
		MaxChanUtil:    maxChan,
		MaxAirUtil:     maxAir,
		TopTalkerNum:   topNum,
		TopTalkerUtil:  topAir,
	}
	if count > 0 {
		agg.AvgChanUtil = sumChan / float64(count)
		agg.AvgAirUtil = sumAir / float64(count)
	}
	agg.Congested = agg.AvgChanUtil > 25
	if topNum != 0 {
		if n, ok := s.nodes[topNum]; ok {
			if n.ShortName != "" {
				agg.TopTalkerName = n.ShortName
			} else if n.LongName != "" {
				agg.TopTalkerName = n.LongName
			}
		}
	}
	return agg
}

// ---- Node list with availability info ----

// NodeDiag extends NodeState with signal sparkline hint and availability.
type NodeDiag struct {
	NodeNum       uint32  `json:"node_num"`
	Name          string  `json:"name"`
	IsOnline      bool    `json:"is_online"`
	LastHeard     int64   `json:"last_heard"`
	RSSI          int32   `json:"rssi"`
	SNR           float32 `json:"snr"`
	BatteryLevel  uint32  `json:"battery_level"`
	Voltage       float32 `json:"voltage"`
	ChanUtil      float32 `json:"chan_util"`
	AirUtilTx     float32 `json:"air_util_tx"`
	// Uptime1h / 24h computed externally when availability events are loaded.
}

// NodesDiag returns a diagnostic summary of all known nodes, sorted by last_heard desc.
func (s *Store) NodesDiag() []NodeDiag {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().Unix()
	out := make([]NodeDiag, 0, len(s.nodes))
	for _, n := range s.nodes {
		name := n.LongName
		if name == "" {
			name = n.ShortName
		}
		if name == "" {
			name = n.ID
		}
		online := (now - n.LastHeard) < offlineThresholdSecs
		out = append(out, NodeDiag{
			NodeNum:      n.NodeNum,
			Name:         name,
			IsOnline:     online,
			LastHeard:    n.LastHeard,
			RSSI:         n.RSSI,
			SNR:          n.SNR,
			BatteryLevel: n.BatteryLevel,
			Voltage:      n.Voltage,
			ChanUtil:     n.ChannelUtilization,
			AirUtilTx:    n.AirUtilTx,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastHeard > out[j].LastHeard })
	return out
}
