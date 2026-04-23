// mesh-reader — capture and log all communications from a Meshtastic serial node.
//
// Usage:
//
//	mesh-reader [--port COM3] [--baud 115200] [--log-dir ./logs] [--web-port :8080] [--db mesh.db] [--ignore-node MESA]
//
// If --port is omitted, the program scans all serial ports and auto-detects
// the first Meshtastic device by probing for the protocol magic bytes.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"mesh-reader/internal/db"
	"mesh-reader/internal/decoder"
	"mesh-reader/internal/fwparser"
	"mesh-reader/internal/logger"
	"mesh-reader/internal/reader"
	"mesh-reader/internal/store"
	"mesh-reader/internal/web"
)

func main() {
	port := flag.String("port", "", "Serial port, e.g. COM3 (Windows) or /dev/ttyUSB0 (Linux/macOS)")
	host := flag.String("host", "", "WiFi/TCP host of the Meshtastic node, e.g. 192.168.1.42 (port 4403 by default)")
	baud := flag.Int("baud", 115200, "Baud rate")
	logDir := flag.String("log-dir", "./logs", "Directory where log files are stored")
	rawLog := flag.Bool("raw-log", false, "Also write a raw packet log (JSONL with hex bytes) in the log directory")
	enableDebugLog := flag.Bool("enable-debug-log", false, "Ask the node to stream firmware debug log (LogRecord) via the API")
	disableDebugLog := flag.Bool("disable-debug-log", false, "Ask the node to stop streaming firmware debug log, then exit")
	webPort := flag.String("web-port", ":8111", "HTTP port for the web dashboard, e.g. :8080 (use 'off' to disable)")
	dbPath := flag.String("db", "mesh.db", "SQLite database path for persistence")
	dbRetention := flag.Int("db-retention-days", 30, "Delete events/signals/snapshots older than N days (0 = keep forever)")
	logCompressDays := flag.Int("log-compress-days", 7, "Gzip .txt/.jsonl log files older than N days (0 = disabled)")
	ignoreNode := flag.String("ignore-node", "", "Short name of a node whose telemetry should be discarded (e.g. MESA)")
	verbose := flag.Int("verbose", 0, "Console verbosity: 0=quiet, 1=packets, 2=debug")
	flag.Parse()

	// "off" disables the dashboard entirely.
	if strings.EqualFold(*webPort, "off") || *webPort == "-" {
		*webPort = ""
	} else if *webPort != "" && !strings.Contains(*webPort, ":") {
		// Accept "8111" as shorthand for ":8111"
		*webPort = ":" + *webPort
	}

	if *host == "" && *port == "" {
		log.Println("[mesh-reader] no --port/--host specified, scanning for Meshtastic device...")
		detected, err := reader.DetectPort(*baud)
		if err != nil {
			exitWithPause(fmt.Sprintf("[error] auto-detect failed: %v\n  Usa --port COMX o --host 192.168.x.x per specificare manualmente.", err))
		}
		*port = detected
	}

	l, err := logger.New(*logDir)
	if err != nil {
		log.Fatalf("[error] logger: %v", err)
	}
	l.SetVerbose(*verbose)
	if *rawLog {
		l.EnableRawLog()
		log.Printf("[mesh-reader] raw packet log attivo in %s/mesh-raw-YYYY-MM-DD.jsonl", *logDir)
	}
	// NOTE: logger/db/reader closed explicitly in graceful shutdown below.

	// Open SQLite database
	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("[error] database: %v", err)
	}

	// connect opens a new Reader using the configured transport.
	// Used for initial connection and for auto-reconnect after EOF.
	connect := func() (*reader.Reader, error) {
		if *host != "" {
			return reader.NewTCP(*host)
		}
		return reader.New(*port, *baud)
	}

	// Try initial connection with exponential backoff (max ~5 min total).
	// This avoids crashing when the USB cable is momentarily unplugged at startup
	// or the node is still booting.
	var r *reader.Reader
	{
		delays := []time.Duration{0, 3 * time.Second, 6 * time.Second, 12 * time.Second, 30 * time.Second, 60 * time.Second, 120 * time.Second}
		for i, d := range delays {
			if d > 0 {
				log.Printf("[mesh-reader] ritento connessione tra %s...", d)
				time.Sleep(d)
			}
			r, err = connect()
			if err == nil {
				break
			}
			if i == len(delays)-1 {
				exitWithPause(fmt.Sprintf("[error] connessione fallita dopo vari tentativi: %v", err))
			}
			log.Printf("[mesh-reader] tentativo %d/%d fallito: %v", i+1, len(delays), err)
		}
	}
	// reader closed explicitly in graceful shutdown below

	dec := decoder.New()
	s := store.New(10000)

	// Restore persisted state from DB
	loadHistory(database, s)

	if *host != "" {
		log.Printf("[mesh-reader] connected to %s (TCP)", *host)
	} else {
		log.Printf("[mesh-reader] connected to %s @ %d baud", *port, *baud)
	}
	log.Printf("[mesh-reader] writing logs to %s/", *logDir)
	log.Printf("[mesh-reader] database: %s", *dbPath)

	// Shared reader pointer so heartbeat goroutine follows reconnects.
	rmu := &sync.Mutex{}
	currentReader := r

	// Start web dashboard if requested
	if *webPort != "" {
		webSrv := web.NewWithDB(s, database)
		// Wire on-demand traceroute: web UI POSTs /api/traceroute/{id},
		// we call SendTraceroute on the *current* reader (follows reconnects).
		webSrv.SetTracerouteSender(func(dest uint32, hop uint32) error {
			rmu.Lock()
			cr := currentReader
			rmu.Unlock()
			if cr == nil {
				return fmt.Errorf("not connected to a Meshtastic node")
			}
			return cr.SendTraceroute(dest, hop)
		})
		go func() {
			if err := webSrv.ListenAndServe(*webPort); err != nil {
				log.Fatalf("[error] web server: %v", err)
			}
		}()
	}

	// Periodic snapshot goroutine (every 5 min):
	// - Radio-health snapshot
	// - Channel-utilization snapshot
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now().Unix()
			// Radio health
			rh := s.RadioHealth()
			if rh.Enabled {
				database.SaveRadioSnapshot(now, rh)
			}
			// Channel utilization aggregate
			cu := s.AggregateChannelUtil()
			if cu.NodesReporting > 0 {
				database.InsertChannelSnapshot(db.ChannelSnapshot{
					Time:           now,
					NodesReporting: cu.NodesReporting,
					AvgChanUtil:    cu.AvgChanUtil,
					MaxChanUtil:    cu.MaxChanUtil,
					AvgAirUtil:     cu.AvgAirUtil,
					MaxAirUtil:     cu.MaxAirUtil,
					TopTalkerNum:   cu.TopTalkerNum,
					TopTalkerUtil:  cu.TopTalkerUtil,
				})
			}
		}
	}()

	// Log compression: gzip .log/.jsonl files older than N days.
	// Runs 10 min after startup and every 24h.
	if *logCompressDays > 0 {
		go func() {
			runCompress := func() {
				n, saved, err := l.CompressOldLogs(*logCompressDays)
				if err != nil {
					log.Printf("[logger] compress error: %v", err)
					return
				}
				if n > 0 {
					log.Printf("[logger] gzipped %d old log file(s), saved %.1f MB", n, float64(saved)/(1024*1024))
				}
			}
			time.Sleep(10 * time.Minute)
			runCompress()
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				runCompress()
			}
		}()
		log.Printf("[mesh-reader] log compression: >%d giorni → gzip", *logCompressDays)
	}

	// DB retention cleanup: runs at startup and every 6h.
	// Deletes rows older than --db-retention-days from high-volume tables
	// (events, signal_history, radio_snapshots, channel_snapshots, node_availability).
	if *dbRetention > 0 {
		go func() {
			runCleanup := func() {
				n, err := database.CleanupOld(*dbRetention)
				if err != nil {
					log.Printf("[db] retention cleanup error: %v", err)
					return
				}
				if n > 0 {
					log.Printf("[db] retention cleanup: deleted %d rows older than %d days", n, *dbRetention)
				}
			}
			// Delay first run 5 minutes after startup (let initial load finish)
			time.Sleep(5 * time.Minute)
			runCleanup()
			ticker := time.NewTicker(6 * time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				runCleanup()
			}
		}()
		log.Printf("[mesh-reader] DB retention: %d giorni", *dbRetention)
	}

	// Node availability scanner: checks every 60 s for offline transitions.
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			offlines := s.ScanOffline()
			for _, tr := range offlines {
				database.InsertAvailability(db.AvailabilityEvent{
					Time:    tr.Time,
					NodeNum: tr.NodeNum,
					Event:   tr.Event,
				})
				if *verbose >= 1 {
					log.Printf("[avail] node !%08x went OFFLINE (not heard for 30 min)", tr.NodeNum)
				}
			}
		}
	}()

	// Heartbeat goroutine: keeps the serial API connection alive.
	// Meshtastic firmware drops the serial client after ~15 min of silence.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			rmu.Lock()
			cr := currentReader
			rmu.Unlock()
			if cr == nil {
				continue
			}
			if err := sendHeartbeat(cr); err != nil {
				log.Printf("[warn] heartbeat failed: %v", err)
			} else if *verbose >= 2 {
				log.Println("[mesh-reader] heartbeat sent")
			}
		}
	}()

	// Meshtastic firmware 2.x handshake
	if err := sendWantConfig(r); err != nil {
		log.Fatalf("[error] want_config handshake: %v", err)
	}
	log.Println("[mesh-reader] handshake sent — receiving config...")

	// Optional: toggle firmware debug log stream via AdminMessage
	if *enableDebugLog {
		if err := r.SetDebugLogApi(true); err != nil {
			log.Printf("[warn] enable debug log: %v", err)
		} else {
			log.Println("[mesh-reader] debug log stream ENABLED on node (LogRecord events in arrivo)")
		}
	}
	if *disableDebugLog {
		if err := r.SetDebugLogApi(false); err != nil {
			log.Printf("[warn] disable debug log: %v", err)
		} else {
			log.Println("[mesh-reader] debug log stream DISABLED on node")
		}
		log.Println("[mesh-reader] uscita richiesta dopo --disable-debug-log")
		time.Sleep(500 * time.Millisecond) // let the write flush
		return
	}

	errCh := make(chan error, 1)
	go func() {
		configPhase := true // true while device is dumping its node DB
		configNodes := 0
		var myNode uint32   // our local node number (from MyInfo)
		var ignoreNum uint32 // resolved node number for --ignore-node

		connectedAt := time.Now()
		backoff := 3 * time.Second

		for {
			rmu.Lock()
			cr := currentReader
			rmu.Unlock()

			raw, err := cr.ReadPacket()
			if err != nil {
				if isFatal(err) {
					// Exponential backoff if the connection keeps dropping quickly
					if time.Since(connectedAt) < 30*time.Second {
						backoff *= 2
						if backoff > 60*time.Second {
							backoff = 60 * time.Second
						}
					} else {
						backoff = 3 * time.Second
					}

					log.Printf("[mesh-reader] connessione persa (%v) — riconnessione tra %s...", err, backoff)
					cr.Close()
					time.Sleep(backoff)

					var newR *reader.Reader
					for attempt := 1; ; attempt++ {
						newR, err = connect()
						if err == nil {
							break
						}
						log.Printf("[mesh-reader] riconnessione tentativo %d fallito: %v — riprovo tra 5s", attempt, err)
						time.Sleep(5 * time.Second)
					}
					rmu.Lock()
					currentReader = newR
					rmu.Unlock()

					log.Println("[mesh-reader] riconnesso — nuovo handshake...")
					if err := sendWantConfig(newR); err != nil {
						log.Printf("[warn] handshake dopo reconnect: %v", err)
					}
					configPhase = true
					configNodes = 0
					connectedAt = time.Now()
					continue
				}
				if *verbose >= 1 {
					log.Printf("[warn] framing: %v — resyncing", err)
				}
				continue
			}

			if *verbose >= 2 {
				log.Printf("[debug] raw packet received: %d bytes", len(raw))
			}

			event, err := dec.Decode(raw)
			if err != nil {
				if *verbose >= 1 {
					log.Printf("[warn] decode: %v (%d bytes)", err, len(raw))
				}
				continue
			}
			if event == nil {
				if *verbose >= 2 {
					log.Printf("[debug] decode returned nil (internal config msg)")
				}
				continue // internal radio config — discarded by decoder
			}

			if *verbose >= 2 {
				log.Printf("[debug] decoded: type=%s from=!%08x configPhase=%v", event.Type, event.FromNode, configPhase)
			}

			// ── Firmware debug log ──
			// LogRecord events don't belong in the mesh event stream
			// (they would flood the ring buffer and the DB). We:
			//   1) write them to the dedicated fwlog file via the logger
			//   2) try to parse them for radio-health metrics
			//   3) skip the rest of the pipeline (store.Add, DB insert, etc).
			if event.Type == decoder.EventLogRecord {
				l.Log(event)
				if msg, ok := event.Details["message"].(string); ok {
					if rx := fwparser.ParseRawRx(msg); rx != nil {
						s.AddRawRx(rx, event.Time)
					} else if d := fwparser.ParseDupe(msg); d != nil {
						_ = d
						s.AddRawDupe(event.Time)
					} else if reason := fwparser.ParseNoRebroadcast(msg); reason != "" {
						s.AddRawNoRebroadcast(reason)
					} else if hz, ok := fwparser.ParseFreqOffset(msg); ok {
						s.AddFreqOffset(hz)
					}
				}
				continue
			}

			// ── Config-phase end ──
			if event.Type == decoder.EventConfigComplete {
				if configPhase {
					configPhase = false
					log.Printf("[mesh-reader] config complete — %d nodes received, now receiving live packets (Ctrl+C to stop)", configNodes)
					// Send an immediate heartbeat to tell the firmware we
					// want to stay connected.  Some TCP firmware versions
					// close the connection right after config_complete if
					// they don't receive a prompt keepalive signal.
					if err := sendHeartbeat(cr); err != nil {
						log.Printf("[warn] post-config heartbeat: %v", err)
					}
				}
				continue
			}

			// ── Config-phase: update state silently, don't count as events ──
			if configPhase {
				if tr := s.AddSilent(event); tr != nil {
					database.InsertAvailability(db.AvailabilityEvent{
						Time: tr.Time, NodeNum: tr.NodeNum, Event: tr.Event,
					})
				}
				if event.FromNode != 0 {
					if node, ok := s.NodeByNum(event.FromNode); ok {
						database.SaveNode(&node)
					}
				}
				if event.Type == decoder.EventNodeInfo || event.Type == decoder.EventMyInfo {
					configNodes++
				}
				// Resolve --ignore-node during config phase too
				if *ignoreNode != "" && ignoreNum == 0 && event.Type == decoder.EventNodeInfo {
					if sn, ok := event.Details["short_name"].(string); ok && strings.EqualFold(sn, *ignoreNode) {
						ignoreNum = event.FromNode
						log.Printf("[mesh-reader] ignoring telemetry from node %q (!%08x)", sn, ignoreNum)
					}
				}
				continue
			}

			// Track our local node number
			if event.Type == decoder.EventMyInfo && event.FromNode != 0 {
				myNode = event.FromNode
			}

			// Resolve --ignore-node short name to node number
			if *ignoreNode != "" && ignoreNum == 0 && event.Type == decoder.EventNodeInfo {
				if sn, ok := event.Details["short_name"].(string); ok && strings.EqualFold(sn, *ignoreNode) {
					ignoreNum = event.FromNode
					log.Printf("[mesh-reader] ignoring telemetry from node %q (!%08x)", sn, ignoreNum)
				}
			}

			// ── Discard local serial-only telemetry/position from our own node ──
			if myNode != 0 && event.FromNode == myNode &&
				event.RSSI == 0 && event.SNR == 0 &&
				(event.Type == decoder.EventTelemetry || event.Type == decoder.EventPosition) {
				continue
			}

			// ── Discard telemetry from --ignore-node ──
			if ignoreNum != 0 && event.FromNode == ignoreNum &&
				event.Type == decoder.EventTelemetry {
				continue
			}

			// ── Live phase: normal processing ──
			if *verbose >= 1 {
				log.Printf("[pkt] %s from !%08x → !%08x  RSSI=%d SNR=%.1f hop=%d/%d",
					event.Type, event.FromNode, event.ToNode,
					event.RSSI, event.SNR, event.HopLimit, event.HopStart)
			}
			l.Log(event)
			tr := s.Add(event)

			// Persist availability transition
			if tr != nil {
				database.InsertAvailability(db.AvailabilityEvent{
					Time:    tr.Time,
					NodeNum: tr.NodeNum,
					Event:   tr.Event,
				})
				if *verbose >= 1 {
					log.Printf("[avail] node !%08x came ONLINE", tr.NodeNum)
				}
			}

			// Persist to SQLite
			database.InsertEvent(event)

			// Persist per-node ChUtil sample when a Telemetry event carries it.
			// Drives the ChUtil Geo-Monitor layer on the map.
			if event.Type == decoder.EventTelemetry && event.FromNode != 0 {
				var cu, au float64
				if v, ok := event.Details["channel_utilization_%"]; ok {
					cu = toFloat64(v)
				}
				if v, ok := event.Details["air_util_tx_%"]; ok {
					au = toFloat64(v)
				}
				if cu > 0 {
					database.InsertChUtilSample(db.ChUtilSample{
						NodeNum:  event.FromNode,
						Time:     event.Time.Unix(),
						ChanUtil: cu,
						AirUtil:  au,
					})
				}
			}

			// Persist signal sample
			if event.FromNode != 0 && (event.RSSI != 0 || event.SNR != 0) {
				database.InsertSignal(db.SignalSample{
					Time:     event.Time.Unix(),
					NodeNum:  event.FromNode,
					RSSI:     event.RSSI,
					SNR:      event.SNR,
					HopLimit: event.HopLimit,
					HopStart: event.HopStart,
				})
			}

			if event.FromNode != 0 {
				if node, ok := s.NodeByNum(event.FromNode); ok {
					database.SaveNode(&node)
				}
			}
			if event.Type == decoder.EventTraceroute {
				tr := store.TracerouteRecord{
					Time: event.Time.Unix(),
					From: event.FromNode,
					To:   event.ToNode,
				}
				if v, ok := event.Details["route"].([]string); ok {
					tr.Route = v
				}
				if v, ok := event.Details["route_back"].([]string); ok {
					tr.RouteBack = v
				}
				if v, ok := event.Details["snr_towards"].([]int32); ok {
					tr.SnrTowards = v
				}
				if v, ok := event.Details["snr_back"].([]int32); ok {
					tr.SnrBack = v
				}
				database.InsertTraceroute(&tr)
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sig:
		log.Println("[mesh-reader] shutting down...")
	case err := <-errCh:
		log.Printf("[mesh-reader] fatal error: %v", err)
	}

	// Graceful shutdown: flush log files, close serial/TCP, close DB cleanly.
	// This prevents SQLite corruption on abrupt exit and makes sure the
	// current day's .txt log is flushed/closed.
	shutdownStart := time.Now()
	rmu.Lock()
	if currentReader != nil {
		_ = currentReader.Close()
	}
	rmu.Unlock()
	if err := l.Close(); err != nil {
		log.Printf("[shutdown] logger close: %v", err)
	}
	if err := database.Close(); err != nil {
		log.Printf("[shutdown] db close: %v", err)
	}
	log.Printf("[mesh-reader] shutdown complete in %s", time.Since(shutdownStart).Round(time.Millisecond))
}

// toFloat64 converts a value from decoder.Event.Details (which may hold
// float32, float64, or integer types depending on the protobuf field) into
// a float64 in a forgiving way. Returns 0 for anything unexpected.
func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case uint32:
		return float64(n)
	case uint64:
		return float64(n)
	}
	return 0
}

