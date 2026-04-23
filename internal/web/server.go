package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"mesh-reader/internal/db"
	"mesh-reader/internal/decoder"
	"mesh-reader/internal/store"
)

//go:embed static/*
var staticFS embed.FS

// TracerouteSender sends a TRACEROUTE_APP packet to dest with hopLimit hops.
// main.go provides a closure that locks the reader pointer (which can change
// across reconnects) and calls reader.SendTraceroute.
type TracerouteSender func(dest uint32, hopLimit uint32) error

// Server serves the web dashboard and API.
type Server struct {
	store    *store.Store
	db       *db.DB // optional; nil disables history endpoints
	mux      *http.ServeMux
	sendTR   TracerouteSender // optional; nil disables traceroute on-demand
}

// New creates a Server wired to the given Store.
func New(s *store.Store) *Server {
	return NewWithDB(s, nil)
}

// SetTracerouteSender wires the on-demand TX path. Pass nil to disable.
func (s *Server) SetTracerouteSender(fn TracerouteSender) { s.sendTR = fn }

// NewWithDB creates a Server with access to the DB for history endpoints.
func NewWithDB(s *store.Store, database *db.DB) *Server {
	srv := &Server{store: s, db: database}
	mux := http.NewServeMux()

	// API
	mux.HandleFunc("GET /api/stats", srv.handleStats)
	mux.HandleFunc("GET /api/nodes", srv.handleNodes)
	mux.HandleFunc("GET /api/nodes/{id}", srv.handleNode)
	mux.HandleFunc("GET /api/messages", srv.handleMessages)
	mux.HandleFunc("GET /api/positions", srv.handlePositions)
	mux.HandleFunc("GET /api/telemetry/{id}", srv.handleTelemetry)
	mux.HandleFunc("GET /api/events", srv.handleEvents)
	mux.HandleFunc("GET /api/traceroutes", srv.handleTraceroutes)
	mux.HandleFunc("GET /api/links", srv.handleLinks)
	mux.HandleFunc("GET /api/radio-health", srv.handleRadioHealth)
	mux.HandleFunc("GET /api/radio-health/history", srv.handleRadioHistory)
	mux.HandleFunc("GET /api/signal/{id}", srv.handleSignalHistory)
	mux.HandleFunc("GET /api/availability", srv.handleAvailability)
	mux.HandleFunc("GET /api/availability/{id}", srv.handleNodeAvailability)
	mux.HandleFunc("GET /api/channel-util", srv.handleChannelUtil)
	mux.HandleFunc("GET /api/channel-util/history", srv.handleChannelHistory)
	mux.HandleFunc("GET /api/health", srv.handleHealth)
	mux.HandleFunc("GET /api/local-node", srv.handleLocalNode)
	mux.HandleFunc("GET /api/events-per-minute", srv.handleEventsPerMinute)
	mux.HandleFunc("GET /api/export/nodes.csv", srv.handleExportNodes)
	mux.HandleFunc("GET /api/export/messages.csv", srv.handleExportMessages)
	mux.HandleFunc("GET /api/isolated-nodes", srv.handleIsolatedNodes)
	mux.HandleFunc("GET /api/snr-distance", srv.handleSNRDistance)
	mux.HandleFunc("GET /api/signal-trends", srv.handleSignalTrends)
	mux.HandleFunc("GET /api/heatmap-temporal", srv.handleHeatmapTemporal)
	mux.HandleFunc("GET /api/heatmap-cell-detail", srv.handleHeatmapCellDetail)
	mux.HandleFunc("GET /api/chutil-zones", srv.handleChUtilZones)
	mux.HandleFunc("GET /api/chutil-history", srv.handleChUtilHistory)
	mux.HandleFunc("GET /api/anomalies", srv.handleAnomalies)
	mux.HandleFunc("GET /api/dx-records", srv.handleDXRecords)
	mux.HandleFunc("GET /api/packet-path", srv.handlePacketPath)
	mux.HandleFunc("POST /api/traceroute/{id}", srv.handleSendTraceroute)
	// WS/SSE removed — dashboard uses manual refresh via REST API

	// Static frontend. When MESH_WEB_DEV is set to a non-empty value, serve
	// directly from internal/web/static on disk so edits to HTML/JS/CSS are
	// visible after a browser refresh without a rebuild+restart cycle.
	// Default (empty) uses the embedded FS — production path.
	if devDir := os.Getenv("MESH_WEB_DEV"); devDir != "" {
		// Caller may set MESH_WEB_DEV=1 (use default path) or pass an
		// explicit path such as MESH_WEB_DEV=internal/web/static.
		path := devDir
		if path == "1" || path == "true" {
			path = "internal/web/static"
		}
		log.Printf("[web] DEV MODE — serving static files from %q (not the embedded FS)", path)
		mux.Handle("GET /", noCacheMiddleware(http.FileServer(http.Dir(path))))
	} else {
		staticSub, _ := fs.Sub(staticFS, "static")
		mux.Handle("GET /", http.FileServer(http.FS(staticSub)))
	}

	srv.mux = mux
	return srv
}

