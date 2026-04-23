package db

import (
	"database/sql"
	"log"
	"time"
)

// HeatmapCell is one (weekday, hour) bucket with rich aggregates, so the
// dashboard can render volume, unique-nodes, or signal-quality views from the
// same payload and also fill a rich tooltip on hover.
type HeatmapCell struct {
	Weekday      int     `json:"weekday"` // 0=Sun, 6=Sat (sqlite strftime('%w'))
	Hour         int     `json:"hour"`    // 0..23 in operator local time
	Count        int     `json:"count"`
	UniqueNodes  int     `json:"unique_nodes"`
	TopType      string  `json:"top_type"`
	TopTypeCount int     `json:"top_type_count"`
	MsgCount     int     `json:"msg_count"`
	PosCount     int     `json:"pos_count"`
	TelCount     int     `json:"tel_count"`
	AvgSNR       float64 `json:"avg_snr"`  // 0 if no signal samples
	AvgRSSI      float64 `json:"avg_rssi"` // 0 if no signal samples
}

// TemporalHeatmap returns event counts grouped by (weekday, hour) for the
// last `days` days, computed in the operator's local timezone so the heatmap
// matches the human perception of "Monday morning". Excludes LOG_RECORD rows
// (firmware debug log noise that should not skew the activity heatmap).
//
// Two passes:
//  1. Main aggregates (count, unique nodes, per-type counters, average SNR/RSSI)
//  2. Top-type per cell (scan grouped (dow, hr, type) rows, keep the max)
//
// Both queries hit idx_events_time; with typical ring sizes the whole thing
// completes in low-ms range.
func (d *DB) TemporalHeatmap(days int) ([]HeatmapCell, error) {
	if days <= 0 {
		days = 30
	}
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format(time.RFC3339)

	// Pass 1 — main aggregates.
	mainRows, err := d.db.Query(`
		SELECT CAST(strftime('%w', time, 'localtime') AS INTEGER) AS dow,
		       CAST(strftime('%H', time, 'localtime') AS INTEGER) AS hr,
		       COUNT(*) AS c,
		       COUNT(DISTINCT from_node) AS un,
		       SUM(CASE WHEN type = 'TEXT_MESSAGE' THEN 1 ELSE 0 END) AS msg,
		       SUM(CASE WHEN type = 'POSITION'     THEN 1 ELSE 0 END) AS pos,
		       SUM(CASE WHEN type = 'TELEMETRY'    THEN 1 ELSE 0 END) AS tel,
		       AVG(CASE WHEN snr  != 0 THEN snr  END) AS asnr,
		       AVG(CASE WHEN rssi != 0 THEN rssi END) AS arssi
		FROM events
		WHERE time >= ? AND type != 'LOG_RECORD'
		GROUP BY dow, hr
		ORDER BY dow, hr
	`, cutoff)
	if err != nil {
		log.Printf("[db] heatmap main: %v", err)
		return nil, err
	}
	defer mainRows.Close()

	// Keyed map so we can fill top_type in pass 2.
	byKey := make(map[int]*HeatmapCell, 24*7)
	key := func(dow, hr int) int { return dow*24 + hr }

	out := make([]HeatmapCell, 0, 24*7)
	for mainRows.Next() {
		var c HeatmapCell
		var snr, rssi sql.NullFloat64
		if err := mainRows.Scan(&c.Weekday, &c.Hour, &c.Count, &c.UniqueNodes,
			&c.MsgCount, &c.PosCount, &c.TelCount, &snr, &rssi); err != nil {
			continue
		}
		if snr.Valid {
			c.AvgSNR = snr.Float64
		}
		if rssi.Valid {
			c.AvgRSSI = rssi.Float64
		}
		out = append(out, c)
	}
	// Build index *after* slice settles (pointers into a growing slice are
	// unsafe; pre-allocation above avoids growth but be explicit).
	for i := range out {
		byKey[key(out[i].Weekday, out[i].Hour)] = &out[i]
	}

	// Pass 2 — dominant type per cell. Sorted so the first row for each
	// (dow, hr) is the winner; later rows for the same key are ignored.
	typeRows, err := d.db.Query(`
		SELECT CAST(strftime('%w', time, 'localtime') AS INTEGER) AS dow,
		       CAST(strftime('%H', time, 'localtime') AS INTEGER) AS hr,
		       type, COUNT(*) AS tc
		FROM events
		WHERE time >= ? AND type != 'LOG_RECORD'
		GROUP BY dow, hr, type
		ORDER BY dow, hr, tc DESC
	`, cutoff)
	if err != nil {
		log.Printf("[db] heatmap top-type: %v", err)
		return out, nil // degrade gracefully: return pass-1 data
	}
	defer typeRows.Close()
	for typeRows.Next() {
		var dow, hr, tc int
		var typ string
		if err := typeRows.Scan(&dow, &hr, &typ, &tc); err != nil {
			continue
		}
		cell, ok := byKey[key(dow, hr)]
		if !ok {
			continue
		}
		if cell.TopType == "" { // first = largest thanks to ORDER BY tc DESC
			cell.TopType = typ
			cell.TopTypeCount = tc
		}
	}
	return out, nil
}

// HeatmapCellDetailNode is one active node in a (weekday, hour) slot.
type HeatmapCellDetailNode struct {
	NodeNum   uint32 `json:"node_num"`
	NodeLabel string `json:"node_label"` // "!xxxx" id fallback
	Count     int    `json:"count"`
}

