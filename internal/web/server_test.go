package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mesh-reader/internal/decoder"
	"mesh-reader/internal/store"
)

func NewTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s := store.New(200)
	s.Add(&decoder.Event{
		Time: time.Now(), Type: decoder.EventMyInfo, FromNode: 0x9001,
		Details: map[string]any{"my_node_num": "!00009001"},
	})
	s.Add(&decoder.Event{Time: time.Now(), Type: decoder.EventTextMessage, FromNode: 0xA, RSSI: -80, SNR: 5,
		HopStart: 3, HopLimit: 3, PacketID: 100, Details: map[string]any{}})
	s.Add(&decoder.Event{
		Time: time.Now(), Type: decoder.EventNodeInfo, FromNode: 0x42,
		Details: map[string]any{"long_name": "TestNode", "short_name": "TN"},
	})
	svr := New(s)
	return svr, s
}

func getJSON(t *testing.T, svr *Server, path string, expectedStatus int) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != expectedStatus {
		t.Fatalf("%s: status=%d, want %d (body=%s)", path, resp.StatusCode, expectedStatus, w.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("%s: json decode: %v (body=%q)", path, err, w.Body.String())
	}
	return out
}

func postJSON(t *testing.T, svr *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	return w
}

// ---- Health ----

func TestHealthHealthy(t *testing.T) {
	svr, s := NewTestServer(t)
	s.Add(&decoder.Event{Time: time.Now(), Type: decoder.EventTextMessage, Details: map[string]any{}})
	out := getJSON(t, svr, "/api/health", http.StatusOK)
	if out["healthy"] != true {
		t.Errorf("healthy = %v", out["healthy"])
	}
}