// ListenAndServe starts the HTTP server on addr (e.g. ":8080").
func (s *Server) ListenAndServe(addr string) error {
	log.Printf("[web] dashboard at http://localhost%s/", addr)
	return http.ListenAndServe(addr, s.mux)
}

// handleLocalNode returns everything known about the directly connected node:
// identity, firmware version, LoRa config and hardware capabilities.
func (s *Server) handleLocalNode(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.LocalNode())
}

// noCacheMiddleware forces browsers to revalidate static assets on every
// request. Used only in dev mode so edits to HTML/JS/CSS take effect on F5
// without needing Ctrl+Shift+R to bust the cache.
func noCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		next.ServeHTTP(w, r)
	})
}

// --- API handlers ---

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Stats())
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Nodes())
}

func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	nodeNum, err := parseNodeID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid node id", http.StatusBadRequest)
		return
	}
	nodes := s.store.Nodes()
	for _, n := range nodes {
		if n.NodeNum == nodeNum {
			writeJSON(w, n)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	writeJSON(w, s.toWebEvents(s.store.Messages(limit)))
}

func (s *Server) handlePositions(w http.ResponseWriter, r *http.Request) {
	nodes := s.store.Nodes()
	type pos struct {
		NodeNum  uint32  `json:"node_num"`
		ID       string  `json:"id"`
		Name     string  `json:"name"`
		Lat      float64 `json:"lat"`
		Lon      float64 `json:"lon"`
		Altitude int32   `json:"altitude"`
	}
	out := make([]pos, 0)
	for _, n := range nodes {
		if !n.HasPos {
			continue
		}
		name := n.LongName
		if name == "" {
			name = n.ID
		}
		out = append(out, pos{
			NodeNum:  n.NodeNum,
			ID:       n.ID,
			Name:     name,
			Lat:      n.Lat,
			Lon:      n.Lon,
			Altitude: n.Altitude,
		})
	}
	writeJSON(w, out)
}

func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	nodeNum, err := parseNodeID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid node id", http.StatusBadRequest)
		return
	}
	limit := queryInt(r, "limit", 100)
	writeJSON(w, s.toWebEvents(s.store.TelemetryHistory(nodeNum, limit)))
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	filterType := decoder.EventType(r.URL.Query().Get("type"))
	writeJSON(w, s.toWebEvents(s.store.RecentEvents(limit, filterType)))
}

func (s *Server) handleTraceroutes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Traceroutes())
}

func (s *Server) handleLinks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Links())
}

func (s *Server) handleRadioHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.RadioHealth())
}

func (s *Server) handleRadioHistory(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, []any{})
		return
	}
	limit := queryInt(r, "limit", 288)
	writeJSON(w, s.db.LoadRadioSnapshots(limit))
}

func (s *Server) handleSignalHistory(w http.ResponseWriter, r *http.Request) {
	nodeNum, err := parseNodeID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid node id", http.StatusBadRequest)
		return
	}
	if s.db == nil {
		writeJSON(w, []any{})
		return
	}
	limit := queryInt(r, "limit", 200)
	writeJSON(w, s.db.LoadSignalHistory(nodeNum, limit))
}

func (s *Server) handleAvailability(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, []any{})
		return
	}
	limit := queryInt(r, "limit", 2000)
	writeJSON(w, s.db.LoadAllAvailability(limit))
}

