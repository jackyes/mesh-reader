// Package app orchestrates the mesh-reader: connection, event loop, background
// goroutines, and graceful shutdown via context.Context.
package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"strings"
	"sync"
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

// Config holds all CLI-tunable parameters.
type Config struct {
	Port             string
	Host             string
	Baud             int
	LogDir           string
	RawLog           bool
	EnableDebugLog   bool
	DisableDebugLog  bool
	WebPort          string
	DBPath           string
	DBRetention      int
	LogCompressDays  int
	IgnoreNode       string
	NotIgnoreSelf    bool
	Verbose          int
}

// App wires together the mesh-reader pipeline.
type App struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	store    *store.Store
	database *db.DB
	logger   *logger.Logger
	webSrv   *web.Server

	mu            sync.Mutex
	currentReader *reader.Reader
	connect       func() (*reader.Reader, error)
}

// New creates an App from the given config.
func New(cfg Config) (*App, error) {
	ctx, cancel := context.WithCancel(context.Background())

	l, err := logger.New(cfg.LogDir)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("logger: %w", err)
	}
	l.SetVerbose(cfg.Verbose)
	if cfg.RawLog {
		l.EnableRawLog()
		log.Printf("[mesh-reader] raw packet log enabled in %s/mesh-raw-YYYY-MM-DD.jsonl", cfg.LogDir)
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		cancel()
		l.Close()
		return nil, fmt.Errorf("database: %w", err)
	}

	connect := func() (*reader.Reader, error) {
		if cfg.Host != "" {
			return reader.NewTCP(cfg.Host)
		}
		return reader.New(cfg.Port, cfg.Baud)
	}

	app := &App{
		cfg:      cfg,
		ctx:      ctx,
		cancel:   cancel,
		store:    store.New(10000),
		database: database,
		logger:   l,
		connect:  connect,
	}

	if cfg.WebPort != "" {
		app.webSrv = web.NewWithDB(app.store, database)
	}

	return app, nil
}

// Run starts the mesh-reader pipeline and blocks until shutdown or fatal error.
func (a *App) Run() error {
	defer a.cancel()

	// Connect with exponential backoff.
	r, err := a.connectWithBackoff()
	if err != nil {
		return err
	}
	a.currentReader = r

	if a.cfg.Host != "" {
		log.Printf("[mesh-reader] connected to %s (TCP)", a.cfg.Host)
	} else {
		log.Printf("[mesh-reader] connected to %s @ %d baud", a.cfg.Port, a.cfg.Baud)
	}
	log.Printf("[mesh-reader] writing logs to %s/", a.cfg.LogDir)
	log.Printf("[mesh-reader] database: %s", a.cfg.DBPath)

	// Restore persisted state.
	a.loadHistory()

	// Start web dashboard.
	if a.webSrv != nil && a.cfg.WebPort != "" {
		a.wireWebServer()
		go func() {
			if err := a.webSrv.ListenAndServe(a.cfg.WebPort); err != nil {
				select {
				case <-a.ctx.Done():
					// Shutting down, ignore.
				default:
					log.Printf("[error] web server: %v", err)
				}
			}
		}()
		go a.runAutoNotifyLoop()
	}

	// Background goroutines (all check ctx.Done() and return cleanly).
	go a.runSnapshotLoop()
	if a.cfg.LogCompressDays > 0 {
		go a.runLogCompressLoop()
	}
	if a.cfg.DBRetention > 0 {
		go a.runRetentionLoop()
	}
	go a.runAvailabilityLoop()
	go a.runHeartbeatLoop()

	// Meshtastic 2.x handshake.
	if err := sendWantConfig(a.currentReader); err != nil {
		return fmt.Errorf("want_config handshake: %w", err)
	}
	log.Println("[mesh-reader] handshake sent - receiving config...")

	if a.cfg.EnableDebugLog {
		if err := a.currentReader.SetDebugLogApi(true); err != nil {
			log.Printf("[warn] enable debug log: %v", err)
		} else {
			log.Println("[mesh-reader] debug log stream ENABLED")
		}
	}
	if a.cfg.DisableDebugLog {
		if err := a.currentReader.SetDebugLogApi(false); err != nil {
			log.Printf("[warn] disable debug log: %v", err)
		} else {
			log.Println("[mesh-reader] debug log stream DISABLED")
		}
		time.Sleep(500 * time.Millisecond)
		return nil
	}

	// Main read loop blocks until context is cancelled or fatal error.
	return a.runReadLoop()
}