// HeatmapCellDetailType is one event-type bucket in the slot.
type HeatmapCellDetailType struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// HeatmapCellDetailSample is a sampled event row from the slot.
type HeatmapCellDetailSample struct {
	Time     string `json:"time"`
	Type     string `json:"type"`
	FromNode uint32 `json:"from_node"`
	RSSI     int    `json:"rssi"`
	SNR      float64 `json:"snr"`
}

// HeatmapCellDetail is the drill-down payload for one heatmap cell.
type HeatmapCellDetail struct {
	Weekday     int                        `json:"weekday"`
	Hour        int                        `json:"hour"`
	Days        int                        `json:"days"`
	Total       int                        `json:"total"`
	UniqueNodes int                        `json:"unique_nodes"`
	AvgSNR      float64                    `json:"avg_snr"`
	AvgRSSI     float64                    `json:"avg_rssi"`
	MinSNR      float64                    `json:"min_snr"`
	MaxSNR      float64                    `json:"max_snr"`
	TopNodes    []HeatmapCellDetailNode    `json:"top_nodes"`
	Types       []HeatmapCellDetailType    `json:"types"`
	Samples     []HeatmapCellDetailSample  `json:"samples"`
}

// HeatmapCellDetailFor returns the drill-down breakdown for one (weekday, hour)
// slot over the last `days` days. Used when the user clicks a cell.
func (d *DB) HeatmapCellDetailFor(weekday, hour, days int) (*HeatmapCellDetail, error) {
	if days <= 0 {
		days = 30
	}
	if weekday < 0 || weekday > 6 || hour < 0 || hour > 23 {
		return &HeatmapCellDetail{Weekday: weekday, Hour: hour, Days: days}, nil
	}
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format(time.RFC3339)
	det := &HeatmapCellDetail{Weekday: weekday, Hour: hour, Days: days}

	// Summary stats for the cell.
	var snrAvg, snrMin, snrMax, rssiAvg sql.NullFloat64
	err := d.db.QueryRow(`
		SELECT COUNT(*), COUNT(DISTINCT from_node),
		       AVG(CASE WHEN snr  != 0 THEN snr  END),
		       MIN(CASE WHEN snr  != 0 THEN snr  END),
		       MAX(CASE WHEN snr  != 0 THEN snr  END),
		       AVG(CASE WHEN rssi != 0 THEN rssi END)
		FROM events
		WHERE time >= ? AND type != 'LOG_RECORD'
		  AND CAST(strftime('%w', time, 'localtime') AS INTEGER) = ?
		  AND CAST(strftime('%H', time, 'localtime') AS INTEGER) = ?
	`, cutoff, weekday, hour).Scan(&det.Total, &det.UniqueNodes,
		&snrAvg, &snrMin, &snrMax, &rssiAvg)
	if err != nil {
		return nil, err
	}
	if snrAvg.Valid {
		det.AvgSNR = snrAvg.Float64
	}
	if snrMin.Valid {
		det.MinSNR = snrMin.Float64
	}
	if snrMax.Valid {
		det.MaxSNR = snrMax.Float64
	}
	if rssiAvg.Valid {
		det.AvgRSSI = rssiAvg.Float64
	}
	if det.Total == 0 {
		return det, nil
	}

	// Top nodes.
	nrows, err := d.db.Query(`
		SELECT from_node, COUNT(*) AS c
		FROM events
		WHERE time >= ? AND type != 'LOG_RECORD'
		  AND from_node != 0
		  AND CAST(strftime('%w', time, 'localtime') AS INTEGER) = ?
		  AND CAST(strftime('%H', time, 'localtime') AS INTEGER) = ?
		GROUP BY from_node
		ORDER BY c DESC
		LIMIT 10
	`, cutoff, weekday, hour)
	if err == nil {
		for nrows.Next() {
			var n HeatmapCellDetailNode
			if err := nrows.Scan(&n.NodeNum, &n.Count); err == nil {
				det.TopNodes = append(det.TopNodes, n)
			}
		}
		nrows.Close()
	}

	// Type breakdown.
	trows, err := d.db.Query(`
		SELECT type, COUNT(*) AS c
		FROM events
		WHERE time >= ? AND type != 'LOG_RECORD'
		  AND CAST(strftime('%w', time, 'localtime') AS INTEGER) = ?
		  AND CAST(strftime('%H', time, 'localtime') AS INTEGER) = ?
		GROUP BY type
		ORDER BY c DESC
	`, cutoff, weekday, hour)
	if err == nil {
		for trows.Next() {
			var t HeatmapCellDetailType
			if err := trows.Scan(&t.Type, &t.Count); err == nil {
				det.Types = append(det.Types, t)
			}
		}
		trows.Close()
	}

	// Sample events (most recent 20).
	srows, err := d.db.Query(`
		SELECT time, type, from_node, rssi, snr
		FROM events
		WHERE time >= ? AND type != 'LOG_RECORD'
		  AND CAST(strftime('%w', time, 'localtime') AS INTEGER) = ?
		  AND CAST(strftime('%H', time, 'localtime') AS INTEGER) = ?
		ORDER BY time DESC
		LIMIT 20
	`, cutoff, weekday, hour)
	if err == nil {
		for srows.Next() {
			var s HeatmapCellDetailSample
			if err := srows.Scan(&s.Time, &s.Type, &s.FromNode, &s.RSSI, &s.SNR); err == nil {
				det.Samples = append(det.Samples, s)
			}
		}
		srows.Close()
	}
	return det, nil
}