func (s *Server) handleNodeAvailability(w http.ResponseWriter, r *http.Request) {
	nodeNum, err := parseNodeID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid node id", http.StatusBadRequest)
		return
	}
	if s.db == nil {
		writeJSON(w, []any{})
		return
	}
	limit := queryInt(r, "limit", 500)
	writeJSON(w, s.db.LoadAvailability(nodeNum, limit))
}

func (s *Server) handleChannelUtil(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.AggregateChannelUtil())
}

func (s *Server) handleChannelHistory(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, []any{})
		return
	}
	limit := queryInt(r, "limit", 288)
	writeJSON(w, s.db.LoadChannelSnapshots(limit))
}

// --- helpers ---

type webEvent struct {
	Time           string         `json:"time"`
	Type           string         `json:"type"`
	From           string         `json:"from"`
	FromNum        uint32         `json:"from_num,omitempty"`
	To             string         `json:"to"`
	RSSI           int32          `json:"rssi"`
	SNR            float32        `json:"snr"`
	HopLimit       uint32         `json:"hop_limit"`
	HopStart       uint32         `json:"hop_start"`
	PacketID       uint32         `json:"packet_id,omitempty"`
	RelayNode      string         `json:"relay_node,omitempty"`
	RelayCandidates []string      `json:"relay_candidates,omitempty"`
	ViaMqtt        bool           `json:"via_mqtt,omitempty"`
	Details        map[string]any `json:"details"`
}

// toWebEventWithStore converts an event using the store to resolve relay nodes.
func toWebEventWithStore(e *decoder.Event, st *store.Store) webEvent {
	w := webEvent{
		Time:     e.Time.Format(time.RFC3339),
		Type:     string(e.Type),
		From:     nodeIDStr(e.FromNode),
		FromNum:  e.FromNode,
		To:       nodeIDStr(e.ToNode),
		RSSI:     e.RSSI,
		SNR:      e.SNR,
		HopLimit: e.HopLimit,
		HopStart: e.HopStart,
		PacketID: e.PacketID,
		ViaMqtt:  e.ViaMqtt,
		Details:  e.Details,
	}
	if e.RelayNode != 0 {
		w.RelayNode = relayNodeStr(e.RelayNode)
		if st != nil {
			matches := st.ResolveRelayNodes(e.RelayNode)
			if len(matches) == 1 {
				w.RelayNode = nodeIDStr(matches[0])
			} else if len(matches) > 1 {
				// Multiple candidates: show the short hint and list all
				candidates := make([]string, len(matches))
				for i, m := range matches {
					candidates[i] = nodeIDStr(m)
				}
				w.RelayCandidates = candidates
			}
		}
	}
	return w
}

func toWebEvent(e *decoder.Event) webEvent {
	return toWebEventWithStore(e, nil)
}

// relayNodeStr converts the single-byte relay node hint to a display string.
// RelayNode from Meshtastic is only the last byte of the node number.
func relayNodeStr(lastByte uint32) string {
	if lastByte == 0 {
		return ""
	}
	return fmt.Sprintf("..%02x", lastByte&0xFF)
}

func (s *Server) toWebEvents(events []*decoder.Event) []webEvent {
	out := make([]webEvent, len(events))
	for i, e := range events {
		out[i] = toWebEventWithStore(e, s.store)
	}
	return out
}