// Shutdown initiates a graceful shutdown: cancels the context (which stops
// all background goroutines), closes the web server, reader, logger, and database.
func (a *App) Shutdown() {
	start := time.Now()
	a.cancel()

	// Give the web server a moment to drain.
	if a.webSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := a.webSrv.Shutdown(ctx); err != nil {
			log.Printf("[shutdown] web server: %v", err)
		}
	}

	if a.currentReader != nil {
		a.currentReader.Close()
	}
	if err := a.logger.Close(); err != nil {
		log.Printf("[shutdown] logger close: %v", err)
	}
	if err := a.database.Close(); err != nil {
		log.Printf("[shutdown] db close: %v", err)
	}
	log.Printf("[mesh-reader] shutdown complete in %s", time.Since(start).Round(time.Millisecond))
}

func (a *App) connectWithBackoff() (*reader.Reader, error) {
	if a.cfg.Host == "" && a.cfg.Port == "" {
		log.Println("[mesh-reader] no --port/--host specified, scanning for Meshtastic device...")
		detected, err := reader.DetectPort(a.cfg.Baud)
		if err != nil {
			return nil, fmt.Errorf("auto-detect failed: %w\n  Use --port COMX or --host 192.168.x.x to specify manually", err)
		}
		a.cfg.Port = detected
	}

	delays := []time.Duration{0, 3 * time.Second, 6 * time.Second, 12 * time.Second, 30 * time.Second, 60 * time.Second, 120 * time.Second}
	for i, d := range delays {
		if d > 0 {
			log.Printf("[mesh-reader] retrying connection in %s...", d)
			time.Sleep(d)
		}
		r, err := a.connect()
		if err == nil {
			return r, nil
		}
		if i == len(delays)-1 {
			return nil, fmt.Errorf("connection failed after %d attempts: %w", len(delays), err)
		}
		log.Printf("[mesh-reader] attempt %d/%d failed: %v", i+1, len(delays), err)
	}
	return nil, fmt.Errorf("unreachable")
}

func (a *App) wireWebServer() {
	a.webSrv.SetTracerouteSender(func(dest uint32, hop uint32) error {
		cr := a.currentReader
		if cr == nil {
			return fmt.Errorf("not connected")
		}
		return cr.SendTraceroute(dest, hop)
	})
	a.webSrv.SetTextMessageSender(func(dest uint32, text string, ch uint32, hop uint32) (uint32, error) {
		cr := a.currentReader
		if cr == nil {
			return 0, fmt.Errorf("not connected")
		}
		return cr.SendTextMessage(dest, text, ch, hop)
	})
	a.webSrv.SetMisbehaveConfigPath(misbDefaultsPath(a.cfg.DBPath))

	a.store.OnRoutingError = func(failingNode uint32, reason string) {
		if a.database.MarkNotificationNack(failingNode, reason, 300) {
			log.Printf("[misb-notify] NACK from !%08x: %s (DM marked as undelivered)", failingNode, reason)
		}
	}
}

func (a *App) loadHistory() {
	// Nodes
	nodes := a.database.LoadNodes()
	if len(nodes) > 0 {
		a.store.LoadNodes(nodes)
		log.Printf("[db] restored %d nodes", len(nodes))
		if n := a.store.RebuildLinksFromNeighbors(24 * 3600); n > 0 {
			log.Printf("[db] restored %d neighbor links (<24h)", n)
		}
	}
	// Traceroutes
	if trs := a.database.LoadTraceroutes(); len(trs) > 0 {
		a.store.LoadTraceroutes(trs)
		log.Printf("[db] restored %d traceroutes", len(trs))
	}
	// Events
	if evts := a.database.LoadRecentEvents(10000); len(evts) > 0 {
		a.store.LoadEvents(evts)
		log.Printf("[db] restored %d recent events", len(evts))
	}
	a.store.SetCounts(a.database.EventCount(), a.database.MessageCount())
}