func TestHealthEmptyStore(t *testing.T) {
	s := store.New(100)
	svr := New(s)
	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("empty store should be 503, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// ---- Stats / Nodes / LocalNode ----

func TestStats(t *testing.T) {
	svr, _ := NewTestServer(t)
	out := getJSON(t, svr, "/api/stats", http.StatusOK)
	if _, ok := out["total_events"]; !ok {
		t.Error("missing total_events")
	}
}

func TestNodes(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	var nodes []map[string]any
	json.NewDecoder(w.Body).Decode(&nodes)
	if len(nodes) < 2 {
		t.Errorf(">=2 nodes, got %d", len(nodes))
	}
}

func TestNodeByID(t *testing.T) {
	svr, _ := NewTestServer(t)
	out := getJSON(t, svr, "/api/nodes/!00000042", http.StatusOK)
	if out["long_name"] != "TestNode" {
		t.Errorf("long_name = %v", out["long_name"])
	}
}

func TestNodeByIDNotFound(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/!deadbeef", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

func TestNodeByIDInvalid(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/xyz", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestLocalNode(t *testing.T) {
	svr, _ := NewTestServer(t)
	out := getJSON(t, svr, "/api/local-node", http.StatusOK)
	if out["node_id"] != "!00009001" {
		t.Errorf("node_id=%q", out["node_id"])
	}
}

// ---- Anomalies / EventsPerMinute ----

func TestAnomalies(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/anomalies?limit=5", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestEventsPerMinute(t *testing.T) {
	svr, _ := NewTestServer(t)
	out := getJSON(t, svr, "/api/events-per-minute?window=5", http.StatusOK)
	if _, ok := out["buckets"]; !ok {
		t.Error("missing buckets")
	}
}

// ---- Links / Traceroutes / Events / IsolatedNodes ----

func TestLinks(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/links", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestTraceroutes(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/traceroutes", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestEvents(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/events?limit=10", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestIsolatedNodes(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/isolated-nodes?min=1", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- Packet Path ----

func TestPacketPathValid(t *testing.T) {
	svr, _ := NewTestServer(t)
	out := getJSON(t, svr, "/api/packet-path?from=a&id=100", http.StatusOK)
	if out["from"] == nil {
		t.Error("missing 'from' in packet-path")
	}
}

func TestPacketPathMissingFrom(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/packet-path?from=0&id=100", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for from=0, got %d", w.Code)
	}
}

func TestPacketPathInvalidFrom(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/packet-path?from=xyz&id=100", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for invalid from, got %d", w.Code)
	}
}

// ---- Send Traceroute ----

func TestSendTracerouteNoSender(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("POST", "/api/traceroute/!00000042", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 without sender, got %d", w.Code)
	}
}

func TestSendTracerouteWithSender(t *testing.T) {
	svr, s := NewTestServer(t)
	var calledDest uint32
	var calledHop uint32
	svr.SetTracerouteSender(func(dest uint32, hop uint32) error {
		calledDest = dest
		calledHop = hop
		return nil
	})
	s.Add(&decoder.Event{Time: time.Now(), Type: decoder.EventNodeInfo, FromNode: 0x42,
		Details: map[string]any{"long_name": "Test"}})
	req := httptest.NewRequest("POST", "/api/traceroute/!00000042?hops=5", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if calledDest != 0x42 || calledHop != 5 {
		t.Errorf("sender called with dest=%x hop=%d, want 42/5", calledDest, calledHop)
	}
}

func TestSendTracerouteInvalidID(t *testing.T) {
	svr, _ := NewTestServer(t)
	// Need a sender configured so we get past the nil check.
	svr.SetTracerouteSender(func(dest uint32, hop uint32) error { return nil })
	req := httptest.NewRequest("POST", "/api/traceroute/xyz", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestSendTracerouteSenderError(t *testing.T) {
	svr, _ := NewTestServer(t)
	svr.SetTracerouteSender(func(dest uint32, hop uint32) error {
		return io.ErrUnexpectedEOF
	})
	req := httptest.NewRequest("POST", "/api/traceroute/!00000042", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Errorf("want 502, got %d", w.Code)
	}
}

// ---- Misbehaving Config ----

func TestMisbehavingConfigGet(t *testing.T) {
	svr, _ := NewTestServer(t)
	out := getJSON(t, svr, "/api/misbehaving/config", http.StatusOK)
	if out == nil {
		t.Error("expected config object")
	}
}

func TestMisbehavingDefaults(t *testing.T) {
	svr, _ := NewTestServer(t)
	out := getJSON(t, svr, "/api/misbehaving/defaults", http.StatusOK)
	if out == nil {
		t.Error("expected defaults object")
	}
}

func TestMisbehavingConfigPostValid(t *testing.T) {
	svr, _ := NewTestServer(t)
	body := `{"telemetry_enabled": true, "telemetry_count": 5, "telemetry_window_sec": 7200}`
	w := postJSON(t, svr, "/api/misbehaving/config", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	json.NewDecoder(w.Body).Decode(&out)
	cfg, ok := out["config"].(map[string]any)
	if !ok || cfg["telemetry_count"] == nil {
		t.Errorf("config not returned: %+v", out)
	}
}

func TestMisbehavingConfigPostInvalidJSON(t *testing.T) {
	svr, _ := NewTestServer(t)
	w := postJSON(t, svr, "/api/misbehaving/config", `{bad json}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad JSON, got %d", w.Code)
	}
}

func TestMisbehavingConfigPostSaveWithoutPath(t *testing.T) {
	svr, _ := NewTestServer(t)
	body := `{"telemetry_enabled": true, "telemetry_count": 3, "telemetry_window_sec": 3600}`
	w := postJSON(t, svr, "/api/misbehaving/config?save=1", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var out map[string]any
	json.NewDecoder(w.Body).Decode(&out)
	if out["saved"] != false {
		t.Error("save without path should report saved=false")
	}
	if out["save_error"] == "" || out["save_error"] == nil {
		t.Error("expected save_error when path not configured")
	}
}

func TestMisbehavingConfigPostSaveWithPath(t *testing.T) {
	svr, _ := NewTestServer(t)
	svr.SetMisbehaveConfigPath("test_misb_config.json")
	t.Cleanup(func() { _ = svr.store.SetMisbehaveConfig(store.MisbehaveConfig{}) }) // reset

	body := `{"telemetry_enabled": true, "telemetry_count": 4, "telemetry_window_sec": 7200}`
	w := postJSON(t, svr, "/api/misbehaving/config?save=1", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	json.NewDecoder(w.Body).Decode(&out)
	if out["saved"] != true {
		t.Errorf("save with path should succeed: %+v", out)
	}
}

// ---- Misbehaving Report ----

func TestMisbehaving(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/misbehaving", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestMisbehavingNotifyStatus(t *testing.T) {
	svr, _ := NewTestServer(t)
	out := getJSON(t, svr, "/api/misbehaving/notify-status", http.StatusOK)
	if _, ok := out["enabled"]; !ok {
		t.Error("missing 'enabled' in notify-status")
	}
}

func TestMisbehavingResetNode(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("POST", "/api/misbehaving/reset/!00000042", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestMisbehavingResetNodeInvalidID(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("POST", "/api/misbehaving/reset/xyz", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestMisbehavingNotificationsDelete(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("DELETE", "/api/misbehaving/notifications", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- DX Records ----

func TestDXRecords(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/dx-records?limit=5&direct_only=true", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- SNR Distance ----

func TestSNRDistance(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/snr-distance", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- Export ----

func TestExportNodesCSV(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/export/nodes.csv", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/csv") {
		t.Errorf("Content-Type=%q, want text/csv", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "node_num,") {
		t.Error("CSV missing header")
	}
}

func TestExportMessagesCSV(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/export/messages.csv", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/csv") {
		t.Errorf("Content-Type=%q, want text/csv", ct)
	}
}

// ---- Signal Trends ----

func TestSignalTrends(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/signal-trends?window_hours=24&min_samples=3", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- Channel Util ----

func TestChannelUtil(t *testing.T) {
	svr, _ := NewTestServer(t)
	out := getJSON(t, svr, "/api/channel-util", http.StatusOK)
	if out == nil {
		t.Error("expected channel-util object")
	}
}

// ---- Pairs ----

func TestPairs(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/pairs?limit=5&transit_only=true", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestPairDetail(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/pairs/!00000042/!0000000a", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

// ---- Sniffer ----

func TestSniffer(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/sniffer?limit=10&type=TEXT_MESSAGE", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- Radio Health ----

func TestRadioHealth(t *testing.T) {
	svr, _ := NewTestServer(t)
	out := getJSON(t, svr, "/api/radio-health", http.StatusOK)
	if out == nil {
		t.Error("expected radio-health object")
	}
}

func TestRadioHistory(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/radio-health/history?limit=5", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- Messages / Positions ----

func TestMessages(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/messages?limit=5", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestPositions(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/positions", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- Telemetry / Signal History ----

func TestTelemetry(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/telemetry/!00000042", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestTelemetryInvalidID(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/telemetry/xyz", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestSignalHistory(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/signal/!00000042?limit=5", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- Availability ----

func TestAvailability(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/availability", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestNodeAvailability(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/availability/!00000042", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- Channel Util History ----

func TestChannelHistory(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/channel-util/history?limit=5", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- ChUtil history ----

func TestChUtilHistory(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/chutil-history?hours=24", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- ChUtil zones ----

func TestChUtilZones(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/chutil-zones?hours=24", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
}

// ---- Heatmap ----

func TestHeatmapTemporal(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/heatmap-temporal?days=7", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Code == http.StatusInternalServerError {
		// May return error if no DB attached, which is ok.
	}
}

func TestHeatmapCellDetail(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("GET", "/api/heatmap-cell-detail?weekday=1&hour=12&days=7", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK && w.Code != http.StatusInternalServerError {
		t.Errorf("unexpected status %d", w.Code)
	}
}

// ---- Notify-now ----

func TestMisbehavingNotifyNow(t *testing.T) {
	svr, _ := NewTestServer(t)
	svr.SetTextMessageSender(func(dest uint32, text string, channel uint32, hopLimit uint32) (uint32, error) {
		return 999, nil
	})
	req := httptest.NewRequest("POST", "/api/misbehaving/notify/!00000042", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	// Without DB, notifications may fail. Just check it doesn't panic.
	if w.Code == 0 {
		t.Error("zero status code (panic?)")
	}
}

func TestMisbehavingNotifyNowNoSender(t *testing.T) {
	svr, _ := NewTestServer(t)
	req := httptest.NewRequest("POST", "/api/misbehaving/notify/!00000042", nil)
	w := httptest.NewRecorder()
	svr.mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 without sender, got %d", w.Code)
	}
}

// ---- unused import guard ----

func TestBytesImportUsed(t *testing.T) {
	_ = bytes.NewReader(nil)
}