// handleSignalTrends returns nodes whose mean SNR/RSSI is degrading over time.
//
// Parameters (query string):
//   window_hours (default 24) — size of each comparison window
//   min_samples  (default 5)  — required samples in BOTH windows to be considered
//   only_bad     (default true) — if true, return only nodes with delta_snr <= -2 dB
//
// Classification (per row):
//   severe:      delta_snr <= -6 dB  (link in freefall)
//   significant: delta_snr <= -4 dB
//   minor:       delta_snr <= -2 dB
//   stable:      delta_snr in (-2, +2) dB
//   improving:   delta_snr >= +2 dB
func (s *Server) handleSignalTrends(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, []any{})
		return
	}
	windowHours := queryInt(r, "window_hours", 24)
	minSamples := queryInt(r, "min_samples", 5)
	onlyBad := r.URL.Query().Get("only_bad") != "false"

	trends, err := s.db.ComputeSignalTrends(int64(windowHours)*3600, minSamples)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type row struct {
		NodeNum        uint32  `json:"node_num"`
		ID             string  `json:"id"`
		LongName       string  `json:"long_name"`
		ShortName      string  `json:"short_name"`
		RecentMeanSNR  float64 `json:"recent_mean_snr"`
		OlderMeanSNR   float64 `json:"older_mean_snr"`
		RecentMeanRSSI float64 `json:"recent_mean_rssi"`
		OlderMeanRSSI  float64 `json:"older_mean_rssi"`
		RecentCount    int     `json:"recent_count"`
		OlderCount     int     `json:"older_count"`
		DeltaSNR       float64 `json:"delta_snr"`
		DeltaRSSI      float64 `json:"delta_rssi"`
		LastSampleAt   int64   `json:"last_sample_at"`
		Severity       string  `json:"severity"`
	}
	classify := func(d float64) string {
		switch {
		case d <= -6: return "severe"
		case d <= -4: return "significant"
		case d <= -2: return "minor"
		case d >=  2: return "improving"
		default:      return "stable"
		}
	}
	out := make([]row, 0, len(trends))
	for _, t := range trends {
		sev := classify(t.DeltaSNR)
		if onlyBad && !(sev == "severe" || sev == "significant" || sev == "minor") {
			continue
		}
		r := row{
			NodeNum: t.NodeNum, RecentMeanSNR: t.RecentMeanSNR, OlderMeanSNR: t.OlderMeanSNR,
			RecentMeanRSSI: t.RecentMeanRSSI, OlderMeanRSSI: t.OlderMeanRSSI,
			RecentCount: t.RecentCount, OlderCount: t.OlderCount,
			DeltaSNR: t.DeltaSNR, DeltaRSSI: t.DeltaRSSI,
			LastSampleAt: t.LastSampleAt, Severity: sev,
		}
		if n, ok := s.store.NodeByNum(t.NodeNum); ok {
			r.ID = n.ID
			r.LongName = n.LongName
			r.ShortName = n.ShortName
		}
		out = append(out, r)
	}
	// Sort: worst first
	sortBy := func(a, b row) bool { return a.DeltaSNR < b.DeltaSNR }
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && sortBy(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleIsolatedNodes(w http.ResponseWriter, r *http.Request) {
	min := queryInt(r, "min_packets", 3)
	writeJSON(w, s.store.IsolatedNodesReport(min))
}

// handleSNRDistance returns scatter-plot data: for each node with a known
// position, compute its distance in km from our local node and pair with
// the node's best observed SNR and RSSI. Clients can plot SNR-vs-distance
// to visually spot under-performing links (e.g. a node that should be close
// but has terrible SNR = likely antenna/RF issue).
func (s *Server) handleSNRDistance(w http.ResponseWriter, r *http.Request) {
	type point struct {
		NodeNum   uint32  `json:"node_num"`
		Name      string  `json:"name"`
		DistanceKm float64 `json:"distance_km"`
		RSSI      int32   `json:"rssi"`
		SNR       float32 `json:"snr"`
		HopStart  uint32  `json:"hop_start"`
		HopLimit  uint32  `json:"hop_limit"`
	}
	nodes := s.store.Nodes()
	// Find our local node (has position) by looking for MyNode
	var myLat, myLon float64
	var haveMe bool
	myNum := s.store.MyNodeNum()
	for _, n := range nodes {
		if n.NodeNum == myNum && n.HasPos {
			myLat, myLon = n.Lat, n.Lon
			haveMe = true
			break
		}
	}
	if !haveMe {
		// Fallback: pick first node with position as anchor
		for _, n := range nodes {
			if n.HasPos {
				myLat, myLon = n.Lat, n.Lon
				haveMe = true
				break
			}
		}
	}
	out := make([]point, 0)
	if !haveMe {
		writeJSON(w, out)
		return
	}
	for _, n := range nodes {
		if !n.HasPos || n.NodeNum == myNum {
			continue
		}
		if n.RSSI == 0 && n.SNR == 0 {
			continue
		}
		name := n.LongName
		if name == "" {
			name = n.ShortName
		}
		if name == "" {
			name = n.ID
		}
		out = append(out, point{
			NodeNum:    n.NodeNum,
			Name:       name,
			DistanceKm: haversineKm(myLat, myLon, n.Lat, n.Lon),
			RSSI:       n.RSSI,
			SNR:        n.SNR,
			HopStart:   n.HopLimit,
		})
	}
	writeJSON(w, out)
}

// haversineKm returns great-circle distance between two (lat,lon) points in km.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	toRad := func(d float64) float64 { return d * math.Pi / 180.0 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	s1 := math.Sin(dLat / 2)
	s2 := math.Sin(dLon / 2)
	a := s1*s1 + math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*s2*s2
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

// handleHealth returns a simple healthcheck:
// - status 200 OK if we received at least one event in the last 15 min
// - status 503 SERVICE_UNAVAILABLE otherwise (useful for systemd/docker/nagios)
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	last := s.store.LastEventAt()
	now := time.Now()
	stats := s.store.Stats()
	healthy := !last.IsZero() && now.Sub(last) < 15*time.Minute
	payload := map[string]any{
		"status":         map[bool]string{true: "ok", false: "stale"}[healthy],
		"healthy":        healthy,
		"uptime_seconds": stats.UptimeSeconds,
		"total_events":   stats.TotalEvents,
		"total_nodes":    stats.TotalNodes,
		"active_nodes":   stats.ActiveNodes,
	}
	if !last.IsZero() {
		payload["last_event_at"] = last.Format(time.RFC3339)
		payload["seconds_since_last_event"] = int64(now.Sub(last).Seconds())
	}
	w.Header().Set("Content-Type", "application/json")
	if !healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(payload)
}