func (a *App) runReadLoop() error {

	dec := decoder.New()

	configPhase := true
	configNodes := 0
	var myNode uint32
	var ignoreNum uint32
	var selfIdentified bool
	neighborInfoLogged := false

	connectedAt := time.Now()
	backoff := 3 * time.Second

	for {
		select {
		case <-a.ctx.Done():
			return nil
		default:
		}

		raw, err := a.currentReader.ReadPacket()
		if err != nil {
			if isFatal(err) {
				if time.Since(connectedAt) < 30*time.Second {
					backoff *= 2
					if backoff > 60*time.Second {
						backoff = 60 * time.Second
					}
				} else {
					backoff = 3 * time.Second
				}

				log.Printf("[mesh-reader] connection lost (%v) - reconnecting in %s...", err, backoff)
				a.currentReader.Close()
				time.Sleep(backoff)

				var newR *reader.Reader
				for attempt := 1; ; attempt++ {
					newR, err = a.connect()
					if err == nil {
						break
					}
					log.Printf("[mesh-reader] reconnect attempt %d failed: %v - retrying in 5s", attempt, err)
					time.Sleep(5 * time.Second)
				}
				a.mu.Lock()
				a.currentReader = newR
				a.mu.Unlock()

				log.Println("[mesh-reader] reconnected - new handshake...")
				if err := sendWantConfig(newR); err != nil {
					log.Printf("[warn] handshake after reconnect: %v", err)
				}
				configPhase = true
				configNodes = 0
				connectedAt = time.Now()
				continue
			}
			if a.cfg.Verbose >= 1 {
				log.Printf("[warn] framing: %v - resyncing", err)
			}
			continue
		}

		if a.cfg.Verbose >= 2 {
			log.Printf("[debug] raw packet received: %d bytes", len(raw))
		}

		event, err := dec.Decode(raw)
		if err != nil {
			if a.cfg.Verbose >= 1 {
				log.Printf("[warn] decode: %v (%d bytes)", err, len(raw))
			}
			continue
		}
		if event == nil {
			if a.cfg.Verbose >= 2 {
				log.Printf("[debug] decode returned nil (internal config msg)")
			}
			continue
		}

		if a.cfg.Verbose >= 2 {
			log.Printf("[debug] decoded: type=%s from=!%08x configPhase=%v", event.Type, event.FromNode, configPhase)
		}

		// Firmware debug log.
		if event.Type == decoder.EventLogRecord {
			a.logger.Log(event)
			if msg, ok := event.Details["message"].(string); ok {
				if rx := fwparser.ParseRawRx(msg); rx != nil {
					a.store.AddRawRx(rx, event.Time)
				} else if d := fwparser.ParseDupe(msg); d != nil {
					a.store.AddRawDupe(event.Time)
				} else if reason := fwparser.ParseNoRebroadcast(msg); reason != "" {
					a.store.AddRawNoRebroadcast(reason)
				} else if hz, ok := fwparser.ParseFreqOffset(msg); ok {
					a.store.AddFreqOffset(hz)
				}
			}
			continue
		}

		// Config-phase end.
		if event.Type == decoder.EventConfigComplete {
			if configPhase {
				configPhase = false
				log.Printf("[mesh-reader] config complete - %d nodes received, now receiving live packets", configNodes)
				if err := sendHeartbeat(a.currentReader); err != nil {
					log.Printf("[warn] post-config heartbeat: %v", err)
				}
			}
			continue
		}

		// Config-phase: silent state update.
		if configPhase {
			if tr := a.store.AddSilent(event); tr != nil {
				a.database.InsertAvailability(db.AvailabilityEvent{
					Time: tr.Time, NodeNum: tr.NodeNum, Event: tr.Event,
				})
			}
			if event.FromNode != 0 {
				if node, ok := a.store.NodeByNum(event.FromNode); ok {
					a.database.SaveNode(&node)
				}
			}
			if event.Type == decoder.EventNodeInfo || event.Type == decoder.EventMyInfo {
				configNodes++
			}
			if event.Type == decoder.EventMyInfo && event.FromNode != 0 {
				myNode = event.FromNode
			}
			if !selfIdentified && event.Type == decoder.EventNodeInfo {
				if mn := a.store.MyNodeNum(); mn != 0 && event.FromNode == mn {
					sn, _ := event.Details["short_name"].(string)
					ln, _ := event.Details["long_name"].(string)
					selfIdentified = true
					verb := "auto-ignoring"
					if a.cfg.NotIgnoreSelf {
						verb = "self-counting (--not-ignore-self)"
					}
					log.Printf("[mesh-reader] %s own telemetry/position from %q / %q (!%08x)", verb, sn, ln, mn)
				}
			}
			if a.cfg.IgnoreNode != "" && ignoreNum == 0 && event.Type == decoder.EventNodeInfo {
				if sn, ok := event.Details["short_name"].(string); ok && strings.EqualFold(sn, a.cfg.IgnoreNode) {
					ignoreNum = event.FromNode
					log.Printf("[mesh-reader] ignoring telemetry from node %q (!%08x)", sn, ignoreNum)
				}
			}
			continue
		}

		// Track our local node number.
		if event.Type == decoder.EventMyInfo && event.FromNode != 0 {
			myNode = event.FromNode
		}
		if !selfIdentified && event.Type == decoder.EventNodeInfo && myNode != 0 && event.FromNode == myNode {
			sn, _ := event.Details["short_name"].(string)
			ln, _ := event.Details["long_name"].(string)
			selfIdentified = true
			verb := "auto-ignoring"
			if a.cfg.NotIgnoreSelf {
				verb = "self-counting (--not-ignore-self)"
			}
			log.Printf("[mesh-reader] %s own telemetry/position from %q / %q (!%08x)", verb, sn, ln, myNode)
		}

		if a.cfg.IgnoreNode != "" && ignoreNum == 0 && event.Type == decoder.EventNodeInfo {
			if sn, ok := event.Details["short_name"].(string); ok && strings.EqualFold(sn, a.cfg.IgnoreNode) {
				ignoreNum = event.FromNode
				log.Printf("[mesh-reader] ignoring telemetry from node %q (!%08x)", sn, ignoreNum)
			}
		}

		// Discard own telemetry/position unless --not-ignore-self.
		if !a.cfg.NotIgnoreSelf && myNode != 0 && event.FromNode == myNode &&
			(event.Type == decoder.EventTelemetry || event.Type == decoder.EventPosition) {
			continue
		}

		// Discard telemetry from --ignore-node.
		if ignoreNum != 0 && event.FromNode == ignoreNum &&
			event.Type == decoder.EventTelemetry {
			continue
		}

		// Live phase: normal processing.
		if a.cfg.Verbose >= 1 {
			log.Printf("[pkt] %s from !%08x -> !%08x  RSSI=%d SNR=%.1f hop=%d/%d",
				event.Type, event.FromNode, event.ToNode,
				event.RSSI, event.SNR, event.HopLimit, event.HopStart)
		}
		if !neighborInfoLogged && event.Type == decoder.EventNeighborInfo {
			count, _ := event.Details["neighbor_count"].(int)
			log.Printf("[mesh-reader] first NeighborInfo received from !%08x (%d neighbors)", event.FromNode, count)
			neighborInfoLogged = true
		}

		a.logger.Log(event)
		tr := a.store.Add(event)

		if tr != nil {
			a.database.InsertAvailability(db.AvailabilityEvent{
				Time: tr.Time, NodeNum: tr.NodeNum, Event: tr.Event,
			})
			if a.cfg.Verbose >= 1 {
				log.Printf("[avail] node !%08x came ONLINE", tr.NodeNum)
			}
		}

		a.database.InsertEvent(event)

		// Per-node ChUtil sample.
		if event.Type == decoder.EventTelemetry && event.FromNode != 0 {
			var cu, au float64
			if v, ok := event.Details["channel_utilization_%"]; ok {
				cu = toFloat64(v)
			}
			if v, ok := event.Details["air_util_tx_%"]; ok {
				au = toFloat64(v)
			}
			if cu > 0 {
				a.database.InsertChUtilSample(db.ChUtilSample{
					NodeNum:  event.FromNode,
					Time:     event.Time.Unix(),
					ChanUtil: cu,
					AirUtil:  au,
				})
			}
		}

		// Signal sample.
		if event.FromNode != 0 && (event.RSSI != 0 || event.SNR != 0) {
			a.database.InsertSignal(db.SignalSample{
				Time:     event.Time.Unix(),
				NodeNum:  event.FromNode,
				RSSI:     event.RSSI,
				SNR:      event.SNR,
				HopLimit: event.HopLimit,
				HopStart: event.HopStart,
			})
		}

		if event.FromNode != 0 {
			if node, ok := a.store.NodeByNum(event.FromNode); ok {
				a.database.SaveNode(&node)
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
			a.database.InsertTraceroute(&tr)
		}
	}
}

func (a *App) runAutoNotifyLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	time.Sleep(45 * time.Second)

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}

		cfg := a.store.MisbehaveConfigSnapshot()
		if !cfg.NotifyEnabled {
			continue
		}
		report := a.store.Misbehaving(nil)
		present := make(map[uint32]bool, len(report.Nodes))
		for _, n := range report.Nodes {
			present[n.NodeNum] = true
			a.store.MarkFlagged(n.NodeNum)
		}
		a.store.UnflagAbsent(present)

		now := time.Now().Unix()
		myNum := a.store.MyNodeNum()
		sentLastHour := a.database.CountMisbehaveNotificationsSince(now - 3600)
		local := a.store.LocalNode()

		for _, n := range report.Nodes {
			if sentLastHour >= cfg.NotifyMaxPerHour {
				break
			}
			if n.NodeNum == 0 || n.NodeNum == myNum {
				continue
			}
			firstAt := a.store.FirstFlaggedAt(n.NodeNum)
			if firstAt == 0 || (now-firstAt) < int64(cfg.NotifyMinFlagAgeSec) {
				continue
			}
			lastSent := a.database.LastMisbehaveNotificationSent(n.NodeNum)
			if lastSent > 0 && (now-lastSent) < int64(cfg.NotifyCooldownHours)*3600 {
				continue
			}

			text := a.store.RenderNotifyTemplate(cfg.NotifyTemplate, n, local.ShortName, local.LongName, cfg)
			rec := store.MisbehaveNotification{
				Time: now, NodeNum: n.NodeNum, NodeID: n.NodeID,
				ShortName: n.ShortName, LongName: n.LongName,
				Reasons: strings.Join(n.Reasons, "; "),
				Text:    text,
			}

			if cfg.NotifyDryRun {
				rec.Status = "dry-run"
				sentLastHour++
				log.Printf("[misb-notify] DRY-RUN to !%08x: %q", n.NodeNum, text)
			} else {
				cr := a.currentReader
				if cr == nil {
					rec.Status = "skipped:not-connected"
				} else if _, err := cr.SendTextMessage(n.NodeNum, text, uint32(cfg.NotifyChannel), uint32(cfg.NotifyHopLimit)); err != nil {
					rec.Status = "failed:" + err.Error()
					log.Printf("[misb-notify] FAILED to !%08x: %v", n.NodeNum, err)
				} else {
					rec.Status = "sent"
					sentLastHour++
					log.Printf("[misb-notify] sent DM to !%08x (%s) reasons=%q", n.NodeNum, n.ShortName, rec.Reasons)
				}
			}
			a.database.InsertMisbehaveNotification(&rec)
		}
	}
}

