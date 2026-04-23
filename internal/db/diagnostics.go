package db

import (
	"log"
	"time"
)

// ---- Signal History ----

// SignalSample is one stored RSSI/SNR sample for a node.
type SignalSample struct {
	Time     int64   `json:"time"`
	NodeNum  uint32  `json:"node_num"`
	RSSI     int32   `json:"rssi"`
	SNR      float32 `json:"snr"`
	HopLimit uint32  `json:"hop_limit"`
	HopStart uint32  `json:"hop_start"`
}

// InsertSignal stores one signal sample. Called from main.go on each live packet.
func (d *DB) InsertSignal(s SignalSample) {
	_, err := d.db.Exec(
		`INSERT INTO signal_history (time, node_num, rssi, snr, hop_limit, hop_start)
		 VALUES (?,?,?,?,?,?)`,
		s.Time, s.NodeNum, s.RSSI, s.SNR, s.HopLimit, s.HopStart,
	)
	if err != nil {
		log.Printf("[db] insert signal: %v", err)
	}
}

// LoadSignalHistory returns the last `limit` samples for a node, oldest first.
func (d *DB) LoadSignalHistory(nodeNum uint32, limit int) []SignalSample {
	if limit <= 0 {
		limit = 200
	}
	rows, err := d.db.Query(
		`SELECT time, node_num, rssi, snr, hop_limit, hop_start
		 FROM signal_history
		 WHERE node_num = ?
		 ORDER BY id DESC LIMIT ?`, nodeNum, limit)
	if err != nil {
		log.Printf("[db] load signal history: %v", err)
		return nil
	}
	defer rows.Close()
	var out []SignalSample
	for rows.Next() {
		var s SignalSample
		if err := rows.Scan(&s.Time, &s.NodeNum, &s.RSSI, &s.SNR, &s.HopLimit, &s.HopStart); err != nil {
			continue
		}
		out = append(out, s)
	}
	// Reverse to oldest-first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// SignalTrend is the output of ComputeSignalTrends for one node.
type SignalTrend struct {
	NodeNum       uint32  `json:"node_num"`
	RecentMeanSNR float64 `json:"recent_mean_snr"`
	OlderMeanSNR  float64 `json:"older_mean_snr"`
	RecentMeanRSSI float64 `json:"recent_mean_rssi"`
	OlderMeanRSSI  float64 `json:"older_mean_rssi"`
	RecentCount   int     `json:"recent_count"`
	OlderCount    int     `json:"older_count"`
	DeltaSNR      float64 `json:"delta_snr"`   // recent - older, dB  (negative = degrading)
	DeltaRSSI     float64 `json:"delta_rssi"`
	LastSampleAt  int64   `json:"last_sample_at"`
}

// ComputeSignalTrends returns a per-node SNR/RSSI comparison between two time windows.
//
// "recent" window = [now - windowSec, now]
// "older"  window = [now - 2*windowSec, now - windowSec]
//
// Only nodes with at least minSamples in BOTH windows are returned. This guarantees
// we compare apples to apples (same-sized windows), avoiding false signals when a
// node just started transmitting.
//
// Uses a single aggregate query (GROUP BY node_num) so it scales to large DBs.
func (d *DB) ComputeSignalTrends(windowSec int64, minSamples int) ([]SignalTrend, error) {
	if windowSec <= 0 {
		windowSec = 24 * 3600
	}
	if minSamples < 1 {
		minSamples = 5
	}
	now := nowUnix()
	recentFrom := now - windowSec
	olderFrom := now - 2*windowSec
	// Pull two aggregate rows per node in one query, then merge in Go.
	q := `
		SELECT node_num,
		       CASE WHEN time >= ? THEN 1 ELSE 0 END AS is_recent,
		       COUNT(*)          AS n,
		       AVG(snr)          AS mean_snr,
		       AVG(rssi)         AS mean_rssi,
		       MAX(time)         AS max_t
		FROM signal_history
		WHERE time >= ?
		GROUP BY node_num, is_recent
	`
	rows, err := d.db.Query(q, recentFrom, olderFrom)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type bucket struct {
		count int
		msnr  float64
		mrssi float64
		maxT  int64
	}
	type pair struct{ recent, older bucket }
	byNode := make(map[uint32]*pair)
	for rows.Next() {
		var nn uint32
		var isRecent, n int
		var msnr, mrssi float64
		var maxT int64
		if err := rows.Scan(&nn, &isRecent, &n, &msnr, &mrssi, &maxT); err != nil {
			continue
		}
		p, ok := byNode[nn]
		if !ok {
			p = &pair{}
			byNode[nn] = p
		}
		b := bucket{count: n, msnr: msnr, mrssi: mrssi, maxT: maxT}
		if isRecent == 1 {
			p.recent = b
		} else {
			p.older = b
		}
	}

	out := make([]SignalTrend, 0, len(byNode))
	for nn, p := range byNode {
		if p.recent.count < minSamples || p.older.count < minSamples {
			continue
		}
		out = append(out, SignalTrend{
			NodeNum:        nn,
			RecentMeanSNR:  p.recent.msnr,
			OlderMeanSNR:   p.older.msnr,
			RecentMeanRSSI: p.recent.mrssi,
			OlderMeanRSSI:  p.older.mrssi,
			RecentCount:    p.recent.count,
			OlderCount:     p.older.count,
			DeltaSNR:       p.recent.msnr - p.older.msnr,
			DeltaRSSI:      p.recent.mrssi - p.older.mrssi,
			LastSampleAt:   p.recent.maxT,
		})
	}
	return out, nil
}

func nowUnix() int64 { return time.Now().Unix() }

// ---- Node Availability ----

// AvailabilityEvent is one online/offline transition.
type AvailabilityEvent struct {
	Time    int64  `json:"time"`
	NodeNum uint32 `json:"node_num"`
	Event   string `json:"event"` // "online" or "offline"
}

// InsertAvailability records an online/offline transition for a node.
func (d *DB) InsertAvailability(a AvailabilityEvent) {
	_, err := d.db.Exec(
		`INSERT INTO node_availability (time, node_num, event) VALUES (?,?,?)`,
		a.Time, a.NodeNum, a.Event,
	)
	if err != nil {
		log.Printf("[db] insert availability: %v", err)
	}
}

// LoadAvailability returns availability events for a node (last N), oldest first.
func (d *DB) LoadAvailability(nodeNum uint32, limit int) []AvailabilityEvent {
	if limit <= 0 {
		limit = 500
	}
	rows, err := d.db.Query(
		`SELECT time, node_num, event FROM node_availability
		 WHERE node_num = ?
		 ORDER BY id DESC LIMIT ?`, nodeNum, limit)
	if err != nil {
		log.Printf("[db] load availability: %v", err)
		return nil
	}
	defer rows.Close()
	var out []AvailabilityEvent
	for rows.Next() {
		var a AvailabilityEvent
		if err := rows.Scan(&a.Time, &a.NodeNum, &a.Event); err != nil {
			continue
		}
		out = append(out, a)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// LoadAllAvailability returns the latest N events for ALL nodes, oldest first.
func (d *DB) LoadAllAvailability(limit int) []AvailabilityEvent {
	if limit <= 0 {
		limit = 2000
	}
	rows, err := d.db.Query(
		`SELECT time, node_num, event FROM node_availability
		 ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		log.Printf("[db] load all availability: %v", err)
		return nil
	}
	defer rows.Close()
	var out []AvailabilityEvent
	for rows.Next() {
		var a AvailabilityEvent
		if err := rows.Scan(&a.Time, &a.NodeNum, &a.Event); err != nil {
			continue
		}
		out = append(out, a)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// ---- Channel Utilization Snapshots ----

// ChannelSnapshot is one aggregated channel-utilization sample across the mesh.
type ChannelSnapshot struct {
	Time           int64   `json:"time"`
	NodesReporting int     `json:"nodes_reporting"`
	AvgChanUtil    float64 `json:"avg_chan_util"`
	MaxChanUtil    float64 `json:"max_chan_util"`
	AvgAirUtil     float64 `json:"avg_air_util"`
	MaxAirUtil     float64 `json:"max_air_util"`
	TopTalkerNum   uint32  `json:"top_talker_num"`
	TopTalkerUtil  float64 `json:"top_talker_util"`
}

// InsertChannelSnapshot stores one aggregated snapshot.
func (d *DB) InsertChannelSnapshot(s ChannelSnapshot) {
	_, err := d.db.Exec(
		`INSERT INTO channel_snapshots
		 (time, nodes_reporting, avg_chan_util, max_chan_util, avg_air_util, max_air_util, top_talker_num, top_talker_util)
		 VALUES (?,?,?,?,?,?,?,?)`,
		s.Time, s.NodesReporting, s.AvgChanUtil, s.MaxChanUtil,
		s.AvgAirUtil, s.MaxAirUtil, s.TopTalkerNum, s.TopTalkerUtil,
	)
	if err != nil {
		log.Printf("[db] insert channel snapshot: %v", err)
	}
}

// LoadChannelSnapshots returns the latest N snapshots, oldest first.
func (d *DB) LoadChannelSnapshots(limit int) []ChannelSnapshot {
	if limit <= 0 {
		limit = 288
	}
	rows, err := d.db.Query(
		`SELECT time, nodes_reporting, avg_chan_util, max_chan_util, avg_air_util, max_air_util, top_talker_num, top_talker_util
		 FROM channel_snapshots ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		log.Printf("[db] load channel snapshots: %v", err)
		return nil
	}
	defer rows.Close()
	var out []ChannelSnapshot
	for rows.Next() {
		var s ChannelSnapshot
		if err := rows.Scan(&s.Time, &s.NodesReporting, &s.AvgChanUtil, &s.MaxChanUtil,
			&s.AvgAirUtil, &s.MaxAirUtil, &s.TopTalkerNum, &s.TopTalkerUtil); err != nil {
			continue
		}
		out = append(out, s)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