func (s *Server) handleEventsPerMinute(w http.ResponseWriter, r *http.Request) {
	win := queryInt(r, "window", 60)
	if win > 360 {
		win = 360
	}
	writeJSON(w, map[string]any{
		"window_minutes": win,
		"buckets":        s.store.EventsPerMinute(win),
	})
}

func (s *Server) handleExportNodes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="nodes.csv"`)
	fmt.Fprintln(w, "node_num,id,long_name,short_name,hw_model,last_heard_utc,lat,lon,has_pos,altitude,battery,voltage,chan_util,air_util,temperature,humidity,pressure,rssi,snr,hop_limit")
	for _, n := range s.store.Nodes() {
		lh := ""
		if n.LastHeard > 0 {
			lh = time.Unix(n.LastHeard, 0).UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%d,%q,%q,%q,%q,%s,%.6f,%.6f,%t,%d,%d,%.2f,%.1f,%.1f,%.1f,%.1f,%.1f,%d,%.1f,%d\n",
			n.NodeNum, n.ID, n.LongName, n.ShortName, n.HWModel, lh,
			n.Lat, n.Lon, n.HasPos, n.Altitude,
			n.BatteryLevel, n.Voltage, n.ChannelUtilization, n.AirUtilTx,
			n.Temperature, n.Humidity, n.BarometricPressure,
			n.RSSI, n.SNR, n.HopLimit,
		)
	}
}

func (s *Server) handleExportMessages(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 10000)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="messages.csv"`)
	fmt.Fprintln(w, "time,from,to,rssi,snr,message")
	for _, e := range s.store.Messages(limit) {
		text, _ := e.Details["text"].(string)
		// CSV-escape: wrap text in quotes, double any existing quotes
		text = `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
		fmt.Fprintf(w, "%s,%s,%s,%d,%.1f,%s\n",
			e.Time.Format(time.RFC3339),
			nodeIDStr(e.FromNode),
			nodeIDStr(e.ToNode),
			e.RSSI, e.SNR, text,
		)
	}
}

func nodeIDStr(n uint32) string {
	if n == 0 {
		return ""
	}
	if n == 0xFFFFFFFF {
		return "^all"
	}
	return fmt.Sprintf("!%08x", n)
}

func parseNodeID(s string) (uint32, error) {
	// Tolerate the Meshtastic "!" prefix ("!91fda3df" → "91fda3df") so
	// URLs pasted from logs or copied from the UI work directly.
	s = strings.TrimPrefix(s, "!")
	n, err := strconv.ParseUint(s, 16, 32)
	return uint32(n), err
}

func queryInt(r *http.Request, key string, defaultVal int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 {
		return defaultVal
	}
	return v
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// handleHeatmapTemporal returns event counts by (weekday, hour) for the last
// `days` days. Used to render a 7×24 heatmap on the Overview tab.
func (s *Server) handleHeatmapTemporal(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, map[string]any{"days": 0, "cells": []any{}})
		return
	}
	days := queryInt(r, "days", 30)
	if days > 365 {
		days = 365
	}
	cells, err := s.db.TemporalHeatmap(days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"days":  days,
		"cells": cells,
	})
}

// handleHeatmapCellDetail returns the drill-down payload for one heatmap
// cell: top nodes, event type breakdown, signal stats, recent samples. The
// client calls this when the user clicks a (weekday, hour) square.
func (s *Server) handleHeatmapCellDetail(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, map[string]any{"total": 0, "top_nodes": []any{}, "types": []any{}, "samples": []any{}})
		return
	}
	weekday := queryInt(r, "weekday", -1)
	hour := queryInt(r, "hour", -1)
	days := queryInt(r, "days", 30)
	if days > 365 {
		days = 365
	}
	det, err := s.db.HeatmapCellDetailFor(weekday, hour, days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Enrich top-nodes with resolved labels from the in-memory node index.
	for i := range det.TopNodes {
		if n, ok := s.store.NodeByNum(det.TopNodes[i].NodeNum); ok {
			label := n.LongName
			if label == "" {
				label = n.ShortName
			}
			if label == "" {
				label = n.ID
			}
			det.TopNodes[i].NodeLabel = label
		}
		if det.TopNodes[i].NodeLabel == "" {
			det.TopNodes[i].NodeLabel = fmt.Sprintf("!%x", det.TopNodes[i].NodeNum)
		}
	}
	writeJSON(w, det)
}

// chutilZoneNode decorates a db.ChUtilNodeStat with position + name so the
// map layer can render everything from one payload.
type chutilZoneNode struct {
	NodeNum        uint32  `json:"node_num"`
	Name           string  `json:"name"`
	ShortName      string  `json:"short_name"`
	Lat            float64 `json:"lat"`
	Lon            float64 `json:"lon"`
	Current        float64 `json:"current"`
	CurrentAgeMin  int64   `json:"current_age_min"`
	Avg            float64 `json:"avg"`
	P50            float64 `json:"p50"`
	P95            float64 `json:"p95"`
	Max            float64 `json:"max"`
	PeakTime       int64   `json:"peak_time"`
	Samples        int     `json:"samples"`
	AirAvg         float64 `json:"air_avg"`
	AirMax         float64 `json:"air_max"`
	LastSampleTime int64   `json:"last_sample_time"`
}

// handleChUtilZones returns per-node channel utilization statistics over a
// window, enriched with node position and label. Only nodes with a known
// position are returned — a map layer can't render what doesn't have lat/lon.
func (s *Server) handleChUtilZones(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, map[string]any{"window_hours": 0, "nodes": []any{}})
		return
	}
	hours := queryInt(r, "window", 24)
	if hours < 1 {
		hours = 1
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	stats := s.db.ChUtilZones(hours)
	out := make([]chutilZoneNode, 0, len(stats))
	var globalMax float64
	var peakNum uint32
	var peakTime int64
	var sum float64
	var sumN int
	for _, st := range stats {
		n, ok := s.store.NodeByNum(st.NodeNum)
		if !ok || !n.HasPos {
			continue
		}
		label := n.LongName
		if label == "" {
			label = n.ShortName
		}
		if label == "" {
			label = n.ID
		}
		if label == "" {
			label = fmt.Sprintf("!%08x", st.NodeNum)
		}
		out = append(out, chutilZoneNode{
			NodeNum:        st.NodeNum,
			Name:           label,
			ShortName:      n.ShortName,
			Lat:            n.Lat,
			Lon:            n.Lon,
			Current:        st.Current,
			CurrentAgeMin:  st.CurrentAgeMin,
			Avg:            st.Avg,
			P50:            st.P50,
			P95:            st.P95,
			Max:            st.Max,
			PeakTime:       st.PeakTime,
			Samples:        st.Samples,
			AirAvg:         st.AirAvg,
			AirMax:         st.AirMax,
			LastSampleTime: st.LastSampleTime,
		})
		if st.Max > globalMax {
			globalMax = st.Max
			peakNum = st.NodeNum
			peakTime = st.PeakTime
		}
		sum += st.Avg
		sumN++
	}
	var netAvg float64
	if sumN > 0 {
		netAvg = sum / float64(sumN)
	}
	writeJSON(w, map[string]any{
		"window_hours":   hours,
		"nodes":          out,
		"network_avg":    netAvg,
		"network_max":    globalMax,
		"network_peak_node": peakNum,
		"network_peak_time": peakTime,
		"reporting":      len(out),
	})
}

// handleChUtilHistory returns a node's ChUtil samples over a window so the
// frontend can draw a sparkline inside the map popup.
func (s *Server) handleChUtilHistory(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, []any{})
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	num, err := parseNodeID(id)
	if err != nil || num == 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	hours := queryInt(r, "hours", 24)
	if hours < 1 {
		hours = 1
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	writeJSON(w, s.db.ChUtilHistory(num, hours))
}

// handleAnomalies returns the most recent flagged anomalies (newest first).
func (s *Server) handleAnomalies(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	writeJSON(w, s.store.Anomalies(limit))
}

// handleDXRecords returns the long-distance reception leaderboard.
// ?direct_only=true   only count direct (hop=0) receptions
// ?limit=N            cap the response (default 25)
func (s *Server) handleDXRecords(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 25)
	directOnly := r.URL.Query().Get("direct_only") == "true"
	writeJSON(w, s.store.DXLeaderboard(limit, directOnly))
}

// handlePacketPath returns information needed to reconstruct the path a
// given packet took to reach us. Inputs: from (node id, hex, with or without
// '!' prefix) and id (decimal packet id). Returns the matching reception(s)
// from the in-memory ring buffer plus traceroutes that involve the same node
// (so the operator can correlate the relay hint with a known route).
func (s *Server) handlePacketPath(w http.ResponseWriter, r *http.Request) {
	fromStr := strings.TrimPrefix(r.URL.Query().Get("from"), "!")
	idStr := r.URL.Query().Get("id")
	from, err := parseNodeID(fromStr)
	if err != nil || from == 0 {
		http.Error(w, "invalid 'from' parameter", http.StatusBadRequest)
		return
	}
	type response struct {
		From        uint32                   `json:"from"`
		FromName    string                   `json:"from_name"`
		PacketID    uint32                   `json:"packet_id"`
		Receptions  []webEvent               `json:"receptions"`
		Traceroutes []store.TracerouteRecord `json:"traceroutes"`
	}
	resp := response{From: from}
	if n, ok := s.store.NodeByNum(from); ok {
		resp.FromName = n.LongName
		if resp.FromName == "" {
			resp.FromName = n.ShortName
		}
		if resp.FromName == "" {
			resp.FromName = n.ID
		}
	}
	if idStr != "" {
		if pid, err := strconv.ParseUint(idStr, 10, 32); err == nil {
			resp.PacketID = uint32(pid)
			// Walk the ring buffer for matching receptions (typically 1).
			events := s.store.RecentEvents(10000, "")
			for _, ev := range events {
				if ev.FromNode == from && ev.PacketID == uint32(pid) {
					resp.Receptions = append(resp.Receptions, toWebEventWithStore(ev, s.store))
				}
			}
		}
	}
	// Recent traceroutes involving this node — useful as path hints.
	all := s.store.Traceroutes()
	var related []store.TracerouteRecord
	for _, tr := range all {
		if tr.From == from || tr.To == from {
			related = append(related, tr)
		}
	}
	if len(related) > 5 {
		related = related[len(related)-5:]
	}
	resp.Traceroutes = related
	writeJSON(w, resp)
}

// handleSendTraceroute initiates an on-demand TRACEROUTE_APP toward the given
// node. The actual response (RouteDiscovery) will arrive asynchronously and
// be persisted as an EventTraceroute by the normal pipeline.
func (s *Server) handleSendTraceroute(w http.ResponseWriter, r *http.Request) {
	if s.sendTR == nil {
		http.Error(w, "traceroute sender not configured", http.StatusServiceUnavailable)
		return
	}
	idStr := strings.TrimPrefix(r.PathValue("id"), "!")
	dest, err := parseNodeID(idStr)
	if err != nil || dest == 0 {
		http.Error(w, "invalid node id", http.StatusBadRequest)
		return
	}
	hop := uint32(queryInt(r, "hops", 7))
	if err := s.sendTR(dest, hop); err != nil {
		http.Error(w, "send failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{
		"status":    "sent",
		"dest":      nodeIDStr(dest),
		"hop_limit": hop,
		"note":      "the response (RouteDiscovery) will appear asynchronously as a Traceroute event",
	})
}