func (a *App) runSnapshotLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}

		now := time.Now().Unix()
		rh := a.store.RadioHealth()
		if rh.Enabled {
			a.database.SaveRadioSnapshot(now, rh)
		}
		cu := a.store.AggregateChannelUtil()
		if cu.NodesReporting > 0 {
			a.database.InsertChannelSnapshot(db.ChannelSnapshot{
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
}

func (a *App) runLogCompressLoop() {

	runCompress := func() {
		n, saved, err := a.logger.CompressOldLogs(a.cfg.LogCompressDays)
		if err != nil {
			log.Printf("[logger] compress error: %v", err)
			return
		}
		if n > 0 {
			log.Printf("[logger] gzipped %d old log file(s), saved %.1f MB", n, float64(saved)/(1024*1024))
		}
	}

	select {
	case <-a.ctx.Done():
		return
	case <-time.After(10 * time.Minute):
	}
	runCompress()

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			runCompress()
		}
	}
}

func (a *App) runRetentionLoop() {

	runCleanup := func() {
		n, err := a.database.CleanupOld(a.cfg.DBRetention)
		if err != nil {
			log.Printf("[db] retention cleanup error: %v", err)
			return
		}
		if n > 0 {
			log.Printf("[db] retention cleanup: deleted %d rows older than %d days", n, a.cfg.DBRetention)
		}
	}

	select {
	case <-a.ctx.Done():
		return
	case <-time.After(5 * time.Minute):
	}
	runCleanup()

	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			runCleanup()
		}
	}
}

