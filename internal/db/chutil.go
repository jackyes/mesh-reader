// Package db — per-node channel utilization history.
//
// Every Telemetry event that carries ChannelUtilization (and optionally
// AirUtilTx) is persisted here on the fly. The map's ChUtil Geo-Monitor
// reads this table to answer "which zone is congested, right now / on
// average / at its worst?"
//
// Retention is the same as the other time-series tables (see Cleanup).
package db

import (
	"log"
	"sort"
	"time"
)

// ChUtilSample is one persisted observation.
type ChUtilSample struct {
	NodeNum  uint32
	Time     int64
	ChanUtil float64
	AirUtil  float64
}

// InsertChUtilSample stores one ChUtil reading. Called from main.go after
// a Telemetry event lands, if channel_utilization_% is non-zero.
func (d *DB) InsertChUtilSample(s ChUtilSample) {
	_, err := d.db.Exec(
		`INSERT INTO chutil_history (node_num, time, chan_util, air_util)
		 VALUES (?, ?, ?, ?)`,
		s.NodeNum, s.Time, s.ChanUtil, s.AirUtil,
	)
	if err != nil {
		log.Printf("[db] insert chutil: %v", err)
	}
}

// ChUtilPoint is one sample returned by the history endpoint (sparkline).
type ChUtilPoint struct {
	Time     int64   `json:"time"`
	ChanUtil float64 `json:"chan_util"`
	AirUtil  float64 `json:"air_util"`
}

// ChUtilHistory returns samples for a node in the last `hours` hours, oldest
// first (so the frontend can feed it straight to a sparkline). A reasonable
// upper bound on samples keeps the response cheap even with noisy nodes.
func (d *DB) ChUtilHistory(nodeNum uint32, hours int) []ChUtilPoint {
	if hours <= 0 {
		hours = 24
	}
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
	rows, err := d.db.Query(
		`SELECT time, chan_util, air_util
		 FROM chutil_history
		 WHERE node_num = ? AND time >= ?
		 ORDER BY time ASC
		 LIMIT 2000`,
		nodeNum, cutoff,
	)
	if err != nil {
		log.Printf("[db] chutil history: %v", err)
		return nil
	}
	defer rows.Close()
	var out []ChUtilPoint
	for rows.Next() {
		var p ChUtilPoint
		if err := rows.Scan(&p.Time, &p.ChanUtil, &p.AirUtil); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// ChUtilNodeStat is the per-node aggregate used by the Geo-Monitor layer.
// The frontend's metric selector picks which field colors the map.
type ChUtilNodeStat struct {
	NodeNum        uint32  `json:"node_num"`
	Current        float64 `json:"current"`          // most recent value in window
	CurrentAgeMin  int64   `json:"current_age_min"`  // how stale the last sample is
	Avg            float64 `json:"avg"`
	P50            float64 `json:"p50"`
	P95            float64 `json:"p95"`
	Max            float64 `json:"max"`
	PeakTime       int64   `json:"peak_time"` // unix, when Max was observed
	Samples        int     `json:"samples"`
	AirAvg         float64 `json:"air_avg"`
	AirMax         float64 `json:"air_max"`
	LastSampleTime int64   `json:"last_sample_time"`
}

// ChUtilZones returns per-node stats over the last `hours` hours. One pass
// over the table, grouping in-memory because SQLite has no percentile_cont.
// Expected cardinality per window is small (retention × #reporting nodes),
// typical workloads are well under 100k rows → trivial.
func (d *DB) ChUtilZones(hours int) []ChUtilNodeStat {
	if hours <= 0 {
		hours = 24
	}
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
	rows, err := d.db.Query(
		`SELECT node_num, time, chan_util, air_util
		 FROM chutil_history
		 WHERE time >= ?
		 ORDER BY node_num, time ASC`,
		cutoff,
	)
	if err != nil {
		log.Printf("[db] chutil zones: %v", err)
		return nil
	}
	defer rows.Close()

	type bucket struct {
		values   []float64
		airs     []float64
		lastT    int64
		lastVal  float64
		peakT    int64
		peakVal  float64
	}
	buckets := make(map[uint32]*bucket)
	for rows.Next() {
		var num uint32
		var t int64
		var cu, au float64
		if err := rows.Scan(&num, &t, &cu, &au); err != nil {
			continue
		}
		b, ok := buckets[num]
		if !ok {
			b = &bucket{}
			buckets[num] = b
		}
		b.values = append(b.values, cu)
		b.airs = append(b.airs, au)
		// ORDER BY time ASC → the last row we see is the most recent.
		b.lastT = t
		b.lastVal = cu
		if cu > b.peakVal {
			b.peakVal = cu
			b.peakT = t
		}
	}

	now := time.Now().Unix()
	out := make([]ChUtilNodeStat, 0, len(buckets))
	for num, b := range buckets {
		if len(b.values) == 0 {
			continue
		}
		sorted := make([]float64, len(b.values))
		copy(sorted, b.values)
		sort.Float64s(sorted)
		var sum, airSum, airMax float64
		for i, v := range b.values {
			sum += v
			airSum += b.airs[i]
			if b.airs[i] > airMax {
				airMax = b.airs[i]
			}
		}
		stat := ChUtilNodeStat{
			NodeNum:        num,
			Current:        b.lastVal,
			CurrentAgeMin:  (now - b.lastT) / 60,
			Avg:            sum / float64(len(sorted)),
			P50:            percentile(sorted, 0.50),
			P95:            percentile(sorted, 0.95),
			Max:            b.peakVal,
			PeakTime:       b.peakT,
			Samples:        len(sorted),
			AirAvg:         airSum / float64(len(sorted)),
			AirMax:         airMax,
			LastSampleTime: b.lastT,
		}
		out = append(out, stat)
	}
	return out
}

// percentile returns the p-quantile (0..1) of a sorted slice using the
// nearest-rank method. Small input sizes mean the choice barely matters.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[n-1]
	}
	// nearest-rank
	rank := int(float64(n-1)*p + 0.5)
	if rank < 0 {
		rank = 0
	}
	if rank >= n {
		rank = n - 1
	}
	return sorted[rank]
}
