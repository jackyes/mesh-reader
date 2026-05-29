// Package db provides SQLite persistence for Meshtastic events.
package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"mesh-reader/internal/decoder"
	"mesh-reader/internal/store"
)

// schemaVersion is the current database schema version. It is stored in
// SQLite's PRAGMA user_version so that future runs skip ALTER TABLE
// statements that have already been applied.
const schemaVersion = 6

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
//   - auto_vacuum=2: INCREMENTAL — required for PRAGMA incremental_vacuum to work
//   - MaxOpenConns=1: SERIALIZES writes across goroutines. This eliminates
//     SQLITE_BUSY caused by multiple goroutines (event loop + snapshot ticker +
//     availability scanner + retention cleanup) racing to write. With
//     modernc.org/sqlite (pure-Go) this is the idiomatic way to avoid locking
//     contention; the Go sql package will queue operations for us.
//   - MaxIdleConns=1: keeps the connection warm
func Open(path string) (*DB, error) {
	dsn := path + "?_journal_mode=WAL&_busy_timeout=30000&_synchronous=NORMAL&_cache_size=-20000&_auto_vacuum=2"
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
	// Part 1 — unconditional schema creation. Every statement uses IF NOT
	// EXISTS so it is safe to run on every open.
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

		-- Misbehaving auto-notify audit log: every DM the dashboard sent (or
		-- chose to skip) lives here so the user can review activity AND so the
		-- per-node cooldown survives restarts.
		CREATE TABLE IF NOT EXISTS misbehave_notifications (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			time      INTEGER NOT NULL,
			node_num  INTEGER NOT NULL,
			reasons   TEXT    NOT NULL DEFAULT '',
			text      TEXT    NOT NULL DEFAULT '',
			status    TEXT    NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_misbn_time ON misbehave_notifications(time);
		CREATE INDEX IF NOT EXISTS idx_misbn_node_time ON misbehave_notifications(node_num, time DESC);

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

	// Part 2 — version-gated ALTER TABLE migrations.
	//
	// These are only needed for databases created before the column was
	// added to the CREATE TABLE statement. Fresh databases already have
	// every column via Part 1 so "duplicate column" errors are expected
	// and silently ignored.
	var version int
	d.db.QueryRow(`PRAGMA user_version`).Scan(&version)

	if version < 2 {
		d.execMigrate(`ALTER TABLE traceroutes ADD COLUMN snr_towards TEXT NOT NULL DEFAULT '[]'`)
		d.execMigrate(`ALTER TABLE traceroutes ADD COLUMN snr_back TEXT NOT NULL DEFAULT '[]'`)
	}
	if version < 3 {
		d.execMigrate(`ALTER TABLE events ADD COLUMN hop_start INTEGER NOT NULL DEFAULT 0`)
		d.execMigrate(`ALTER TABLE events ADD COLUMN packet_id INTEGER NOT NULL DEFAULT 0`)
	}
	if version < 4 {
		d.execMigrate(`ALTER TABLE events ADD COLUMN channel INTEGER NOT NULL DEFAULT 0`)
		d.execMigrate(`ALTER TABLE events ADD COLUMN via_mqtt INTEGER NOT NULL DEFAULT 0`)
		d.execMigrate(`ALTER TABLE events ADD COLUMN relay_node INTEGER NOT NULL DEFAULT 0`)
		d.execMigrate(`ALTER TABLE events ADD COLUMN class TEXT NOT NULL DEFAULT ''`)
		d.execMigrate(`CREATE INDEX IF NOT EXISTS idx_events_class ON events(class)`)
		d.execMigrate(`CREATE INDEX IF NOT EXISTS idx_events_to ON events(to_node)`)
		d.execMigrate(`CREATE INDEX IF NOT EXISTS idx_events_channel ON events(channel)`)
	}
	if version < 5 {
		d.execMigrate(`ALTER TABLE nodes ADD COLUMN role TEXT NOT NULL DEFAULT ''`)
	}
	if version < 6 {
		d.execMigrate(`ALTER TABLE nodes ADD COLUMN neighbors_json TEXT NOT NULL DEFAULT '[]'`)
		d.execMigrate(`ALTER TABLE nodes ADD COLUMN neighbors_at INTEGER NOT NULL DEFAULT 0`)
		d.execMigrate(`ALTER TABLE nodes ADD COLUMN neighbor_broadcast_secs INTEGER NOT NULL DEFAULT 0`)
	}

	// Part 3 — persist the current schema version so future opens skip
	// the ALTER TABLE statements above.
	_, err = d.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
	return err
}

// execMigrate runs a DDL migration statement. "Duplicate column" errors are
// silently ignored because they occur when the column already exists (fresh
// database where CREATE TABLE already includes it, or a previously applied
// migration). Real errors are logged but do not abort the migration so that
// one broken step does not prevent later migrations from running.
func (d *DB) execMigrate(q string) {
	if _, err := d.db.Exec(q); err != nil {
		if strings.Contains(err.Error(), "duplicate column") {
			return
		}
		log.Printf("[db] migration: %v", err)
	}
}

// CleanupOld deletes rows older than retentionDays from high-volume tables.
// Preserves: nodes (low volume, high value), traceroutes (rare, valuable for topology).
// Prunes: events, signal_history, radio_snapshots, channel_snapshots, node_availability.
// Returns the total number of rows deleted.
// A retentionDays <= 0 disables cleanup (returns 0 immediately).
// All deletions are wrapped in a single transaction for atomicity.
func (d *DB) CleanupOld(retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cutoffUnix := time.Now().AddDate(0, 0, -retentionDays).Unix()
	cutoffRFC := time.Unix(cutoffUnix, 0).UTC().Format(time.RFC3339)

	tx, err := d.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("cleanup begin tx: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	var total int64
	// events uses RFC3339 strings for time
	if res, err := tx.Exec(`DELETE FROM events WHERE time < ?`, cutoffRFC); err == nil {
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
		if res, err := tx.Exec(q, cutoffUnix); err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				total += n
			}
		} else {
			log.Printf("[db] cleanup %s: %v", t, err)
		}
	}
	// Reclaim space after a big delete
	if total > 10000 {
		if _, err := tx.Exec(`PRAGMA incremental_vacuum`); err != nil {
			_ = err
		}
	}
	return total, tx.Commit()
}