func (a *App) runAvailabilityLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}

		offlines := a.store.ScanOffline()
		for _, tr := range offlines {
			a.database.InsertAvailability(db.AvailabilityEvent{
				Time:    tr.Time,
				NodeNum: tr.NodeNum,
				Event:   tr.Event,
			})
			if a.cfg.Verbose >= 1 {
				log.Printf("[avail] node !%08x went OFFLINE (not heard for 30 min)", tr.NodeNum)
			}
		}
	}
}

func (a *App) runHeartbeatLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}

		cr := a.currentReader
		if cr == nil {
			continue
		}
		if err := sendHeartbeat(cr); err != nil {
			log.Printf("[warn] heartbeat failed: %v", err)
		} else if a.cfg.Verbose >= 2 {
			log.Println("[mesh-reader] heartbeat sent")
		}
	}
}

// ---- helpers ----

func misbDefaultsPath(dbPath string) string {
	dir := ""
	for i := len(dbPath) - 1; i >= 0; i-- {
		if dbPath[i] == '/' || dbPath[i] == '\\' {
			dir = dbPath[:i]
			break
		}
	}
	if dir == "" || dir == "." {
		return "misbehave-defaults.json"
	}
	return dir + "/misbehave-defaults.json"
}

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

// ExitWithPause prints an error and waits for Enter so the console window
// stays open when launched via double-click.
func ExitWithPause(msg string) {
	log.Println(msg)
	fmt.Println("Press Enter to close...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
	os.Exit(1)
}
