// Package db provides SQLite persistence for Meshtastic events.
package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	_ "modernc.org/sqlite"

	"mesh-reader/internal/decoder"
	"mesh-reader/internal/store"
)

// DB wraps the SQLite database.
type DB struct {
	db *sql.DB
}

// Open creates or opens the SQLite database at path.
//
// Connection tuning:
//   - WAL journal: concurrent readers + one writer, no reader blocking on write
//   - busy_timeout=30s: SQLite internally retries on BUSY for up to 30s before
//     surfacing the error. Plenty for our workload (all writes complete in <100ms).
//   - synchronous=NORMAL: safe with WAL, ~2-3x faster than FULL on spinning disks
//   - cache_size=-20000: 20 MB page cache (negative = KB)
//   - MaxOpenConns=1: SERIALIZES writes across goroutines. This eliminates
//     SQLITE_BUSY caused by multiple goroutines (event loop + snapshot ticker +
//     availability scanner + retention cleanup) racing to write. With
//     modernc.org/sqlite (pure-Go) this is the idiomatic way to avoid locking
//     contention; the Go sql package will queue operations for us.
//   - MaxIdleConns=1: keeps the connection warm
func Open(path string) (*DB, error) {
	dsn := path + "?_journal_mode=WAL&_busy_timeout=30000&_synchronous=NORMAL&_cache_size=-20000"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	sqldb.SetMaxOpenConns(1)
	sqldb.SetMaxIdleConns(1)
	sqldb.SetConnMaxLifetime(0)
	d := &DB{db: sqldb}
	if err := d.migrate(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

func (d *DB) migrate() error {
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS events (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			time        TEXT    NOT NULL,
			type        TEXT    NOT NULL,
			from_node   INTEGER NOT NULL DEFAULT 0,
			to_node     INTEGER NOT NULL DEFAULT 0,
			rssi        INTEGER NOT NULL DEFAULT 0,
			snr         REAL    NOT NULL DEFAULT 0,
			hop_limit   INTEGER NOT NULL DEFAULT 0,
			details     TEXT    NOT NULL DEFAULT '{}'
		);
		CREATE INDEX IF NOT EXISTS idx_events_type ON events(type);
		CREATE INDEX IF NOT EXISTS idx_events_time ON events(time);
		CREATE INDEX IF NOT EXISTS idx_events_from ON events(from_node);

		CREATE TABLE IF NOT EXISTS nodes (
			node_num    INTEGER PRIMARY KEY,
			id          TEXT    NOT NULL DEFAULT '',
			long_name   TEXT    NOT NULL DEFAULT '',
			short_name  TEXT    NOT NULL DEFAULT '',
			hw_model    TEXT    NOT NULL DEFAULT '',
			role        TEXT    NOT NULL DEFAULT '',
			last_heard  INTEGER NOT NULL DEFAULT 0,
			lat         REAL    NOT NULL DEFAULT 0,
			lon         REAL    NOT NULL DEFAULT 0,
			has_pos     INTEGER NOT NULL DEFAULT 0,
			altitude    INTEGER NOT NULL DEFAULT 0,
			battery     INTEGER NOT NULL DEFAULT 0,
			voltage     REAL    NOT NULL DEFAULT 0,
			chan_util   REAL    NOT NULL DEFAULT 0,
			air_util    REAL    NOT NULL DEFAULT 0,
			temperature REAL    NOT NULL DEFAULT 0,
			humidity    REAL    NOT NULL DEFAULT 0,
			pressure    REAL    NOT NULL DEFAULT 0,
			rssi        INTEGER NOT NULL DEFAULT 0,
			snr         REAL    NOT NULL DEFAULT 0,
			hop_limit   INTEGER NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS traceroutes (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			time        INTEGER NOT NULL,
			from_node   INTEGER NOT NULL,
			to_node     INTEGER NOT NULL,
			route       TEXT    NOT NULL DEFAULT '[]',
			route_back  TEXT    NOT NULL DEFAULT '[]',
			snr_towards TEXT    NOT NULL DEFAULT '[]',
			snr_back    TEXT    NOT NULL DEFAULT '[]'
		);
		CREATE INDEX IF NOT EXISTS idx_tr_time ON traceroutes(time);

		CREATE TABLE IF NOT EXISTS radio_snapshots (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			time            INTEGER NOT NULL,
			rx_total        INTEGER NOT NULL,
			dup_total       INTEGER NOT NULL,
			mqtt_total      INTEGER NOT NULL,
			rx_last_5min    INTEGER NOT NULL,
			dup_last_5min   INTEGER NOT NULL,
			dup_rate_5min   REAL    NOT NULL,
			senders_count   INTEGER NOT NULL,
			top_relay       TEXT    NOT NULL DEFAULT '',
			summary_json    TEXT    NOT NULL DEFAULT '{}'
		);
		CREATE INDEX IF NOT EXISTS idx_radio_time ON radio_snapshots(time);

		CREATE TABLE IF NOT EXISTS signal_history (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			time        INTEGER NOT NULL,
			node_num    INTEGER NOT NULL,
			rssi        INTEGER NOT NULL,
			snr         REAL    NOT NULL,
			hop_limit   INTEGER NOT NULL DEFAULT 0,
			hop_start   INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_sig_node ON signal_history(node_num);
		CREATE INDEX IF NOT EXISTS idx_sig_time ON signal_history(time);

		CREATE TABLE IF NOT EXISTS node_availability (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			time        INTEGER NOT NULL,
			node_num    INTEGER NOT NULL,
			event       TEXT    NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_avail_node ON node_availability(node_num);
		CREATE INDEX IF NOT EXISTS idx_avail_time ON node_availability(time);

		CREATE TABLE IF NOT EXISTS channel_snapshots (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			time            INTEGER NOT NULL,
			nodes_reporting INTEGER NOT NULL DEFAULT 0,
			avg_chan_util   REAL    NOT NULL DEFAULT 0,
			max_chan_util   REAL    NOT NULL DEFAULT 0,
			avg_air_util    REAL    NOT NULL DEFAULT 0,
			max_air_util    REAL    NOT NULL DEFAULT 0,
			top_talker_num  INTEGER NOT NULL DEFAULT 0,
			top_talker_util REAL    NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_chan_time ON channel_snapshots(time);

		-- Per-node channel utilization history. One row per telemetry sample
		-- that carries ChannelUtilization. Used by the ChUtil Geo-Monitor map
		-- layer to show zone-by-zone congestion (current / avg / p95 / peak).
		CREATE TABLE IF NOT EXISTS chutil_history (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			node_num  INTEGER NOT NULL,
			time      INTEGER NOT NULL,
			chan_util REAL    NOT NULL,
			air_util  REAL    NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_chutil_node_time ON chutil_history(node_num, time DESC);
		CREATE INDEX IF NOT EXISTS idx_chutil_time      ON chutil_history(time);

		-- Composite indexes for common query patterns.
		-- These speed up per-node history views (signal sparkline, telemetry charts)
		-- and "last N events from a node" queries by orders of magnitude on large DBs.
		CREATE INDEX IF NOT EXISTS idx_events_from_time ON events(from_node, time DESC);
		CREATE INDEX IF NOT EXISTS idx_sig_node_time   ON signal_history(node_num, time DESC);
		CREATE INDEX IF NOT EXISTS idx_avail_node_time ON node_availability(node_num, time DESC);
	`)
	if err != nil {
		return err
	}

	// Safe migration for existing databases: add SNR columns if missing.
	d.db.Exec(`ALTER TABLE traceroutes ADD COLUMN snr_towards TEXT NOT NULL DEFAULT '[]'`)
	d.db.Exec(`ALTER TABLE traceroutes ADD COLUMN snr_back TEXT NOT NULL DEFAULT '[]'`)

	// Add hop_start and packet_id columns to events.
	d.db.Exec(`ALTER TABLE events ADD COLUMN hop_start INTEGER NOT NULL DEFAULT 0`)
	d.db.Exec(`ALTER TABLE events ADD COLUMN packet_id INTEGER NOT NULL DEFAULT 0`)

	// Add role column to nodes (Meshtastic device role: CLIENT, ROUTER, …).
	d.db.Exec(`ALTER TABLE nodes ADD COLUMN role TEXT NOT NULL DEFAULT ''`)

	return nil
}

// CleanupOld deletes rows older than retentionDays from high-volume tables.
// Preserves: nodes (low volume, high value), traceroutes (rare, valuable for topology).
// Prunes: events, signal_history, radio_snapshots, channel_snapshots, node_availability.
// Returns the total number of rows deleted.
// A retentionDays <= 0 disables cleanup (returns 0 immediately).
func (d *DB) CleanupOld(retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cutoffUnix := time.Now().AddDate(0, 0, -retentionDays).Unix()
	cutoffRFC := time.Unix(cutoffUnix, 0).UTC().Format(time.RFC3339)

	var total int64
	// events uses RFC3339 strings for time
	if res, err := d.db.Exec(`DELETE FROM events WHERE time < ?`, cutoffRFC); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			total += n
		}
	} else {
		log.Printf("[db] cleanup events: %v", err)
	}
	// Integer-unix tables
	intTables := []string{"signal_history", "radio_snapshots", "channel_snapshots", "node_availability", "chutil_history"}
	for _, t := range intTables {
		q := fmt.Sprintf(`DELETE FROM %s WHERE time < ?`, t)
		if res, err := d.db.Exec(q, cutoffUnix); err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				total += n
			}
		} else {
			log.Printf("[db] cleanup %s: %v", t, err)
		}
	}
	// Reclaim space after a big delete
	if total > 10000 {
		if _, err := d.db.Exec(`PRAGMA incremental_vacuum`); err != nil {
			// incremental_vacuum requires auto_vacuum mode — ignore otherwise
			_ = err
		}
	}
	return total, nil
}

// InsertEvent saves an event to the database.
func (d *DB) InsertEvent(event *decoder.Event) {
	detailsJSON, _ := json.Marshal(event.Details)
	_, err := d.db.Exec(
		`INSERT INTO events (time, type, from_node, to_node, rssi, snr, hop_limit, hop_start, packet_id, details)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.Time.Format(time.RFC3339),
		string(event.Type),
		event.FromNode,
		event.ToNode,
		event.RSSI,
		event.SNR,
		event.HopLimit,
		event.HopStart,
		event.PacketID,
		string(detailsJSON),
	)
	if err != nil {
		log.Printf("[db] insert event: %v", err)
	}
}

// SaveNode upserts the current node state.
func (d *DB) SaveNode(n *store.NodeState) {
	_, err := d.db.Exec(
		`INSERT INTO nodes (node_num, id, long_name, short_name, hw_model, role, last_heard,
		                     lat, lon, has_pos, altitude, battery, voltage, chan_util,
		                     air_util, temperature, humidity, pressure, rssi, snr, hop_limit)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(node_num) DO UPDATE SET
		   id=excluded.id, long_name=excluded.long_name, short_name=excluded.short_name,
		   hw_model=excluded.hw_model, role=excluded.role, last_heard=excluded.last_heard,
		   lat=excluded.lat, lon=excluded.lon, has_pos=excluded.has_pos,
		   altitude=excluded.altitude, battery=excluded.battery, voltage=excluded.voltage,
		   chan_util=excluded.chan_util, air_util=excluded.air_util,
		   temperature=excluded.temperature, humidity=excluded.humidity,
		   pressure=excluded.pressure, rssi=excluded.rssi, snr=excluded.snr,
		   hop_limit=excluded.hop_limit`,
		n.NodeNum, n.ID, n.LongName, n.ShortName, n.HWModel, n.Role, n.LastHeard,
		n.Lat, n.Lon, n.HasPos, n.Altitude, n.BatteryLevel, n.Voltage,
		n.ChannelUtilization, n.AirUtilTx, n.Temperature, n.Humidity,
		n.BarometricPressure, n.RSSI, n.SNR, n.HopLimit,
	)
	if err != nil {
		log.Printf("[db] save node: %v", err)
	}
}

// InsertTraceroute saves a traceroute record.
func (d *DB) InsertTraceroute(tr *store.TracerouteRecord) {
	routeJSON, _ := json.Marshal(tr.Route)
	routeBackJSON, _ := json.Marshal(tr.RouteBack)
	snrTowardsJSON, _ := json.Marshal(tr.SnrTowards)
	snrBackJSON, _ := json.Marshal(tr.SnrBack)
	_, err := d.db.Exec(
		`INSERT INTO traceroutes (time, from_node, to_node, route, route_back, snr_towards, snr_back) VALUES (?,?,?,?,?,?,?)`,
		tr.Time, tr.From, tr.To, string(routeJSON), string(routeBackJSON),
		string(snrTowardsJSON), string(snrBackJSON),
	)
	if err != nil {
		log.Printf("[db] insert traceroute: %v", err)
	}
}

// LoadNodes loads all saved nodes into the store.
func (d *DB) LoadNodes() []store.NodeState {
	rows, err := d.db.Query(`SELECT node_num, id, long_name, short_name, hw_model, role,
		last_heard, lat, lon, has_pos, altitude, battery, voltage, chan_util,
		air_util, temperature, humidity, pressure, rssi, snr, hop_limit FROM nodes`)
	if err != nil {
		log.Printf("[db] load nodes: %v", err)
		return nil
	}
	defer rows.Close()

	var out []store.NodeState
	for rows.Next() {
		var n store.NodeState
		var hasPos int
		if err := rows.Scan(&n.NodeNum, &n.ID, &n.LongName, &n.ShortName, &n.HWModel, &n.Role,
			&n.LastHeard, &n.Lat, &n.Lon, &hasPos, &n.Altitude, &n.BatteryLevel,
			&n.Voltage, &n.ChannelUtilization, &n.AirUtilTx, &n.Temperature,
			&n.Humidity, &n.BarometricPressure, &n.RSSI, &n.SNR, &n.HopLimit); err != nil {
			log.Printf("[db] scan node: %v", err)
			continue
		}
		n.HasPos = hasPos != 0
		out = append(out, n)
	}
	return out
}

// LoadTraceroutes loads all traceroute records.
func (d *DB) LoadTraceroutes() []store.TracerouteRecord {
	rows, err := d.db.Query(`SELECT time, from_node, to_node, route, route_back, snr_towards, snr_back FROM traceroutes ORDER BY time`)
	if err != nil {
		log.Printf("[db] load traceroutes: %v", err)
		return nil
	}
	defer rows.Close()

	var out []store.TracerouteRecord
	for rows.Next() {
		var tr store.TracerouteRecord
		var routeJSON, routeBackJSON, snrTowardsJSON, snrBackJSON string
		if err := rows.Scan(&tr.Time, &tr.From, &tr.To, &routeJSON, &routeBackJSON, &snrTowardsJSON, &snrBackJSON); err != nil {
			log.Printf("[db] scan traceroute: %v", err)
			continue
		}
		json.Unmarshal([]byte(routeJSON), &tr.Route)
		json.Unmarshal([]byte(routeBackJSON), &tr.RouteBack)
		json.Unmarshal([]byte(snrTowardsJSON), &tr.SnrTowards)
		json.Unmarshal([]byte(snrBackJSON), &tr.SnrBack)
		out = append(out, tr)
	}
	return out
}

// LoadRecentEvents loads the last N events for replaying into the store.
func (d *DB) LoadRecentEvents(n int) []*decoder.Event {
	// Exclude LOG_RECORD (firmware debug log) rows from old DBs created
	// before LogRecord events were filtered out of the normal pipeline —
	// otherwise they inflate the event count on the dashboard.
	rows, err := d.db.Query(
		`SELECT time, type, from_node, to_node, rssi, snr, hop_limit, COALESCE(hop_start,0), COALESCE(packet_id,0), details
		 FROM events WHERE type != 'LOG_RECORD' ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		log.Printf("[db] load events: %v", err)
		return nil
	}
	defer rows.Close()

	var out []*decoder.Event
	for rows.Next() {
		var ev decoder.Event
		var timeStr, evType, detailsJSON string
		if err := rows.Scan(&timeStr, &evType, &ev.FromNode, &ev.ToNode,
			&ev.RSSI, &ev.SNR, &ev.HopLimit, &ev.HopStart, &ev.PacketID, &detailsJSON); err != nil {
			log.Printf("[db] scan event: %v", err)
			continue
		}
		ev.Time, _ = time.Parse(time.RFC3339, timeStr)
		ev.Type = decoder.EventType(evType)
		json.Unmarshal([]byte(detailsJSON), &ev.Details)
		out = append(out, &ev)
	}
	// Reverse to chronological order (oldest first)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// EventCount returns the total number of events in the database.
// LOG_RECORD rows (firmware debug log) are excluded from the count — they
// are not part of the mesh event stream and should not inflate dashboard
// totals, even if an older DB contains them.
func (d *DB) EventCount() int {
	var count int
	d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type != 'LOG_RECORD'`).Scan(&count)
	return count
}

// MessageCount returns the total number of text messages.
func (d *DB) MessageCount() int {
	var count int
	d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'TEXT_MESSAGE'`).Scan(&count)
	return count
}