// InsertEvent saves an event to the database.
func (d *DB) InsertEvent(event *decoder.Event) {
	detailsJSON, jerr := json.Marshal(event.Details)
	if jerr != nil {
		log.Printf("[db] marshal event details: %v", jerr)
	}
	viaMqtt := 0
	if event.ViaMqtt {
		viaMqtt = 1
	}
	_, err := d.db.Exec(
		`INSERT INTO events (time, type, from_node, to_node, rssi, snr, hop_limit, hop_start, packet_id, channel, via_mqtt, relay_node, class, details)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.Time.UTC().Format(time.RFC3339),
		string(event.Type),
		event.FromNode,
		event.ToNode,
		event.RSSI,
		event.SNR,
		event.HopLimit,
		event.HopStart,
		event.PacketID,
		event.Channel,
		viaMqtt,
		event.RelayNode,
		event.Class,
		string(detailsJSON),
	)
	if err != nil {
		log.Printf("[db] insert event: %v", err)
	}
}

// SaveNode upserts the current node state.
func (d *DB) SaveNode(n *store.NodeState) {
	neighborsJSON := []byte("[]")
	if len(n.Neighbors) > 0 {
		if b, err := json.Marshal(n.Neighbors); err == nil {
			neighborsJSON = b
		}
	}
	_, err := d.db.Exec(
		`INSERT INTO nodes (node_num, id, long_name, short_name, hw_model, role, last_heard,
		                     lat, lon, has_pos, altitude, battery, voltage, chan_util,
		                     air_util, temperature, humidity, pressure, rssi, snr, hop_limit,
		                     neighbors_json, neighbors_at, neighbor_broadcast_secs)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(node_num) DO UPDATE SET
		   id=excluded.id, long_name=excluded.long_name, short_name=excluded.short_name,
		   hw_model=excluded.hw_model, role=excluded.role, last_heard=excluded.last_heard,
		   lat=excluded.lat, lon=excluded.lon, has_pos=excluded.has_pos,
		   altitude=excluded.altitude, battery=excluded.battery, voltage=excluded.voltage,
		   chan_util=excluded.chan_util, air_util=excluded.air_util,
		   temperature=excluded.temperature, humidity=excluded.humidity,
		   pressure=excluded.pressure, rssi=excluded.rssi, snr=excluded.snr,
		   hop_limit=excluded.hop_limit,
		   neighbors_json=excluded.neighbors_json,
		   neighbors_at=excluded.neighbors_at,
		   neighbor_broadcast_secs=excluded.neighbor_broadcast_secs`,
		n.NodeNum, n.ID, n.LongName, n.ShortName, n.HWModel, n.Role, n.LastHeard,
		n.Lat, n.Lon, n.HasPos, n.Altitude, n.BatteryLevel, n.Voltage,
		n.ChannelUtilization, n.AirUtilTx, n.Temperature, n.Humidity,
		n.BarometricPressure, n.RSSI, n.SNR, n.HopLimit,
		string(neighborsJSON), n.NeighborsAt, n.NeighborBroadcastSecs,
	)
	if err != nil {
		log.Printf("[db] save node: %v", err)
	}
}

// InsertTraceroute saves a traceroute record.
func (d *DB) InsertTraceroute(tr *store.TracerouteRecord) {
	routeJSON, jerr := json.Marshal(tr.Route)
	if jerr != nil {
		log.Printf("[db] marshal traceroute route: %v", jerr)
	}
	routeBackJSON, jerr := json.Marshal(tr.RouteBack)
	if jerr != nil {
		log.Printf("[db] marshal traceroute route_back: %v", jerr)
	}
	snrTowardsJSON, jerr := json.Marshal(tr.SnrTowards)
	if jerr != nil {
		log.Printf("[db] marshal traceroute snr_towards: %v", jerr)
	}
	snrBackJSON, jerr := json.Marshal(tr.SnrBack)
	if jerr != nil {
		log.Printf("[db] marshal traceroute snr_back: %v", jerr)
	}
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
		air_util, temperature, humidity, pressure, rssi, snr, hop_limit,
		neighbors_json, neighbors_at, neighbor_broadcast_secs FROM nodes`)
	if err != nil {
		log.Printf("[db] load nodes: %v", err)
		return nil
	}
	defer rows.Close()

	var out []store.NodeState
	for rows.Next() {
		var n store.NodeState
		var hasPos int
		var neighborsJSON string
		if err := rows.Scan(&n.NodeNum, &n.ID, &n.LongName, &n.ShortName, &n.HWModel, &n.Role,
			&n.LastHeard, &n.Lat, &n.Lon, &hasPos, &n.Altitude, &n.BatteryLevel,
			&n.Voltage, &n.ChannelUtilization, &n.AirUtilTx, &n.Temperature,
			&n.Humidity, &n.BarometricPressure, &n.RSSI, &n.SNR, &n.HopLimit,
			&neighborsJSON, &n.NeighborsAt, &n.NeighborBroadcastSecs); err != nil {
			log.Printf("[db] scan node: %v", err)
			continue
		}
		n.HasPos = hasPos != 0
		if neighborsJSON != "" && neighborsJSON != "[]" {
			if uerr := json.Unmarshal([]byte(neighborsJSON), &n.Neighbors); uerr != nil {
				log.Printf("[db] unmarshal neighbors: %v", uerr)
			}
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[db] rows iteration: %v", err)
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
		if uerr := json.Unmarshal([]byte(routeJSON), &tr.Route); uerr != nil {
			log.Printf("[db] unmarshal traceroute route: %v", uerr)
		}
		if uerr := json.Unmarshal([]byte(routeBackJSON), &tr.RouteBack); uerr != nil {
			log.Printf("[db] unmarshal traceroute route_back: %v", uerr)
		}
		if uerr := json.Unmarshal([]byte(snrTowardsJSON), &tr.SnrTowards); uerr != nil {
			log.Printf("[db] unmarshal traceroute snr_towards: %v", uerr)
		}
		if uerr := json.Unmarshal([]byte(snrBackJSON), &tr.SnrBack); uerr != nil {
			log.Printf("[db] unmarshal traceroute snr_back: %v", uerr)
		}
		out = append(out, tr)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[db] rows iteration: %v", err)
	}
	return out
}

// LoadRecentEvents loads the last N events for replaying into the store.
func (d *DB) LoadRecentEvents(n int) []*decoder.Event {
	// Exclude LOG_RECORD (firmware debug log) rows from old DBs created
	// before LogRecord events were filtered out of the normal pipeline —
	// otherwise they inflate the event count on the dashboard.
	rows, err := d.db.Query(
		`SELECT time, type, from_node, to_node, rssi, snr, hop_limit,
		        COALESCE(hop_start,0), COALESCE(packet_id,0),
		        COALESCE(channel,0), COALESCE(via_mqtt,0), COALESCE(relay_node,0),
		        COALESCE(class,''), details
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
		var viaMqtt int
		if err := rows.Scan(&timeStr, &evType, &ev.FromNode, &ev.ToNode,
			&ev.RSSI, &ev.SNR, &ev.HopLimit, &ev.HopStart, &ev.PacketID,
			&ev.Channel, &viaMqtt, &ev.RelayNode, &ev.Class, &detailsJSON); err != nil {
			log.Printf("[db] scan event: %v", err)
			continue
		}
		ev.ViaMqtt = viaMqtt != 0
		if t, pErr := time.Parse(time.RFC3339, timeStr); pErr != nil {
			log.Printf("[db] parse event time %q: %v", timeStr, pErr)
		} else {
			ev.Time = t
		}
		ev.Type = decoder.EventType(evType)
		if uerr := json.Unmarshal([]byte(detailsJSON), &ev.Details); uerr != nil {
			log.Printf("[db] unmarshal event details: %v", uerr)
		}
		out = append(out, &ev)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[db] rows iteration: %v", err)
	}
	// Reverse to chronological order (oldest first)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// SnifferFilter narrows a /api/sniffer query against the events table.
// Empty / unset fields mean "no filter on this dimension".
type SnifferFilter struct {
	From    uint32
	FromSet bool
	To      uint32
	ToSet   bool
	Type    string
	Class   string
	Channel int   // -1 = unset
	Since   int64 // unix seconds; 0 = unset
	Until   int64 // unix seconds; 0 = unset
	Limit   int   // already clamped by caller
}

// LoadSniffer returns the most recent events matching the filter, newest
// first. LOG_RECORD rows are always excluded (firmware noise). class column
// may be empty for events written before the migration — those rows still
// match a class filter if the caller passes the empty string.
func (d *DB) LoadSniffer(f SnifferFilter) []*decoder.Event {
	q := `SELECT time, type, from_node, to_node, rssi, snr, hop_limit,
	             COALESCE(hop_start,0), COALESCE(packet_id,0),
	             COALESCE(channel,0), COALESCE(via_mqtt,0), COALESCE(relay_node,0),
	             COALESCE(class,''), details
	      FROM events WHERE type != 'LOG_RECORD'`
	args := []any{}
	if f.FromSet {
		q += ` AND from_node = ?`
		args = append(args, f.From)
	}
	if f.ToSet {
		q += ` AND to_node = ?`
		args = append(args, f.To)
	}
	if f.Type != "" {
		q += ` AND type = ?`
		args = append(args, f.Type)
	}
	if f.Class != "" {
		q += ` AND class = ?`
		args = append(args, f.Class)
	}
	if f.Channel >= 0 {
		q += ` AND channel = ?`
		args = append(args, f.Channel)
	}
	if f.Since > 0 {
		q += ` AND time >= ?`
		args = append(args, time.Unix(f.Since, 0).UTC().Format(time.RFC3339))
	}
	if f.Until > 0 {
		q += ` AND time <= ?`
		args = append(args, time.Unix(f.Until, 0).UTC().Format(time.RFC3339))
	}
	q += ` ORDER BY id DESC LIMIT ?`
	if f.Limit <= 0 {
		f.Limit = 200
	}
	args = append(args, f.Limit)

	rows, err := d.db.Query(q, args...)
	if err != nil {
		log.Printf("[db] sniffer: %v", err)
		return nil
	}
	defer rows.Close()

	var out []*decoder.Event
	for rows.Next() {
		var ev decoder.Event
		var timeStr, evType, detailsJSON string
		var viaMqtt int
		if err := rows.Scan(&timeStr, &evType, &ev.FromNode, &ev.ToNode,
			&ev.RSSI, &ev.SNR, &ev.HopLimit, &ev.HopStart, &ev.PacketID,
			&ev.Channel, &viaMqtt, &ev.RelayNode, &ev.Class, &detailsJSON); err != nil {
			log.Printf("[db] sniffer scan: %v", err)
			continue
		}
		ev.ViaMqtt = viaMqtt != 0
		if t, pErr := time.Parse(time.RFC3339, timeStr); pErr != nil {
			log.Printf("[db] parse event time %q: %v", timeStr, pErr)
		} else {
			ev.Time = t
		}
		ev.Type = decoder.EventType(evType)
		if uerr := json.Unmarshal([]byte(detailsJSON), &ev.Details); uerr != nil {
			log.Printf("[db] sniffer unmarshal details: %v", uerr)
		}
		out = append(out, &ev)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[db] rows iteration: %v", err)
	}
	return out
}

// ClassCountsSince returns a count of events grouped by class for entries
// at or after sinceUnix. Used by the dashboard to display the 24h breakdown
// of personal / broadcast / from_me / transit packets. Entries with an empty
// class (pre-migration rows) are returned under the key "" so the dashboard
// can show them as "unclassified" if desired.
func (d *DB) ClassCountsSince(sinceUnix int64) map[string]int64 {
	out := make(map[string]int64)
	cutoff := time.Unix(sinceUnix, 0).UTC().Format(time.RFC3339)
	rows, err := d.db.Query(
		`SELECT COALESCE(class,''), COUNT(*) FROM events
		 WHERE type != 'LOG_RECORD' AND time >= ?
		 GROUP BY class`,
		cutoff,
	)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err == nil {
			out[k] = n
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[db] rows iteration: %v", err)
	}
	return out
}

// EventCount returns the total number of events in the database.
// LOG_RECORD rows (firmware debug log) are excluded from the count — they
// are not part of the mesh event stream and should not inflate dashboard
// totals, even if an older DB contains them.
func (d *DB) EventCount() int {
	var count int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type != 'LOG_RECORD'`).Scan(&count); err != nil {
		log.Printf("[db] event count: %v", err)
	}
	return count
}

// MessageCount returns the total number of text messages.
func (d *DB) MessageCount() int {
	var count int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'TEXT_MESSAGE'`).Scan(&count); err != nil {
		log.Printf("[db] message count: %v", err)
	}
	return count
}

// MarkNotificationNack finds the most recent "sent" notification for nodeNum
// within lookbackSec seconds and changes its status to "nack:<reason>".
// Returns true if a row was updated. Used by the auto-notify scheduler when
// a Routing_ErrorReason is received from a node — the DM we just sent couldn't
// be delivered, so we retroactively mark it and log the reason.
func (d *DB) MarkNotificationNack(nodeNum uint32, reason string, lookbackSec int64) bool {
	now := time.Now().Unix()
	since := now - lookbackSec
	res, err := d.db.Exec(
		`UPDATE misbehave_notifications SET status = ?
		 WHERE id = (
		   SELECT id FROM misbehave_notifications
		    WHERE node_num = ? AND status = 'sent' AND time >= ?
		    ORDER BY time DESC LIMIT 1
		 )`,
		"nack:"+reason, nodeNum, since,
	)
	if err != nil {
		log.Printf("[db] mark nack: %v", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// InsertMisbehaveNotification stores one auto-notify attempt for the audit
// log AND for cross-restart cooldown tracking.
func (d *DB) InsertMisbehaveNotification(n *store.MisbehaveNotification) {
	_, err := d.db.Exec(
		`INSERT INTO misbehave_notifications (time, node_num, reasons, text, status) VALUES (?,?,?,?,?)`,
		n.Time, n.NodeNum, n.Reasons, n.Text, n.Status,
	)
	if err != nil {
		log.Printf("[db] insert misb-notify: %v", err)
	}
}

// LastMisbehaveNotificationSent returns the time (unix sec) of the most
// recent successful (status='sent' or 'dry-run') notification for the
// given node, or 0 if none. Used by the cooldown check.
func (d *DB) LastMisbehaveNotificationSent(nodeNum uint32) int64 {
	var t int64
	err := d.db.QueryRow(
		`SELECT COALESCE(MAX(time), 0) FROM misbehave_notifications
		 WHERE node_num = ? AND status IN ('sent','dry-run')`,
		nodeNum,
	).Scan(&t)
	if err != nil {
		log.Printf("[db] last misb-notify sent: %v", err)
		return 0
	}
	return t
}

// CountMisbehaveNotificationsSince returns how many notifications were
// "delivered" (real send OR dry-run) on or after sinceUnix. Used for the
// global rate limit (max DM/hour). dry-run is included so that running
// the simulator before going live doesn't bypass the limit — otherwise
// the user would see the real-mode behavior diverge from what dry-run
// previewed.
func (d *DB) CountMisbehaveNotificationsSince(sinceUnix int64) int {
	var n int
	if err := d.db.QueryRow(
		`SELECT COUNT(*) FROM misbehave_notifications
		 WHERE status IN ('sent','dry-run') AND time >= ?`,
		sinceUnix,
	).Scan(&n); err != nil {
		log.Printf("[db] count misb-notify since: %v", err)
	}
	return n
}

// OldestMisbehaveNotificationSince returns the unix timestamp of the OLDEST
// delivered notification (sent or dry-run) on or after sinceUnix, or 0 if
// none exists. Used by the dashboard to show "next slot in <T>": the oldest
// will roll out of the trailing-hour window first, freeing up one slot.
func (d *DB) OldestMisbehaveNotificationSince(sinceUnix int64) int64 {
	var t sql.NullInt64
	if err := d.db.QueryRow(
		`SELECT MIN(time) FROM misbehave_notifications
		 WHERE status IN ('sent','dry-run') AND time >= ?`,
		sinceUnix,
	).Scan(&t); err != nil {
		log.Printf("[db] oldest misb-notify since: %v", err)
	}
	if !t.Valid {
		return 0
	}
	return t.Int64
}

// CountMisbehaveNotificationsByNode returns a map from node_num to the
// total count of "delivered" notifications (status='sent' OR 'dry-run')
// emitted for that node, with no time filter. Used to populate the
// "Notif" column in the Misbehaving table so the operator sees at a
// glance how many DMs each flagged node has already received over its
// lifetime — handy for spotting nodes that are repeatedly misconfigured
// vs. nodes that just popped up.
func (d *DB) CountMisbehaveNotificationsByNode() map[uint32]int {
	out := make(map[uint32]int)
	rows, err := d.db.Query(
		`SELECT node_num, COUNT(*) FROM misbehave_notifications
		 WHERE status IN ('sent','dry-run') AND node_num <> 0
		 GROUP BY node_num`,
	)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var num uint32
		var n int
		if err := rows.Scan(&num, &n); err == nil {
			out[num] = n
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[db] rows iteration: %v", err)
	}
	return out
}

// LastMisbehaveNotificationSentAll returns a map from node_num to the
// timestamp of its most recent successful (sent or dry-run) notification.
// Used to populate per-node cooldown ETAs in the Misbehaving table without
// running one query per node. Only entries newer than sinceUnix are
// returned (cooldown can't be longer than the configured max anyway).
func (d *DB) LastMisbehaveNotificationSentAll(sinceUnix int64) map[uint32]int64 {
	out := make(map[uint32]int64)
	rows, err := d.db.Query(
		`SELECT node_num, MAX(time) FROM misbehave_notifications
		 WHERE status IN ('sent','dry-run') AND time >= ?
		 GROUP BY node_num`,
		sinceUnix,
	)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var num uint32
		var ts int64
		if err := rows.Scan(&num, &ts); err == nil {
			out[num] = ts
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[db] rows iteration: %v", err)
	}
	return out
}

// DeleteMisbehaveNotificationsForNode wipes every audit row of the given
// node. Used by the per-row "Reset" button on the Misbehaving page so the
// per-node cooldown clears (cooldown is computed off the audit log).
// Returns the number of rows deleted.
func (d *DB) DeleteMisbehaveNotificationsForNode(nodeNum uint32) int64 {
	res, err := d.db.Exec(`DELETE FROM misbehave_notifications WHERE node_num = ?`, nodeNum)
	if err != nil {
		log.Printf("[db] delete misb-notify (node): %v", err)
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// DeleteAllMisbehaveNotifications wipes the entire audit log. Used by the
// "Clear log" button. Side effect: every per-node cooldown and the global
// rate limit are reset to zero, since both are derived from the table.
func (d *DB) DeleteAllMisbehaveNotifications() int64 {
	res, err := d.db.Exec(`DELETE FROM misbehave_notifications`)
	if err != nil {
		log.Printf("[db] delete misb-notify (all): %v", err)
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// RecentMisbehaveNotifications returns the most recent n notification rows,
// newest first. Used by the dashboard's audit log table.
func (d *DB) RecentMisbehaveNotifications(limit int) []store.MisbehaveNotification {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.Query(
		`SELECT time, node_num, reasons, text, status
		 FROM misbehave_notifications
		 ORDER BY time DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []store.MisbehaveNotification
	for rows.Next() {
		var n store.MisbehaveNotification
		if err := rows.Scan(&n.Time, &n.NodeNum, &n.Reasons, &n.Text, &n.Status); err != nil {
			continue
		}
		n.NodeID = fmt.Sprintf("!%08x", n.NodeNum)
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[db] rows iteration: %v", err)
	}
	return out
}
