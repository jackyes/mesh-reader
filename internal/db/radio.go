package db

import (
	"encoding/json"
	"log"

	"mesh-reader/internal/store"
)

// RadioSnapshotRow is one historical sample of the radio-health state, as
// persisted in the radio_snapshots table. Used to plot trends over time.
type RadioSnapshotRow struct {
	Time         int64   `json:"time"` // unix seconds
	RxTotal      int     `json:"rx_total"`
	DupTotal     int     `json:"dup_total"`
	MqttTotal    int     `json:"mqtt_total"`
	RxLast5Min   int     `json:"rx_last_5min"`
	DupLast5Min  int     `json:"dup_last_5min"`
	DupRate5Min  float64 `json:"dup_rate_5min"`
	SendersCount int     `json:"senders_count"`
	TopRelay     string  `json:"top_relay,omitempty"`
}

// SaveRadioSnapshot appends one sample of the current RadioHealth to DB.
// summaryJSON is a small compact representation used for detailed inspection
// (hop histogram, channel hashes, router candidates).
func (d *DB) SaveRadioSnapshot(t int64, rh store.RadioHealth) {
	if !rh.Enabled {
		return
	}
	topRelay := ""
	if len(rh.RawRelays) > 0 {
		topRelay = rh.RawRelays[0].NodeID
	}
	// Compact summary: keep only the lightweight slices / maps, not the
	// full senders list (that would bloat each row).
	summary := map[string]any{
		"hop_used":          rh.HopUsed,
		"channel_hashes":    rh.ChannelHashes,
		"router_candidates": rh.RouterCandidates,
		"no_rebc_reasons":   rh.NoRebcReasons,
	}
	if rh.FreqOffset != nil {
		summary["freq_offset"] = rh.FreqOffset
	}
	sb, _ := json.Marshal(summary)
	_, err := d.db.Exec(
		`INSERT INTO radio_snapshots (time, rx_total, dup_total, mqtt_total,
			rx_last_5min, dup_last_5min, dup_rate_5min, senders_count, top_relay, summary_json)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
		t, rh.RawRxTotal, rh.RawDupTotal, rh.RawMqttTotal,
		rh.RxLast5Min, rh.DupLast5Min, rh.DupRate5Min,
		len(rh.Senders), topRelay, string(sb),
	)
	if err != nil {
		log.Printf("[db] save radio snapshot: %v", err)
	}
}

// LoadRadioSnapshots returns the latest `limit` snapshots, oldest first.
// Used to render trend charts on the dashboard.
func (d *DB) LoadRadioSnapshots(limit int) []RadioSnapshotRow {
	if limit <= 0 {
		limit = 288 // ~24h at 5-min cadence
	}
	rows, err := d.db.Query(
		`SELECT time, rx_total, dup_total, mqtt_total,
			rx_last_5min, dup_last_5min, dup_rate_5min, senders_count, top_relay
		FROM radio_snapshots
		ORDER BY id DESC
		LIMIT ?`, limit,
	)
	if err != nil {
		log.Printf("[db] load radio snapshots: %v", err)
		return nil
	}
	defer rows.Close()

	var out []RadioSnapshotRow
	for rows.Next() {
		var r RadioSnapshotRow
		if err := rows.Scan(&r.Time, &r.RxTotal, &r.DupTotal, &r.MqttTotal,
			&r.RxLast5Min, &r.DupLast5Min, &r.DupRate5Min, &r.SendersCount, &r.TopRelay); err != nil {
			log.Printf("[db] scan radio snapshot: %v", err)
			continue
		}
		out = append(out, r)
	}
	// Reverse (oldest first) for chart plotting.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