func loadHistory(database *db.DB, s *store.Store) {
	// Load nodes (with positions, telemetry, names)
	nodes := database.LoadNodes()
	if len(nodes) > 0 {
		s.LoadNodes(nodes)
		log.Printf("[db] restored %d nodes", len(nodes))
	}
	// Load traceroutes
	traceroutes := database.LoadTraceroutes()
	if len(traceroutes) > 0 {
		s.LoadTraceroutes(traceroutes)
		log.Printf("[db] restored %d traceroutes", len(traceroutes))
	}
	// Load recent events into ring buffer
	events := database.LoadRecentEvents(10000)
	if len(events) > 0 {
		s.LoadEvents(events)
		log.Printf("[db] restored %d recent events", len(events))
	}
	// Set accurate totals from DB
	s.SetCounts(database.EventCount(), database.MessageCount())
}

func sendWantConfig(r *reader.Reader) error {
	toRadio := &pb.ToRadio{
		PayloadVariant: &pb.ToRadio_WantConfigId{
			WantConfigId: rand.Uint32() | 1,
		},
	}
	data, err := proto.Marshal(toRadio)
	if err != nil {
		return err
	}
	return r.WriteFrame(data)
}

func sendHeartbeat(r *reader.Reader) error {
	toRadio := &pb.ToRadio{
		PayloadVariant: &pb.ToRadio_Heartbeat{
			Heartbeat: &pb.Heartbeat{},
		},
	}
	data, err := proto.Marshal(toRadio)
	if err != nil {
		return err
	}
	return r.WriteFrame(data)
}

func isFatal(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "closed") ||
		strings.Contains(msg, "disconnected") ||
		strings.Contains(msg, "access denied")
}

// exitWithPause prints an error message and waits for Enter before exiting.
// This keeps the console window open when launched via double-click so the
// user can actually read the error.
func exitWithPause(msg string) {
	log.Println(msg)
	fmt.Println("\nPremi Invio per chiudere...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
	os.Exit(1)
}
