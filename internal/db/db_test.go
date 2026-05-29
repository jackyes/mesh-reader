package db

import (
	"testing"
	"time"

	"mesh-reader/internal/decoder"
	"mesh-reader/internal/store"
)

// newMemDB opens a new in-memory SQLite for testing.
func newMemDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestOpenMemory(t *testing.T) {
	d := newMemDB(t)
	if d.db == nil {
		t.Fatal("db is nil")
	}
	// Verify tables exist.
	var count int
	if err := d.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='events'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("events table missing: count=%d", count)
	}
}

func TestInsertEventAndLoad(t *testing.T) {
	d := newMemDB(t)
	now := time.Now().UTC()
	ev := &decoder.Event{
		Time:     now,
		Type:     decoder.EventTextMessage,
		FromNode: 0x12345678,
		ToNode:   0xFFFFFFFF,
		RSSI:     -80, SNR: 4.5,
		HopLimit: 3, HopStart: 3, PacketID: 42, Channel: 1,
		ViaMqtt: true, RelayNode: 0xAB,
		Class:   "broadcast",
		Details: map[string]any{"text": "hello"},
	}
	d.InsertEvent(ev)

	loaded := d.LoadRecentEvents(10)
	if len(loaded) != 1 {
		t.Fatalf("expected 1 event loaded, got %d", len(loaded))
	}
	l := loaded[0]
	if l.FromNode != 0x12345678 {
		t.Errorf("FromNode = %x", l.FromNode)
	}
	if l.ViaMqtt != true {
		t.Error("ViaMqtt should be true")
	}
	if l.Class != "broadcast" {
		t.Errorf("Class = %q", l.Class)
	}
	if l.RelayNode != 0xAB {
		t.Errorf("RelayNode = %x", l.RelayNode)
	}
	if l.Channel != 1 {
		t.Errorf("Channel = %d", l.Channel)
	}
	if text, _ := l.Details["text"].(string); text != "hello" {
		t.Errorf("details text = %q", text)
	}
}

func TestInsertEventDefaults(t *testing.T) {
	d := newMemDB(t)
	ev := &decoder.Event{
		Time:     time.Now().UTC(),
		Type:     decoder.EventEncrypted,
		FromNode: 0x1,
		ToNode:   0x2,
		Details:  map[string]any{},
	}
	d.InsertEvent(ev)
	loaded := d.LoadRecentEvents(10)
	if len(loaded) != 1 {
		t.Fatalf("expected 1, got %d", len(loaded))
	}
	// Defaults: ViaMqtt=false, Channel=0, HopStart=0, Class=""
	if loaded[0].ViaMqtt {
		t.Error("ViaMqtt default should be false")
	}
	if loaded[0].Class != "" {
		t.Errorf("Class default should be empty, got %q", loaded[0].Class)
	}
}

func TestSaveAndLoadNodes(t *testing.T) {
	d := newMemDB(t)
	ns := &store.NodeState{
		NodeNum:  0x42,
		ID:       "!00000042",
		LongName: "TestNode", ShortName: "TN",
		HWModel: "HELTEC", Role: "CLIENT",
		LastHeard: time.Now().Unix(),
		HasPos:    true, Lat: 45.0, Lon: 9.0, Altitude: 100,
		BatteryLevel: 77, Voltage: 3.9,
		ChannelUtilization: 12.5, AirUtilTx: 4.0,
		Temperature: 21.5, Humidity: 55, BarometricPressure: 1013,
		RSSI: -80, SNR: 5, HopLimit: 3,
	}
	d.SaveNode(ns)

	loaded := d.LoadNodes()
	if len(loaded) != 1 {
		t.Fatalf("expected 1 node, got %d", len(loaded))
	}
	n := loaded[0]
	if n.NodeNum != 0x42 || n.LongName != "TestNode" {
		t.Errorf("identity wrong: %+v", n)
	}
	if !n.HasPos || n.Lat != 45.0 || n.Lon != 9.0 || n.Altitude != 100 {
		t.Errorf("position wrong: %+v", n)
	}
	if n.BatteryLevel != 77 || n.Voltage != 3.9 {
		t.Errorf("telemetry wrong: %+v", n)
	}
	if n.ChannelUtilization != 12.5 || n.AirUtilTx != 4.0 {
		t.Errorf("util wrong: %+v", n)
	}
}

func TestSaveNodeUpsert(t *testing.T) {
	d := newMemDB(t)
	ns1 := &store.NodeState{NodeNum: 0x99, LongName: "First", LastHeard: 100}
	d.SaveNode(ns1)
	ns2 := &store.NodeState{NodeNum: 0x99, LongName: "Updated", LastHeard: 200}
	d.SaveNode(ns2)

	loaded := d.LoadNodes()
	if len(loaded) != 1 {
		t.Fatalf("expected 1 node, got %d", len(loaded))
	}
	if loaded[0].LongName != "Updated" || loaded[0].LastHeard != 200 {
		t.Errorf("upsert failed: %+v", loaded[0])
	}
}

func TestSaveNodeWithNeighbors(t *testing.T) {
	d := newMemDB(t)
	ns := &store.NodeState{
		NodeNum: 0x10,
		Neighbors: []store.NeighborEntry{
			{NodeNum: 0x20, SNR: 5.0},
			{NodeNum: 0x30, SNR: 3.0},
		},
		NeighborsAt: time.Now().Unix(),
		NeighborBroadcastSecs: 3600,
	}
	d.SaveNode(ns)
	loaded := d.LoadNodes()
	if len(loaded) != 1 {
		t.Fatal("node not loaded")
	}
	n := loaded[0]
	if len(n.Neighbors) != 2 || n.NeighborBroadcastSecs != 3600 {
		t.Errorf("neighbors not round-tripped: %+v", n)
	}
}

func TestInsertAndLoadTraceroutes(t *testing.T) {
	d := newMemDB(t)
	tr := &store.TracerouteRecord{
		Time: time.Now().Unix(),
		From: 0xA, To: 0xB,
		Route:     []string{"!0000000a", "!0000000b"},
		RouteBack: []string{"!0000000b", "!0000000a"},
		SnrTowards: []int32{10, 8},
		SnrBack:    []int32{9, 7},
	}
	d.InsertTraceroute(tr)

	loaded := d.LoadTraceroutes()
	if len(loaded) != 1 {
		t.Fatalf("expected 1 traceroute, got %d", len(loaded))
	}
	if len(loaded[0].Route) != 2 || loaded[0].Route[0] != "!0000000a" {
		t.Errorf("route not round-tripped: %+v", loaded[0].Route)
	}
	if len(loaded[0].SnrTowards) != 2 || loaded[0].SnrTowards[0] != 10 {
		t.Errorf("snr_towards not round-tripped: %+v", loaded[0].SnrTowards)
	}
	if len(loaded[0].SnrBack) != 2 || loaded[0].SnrBack[1] != 7 {
		t.Errorf("snr_back not round-tripped: %+v", loaded[0].SnrBack)
	}
}

func TestCleanupOldDisabled(t *testing.T) {
	d := newMemDB(t)
	d.InsertEvent(&decoder.Event{
		Time: time.Now().UTC(), Type: decoder.EventTextMessage,
		Details: map[string]any{},
	})
	deleted, err := d.CleanupOld(0)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Errorf("CleanupOld(0) should delete nothing, got %d", deleted)
	}
}

func TestCleanupOldDeletes(t *testing.T) {
	d := newMemDB(t)
	// Old event: 100 days ago.
	d.InsertEvent(&decoder.Event{
		Time: time.Now().UTC().AddDate(0, 0, -100), Type: decoder.EventTextMessage,
		Details: map[string]any{},
	})
	d.InsertEvent(&decoder.Event{
		Time: time.Now().UTC(), Type: decoder.EventTextMessage,
		Details: map[string]any{},
	})
	deleted, err := d.CleanupOld(30)
	if err != nil {
		t.Fatal(err)
	}
	if deleted < 1 {
		t.Errorf("expected at least 1 deleted (old event), got %d", deleted)
	}
	loaded := d.LoadRecentEvents(10)
	if len(loaded) != 1 {
		t.Errorf("expected 1 remaining event, got %d", len(loaded))
	}
}

func TestLoadRecentEventsLogRecordFiltered(t *testing.T) {
	d := newMemDB(t)
	d.InsertEvent(&decoder.Event{
		Time: time.Now().UTC(), Type: decoder.EventLogRecord,
		Details: map[string]any{},
	})
	loaded := d.LoadRecentEvents(10)
	if len(loaded) != 0 {
		t.Errorf("LOG_RECORD events should be excluded, got %d", len(loaded))
	}
}

func TestLoadRecentEventsReversedChronological(t *testing.T) {
	d := newMemDB(t)
	for i := 0; i < 5; i++ {
		d.InsertEvent(&decoder.Event{
			Time: time.Now().UTC().Add(time.Duration(i) * time.Minute),
			Type: decoder.EventTextMessage, FromNode: uint32(i + 1),
			Details: map[string]any{},
		})
	}
	loaded := d.LoadRecentEvents(10)
	if len(loaded) != 5 {
		t.Fatalf("expected 5, got %d", len(loaded))
	}
	// LoadRecentEvents returns oldest-first (it reverses DB order).
	if loaded[0].FromNode != 1 || loaded[4].FromNode != 5 {
		t.Errorf("chronological order wrong: %d,%d", loaded[0].FromNode, loaded[4].FromNode)
	}
}

func TestLoadSnifferFiltering(t *testing.T) {
	d := newMemDB(t)
	now := time.Now().UTC()
	d.InsertEvent(&decoder.Event{
		Time: now, Type: decoder.EventTextMessage,
		FromNode: 0xA, ToNode: 0xB,
		Channel: 2, Class: "transit",
		Details: map[string]any{},
	})
	d.InsertEvent(&decoder.Event{
		Time: now.Add(time.Minute), Type: decoder.EventPosition,
		FromNode: 0xC, ToNode: 0xFFFF,
		Channel: 1, Class: "broadcast",
		Details: map[string]any{},
	})

	// Filter by type (Channel: -1 = unset filter).
	f := SnifferFilter{Type: "TEXT_MESSAGE", Channel: -1, Limit: 10}
	evts := d.LoadSniffer(f)
	if len(evts) != 1 || evts[0].FromNode != 0xA {
		t.Errorf("type filter: got %d want 1 from=A, data=%+v", len(evts), evts)
	}

	// Filter by from.
	f2 := SnifferFilter{From: 0xC, FromSet: true, Channel: -1, Limit: 10}
	evts2 := d.LoadSniffer(f2)
	if len(evts2) != 1 || evts2[0].Type != decoder.EventPosition {
		t.Errorf("from filter: got %d", len(evts2))
	}

	// Filter by class.
	f3 := SnifferFilter{Class: "broadcast", Channel: -1, Limit: 10}
	evts3 := d.LoadSniffer(f3)
	if len(evts3) != 1 || evts3[0].Class != "broadcast" {
		t.Errorf("class filter: got %d", len(evts3))
	}
}

func TestLoadSnifferSinceUntil(t *testing.T) {
	d := newMemDB(t)
	base := time.Now().UTC()
	d.InsertEvent(&decoder.Event{Time: base.Add(-2 * time.Hour), Type: decoder.EventTextMessage, Details: map[string]any{}})
	d.InsertEvent(&decoder.Event{Time: base.Add(-1 * time.Hour), Type: decoder.EventTextMessage, Details: map[string]any{}})
	d.InsertEvent(&decoder.Event{Time: base, Type: decoder.EventTextMessage, Details: map[string]any{}})

	f := SnifferFilter{Since: base.Add(-90 * time.Minute).Unix(), Until: base.Add(-30 * time.Minute).Unix(), Channel: -1, Limit: 10}
	evts := d.LoadSniffer(f)
	if len(evts) != 1 {
		t.Errorf("since/until filter: expected 1, got %d", len(evts))
	}
}

// ---- Signal History ----

func TestInsertAndLoadSignalHistory(t *testing.T) {
	d := newMemDB(t)
	now := time.Now().Unix()
	d.InsertSignal(SignalSample{Time: now - 100, NodeNum: 0x42, RSSI: -80, SNR: 5, HopLimit: 3, HopStart: 3})
	d.InsertSignal(SignalSample{Time: now - 50, NodeNum: 0x42, RSSI: -75, SNR: 6, HopLimit: 2, HopStart: 3})
	d.InsertSignal(SignalSample{Time: now, NodeNum: 0x99, RSSI: -90, SNR: 4, HopLimit: 1, HopStart: 3})

	// All samples for node 0x42.
	hist := d.LoadSignalHistory(0x42, 100)
	if len(hist) != 2 {
		t.Fatalf("want 2, got %d", len(hist))
	}
	// Oldest first.
	if hist[0].RSSI != -80 || hist[1].RSSI != -75 {
		t.Errorf("order/values wrong: %+v", hist)
	}
	// Limit=1.
	hist1 := d.LoadSignalHistory(0x42, 1)
	if len(hist1) != 1 || hist1[0].RSSI != -75 {
		t.Errorf("limit: %+v", hist1)
	}
	// Default limit.
	histDef := d.LoadSignalHistory(0x42, 0)
	if len(histDef) < 1 {
		t.Error("default limit should return samples")
	}
}

// ---- Radio Snapshots ----

func TestSaveAndLoadRadioSnapshots(t *testing.T) {
	d := newMemDB(t)
	now := time.Now().Unix()
	d.SaveRadioSnapshot(now, store.RadioHealth{
		Enabled: true, RawRxTotal: 100, RawDupTotal: 5, RawMqttTotal: 10,
		RxLast5Min: 20, DupLast5Min: 2, DupRate5Min: 0.1,
		Senders: []store.RadioSender{{NodeID: "!01"}, {NodeID: "!02"}},
		RawRelays: []store.RelayStat{{NodeID: "..ab"}},
		HopUsed:      map[string]int{"0": 10, "1": 5},
		ChannelHashes: map[string]int{"1f": 20},
	})
	// Disabled snapshot is skipped.
	d.SaveRadioSnapshot(now+1, store.RadioHealth{Enabled: false, RawRxTotal: 999})

	snaps := d.LoadRadioSnapshots(10)
	if len(snaps) != 1 {
		t.Fatalf("want 1 (disabled skipped), got %d", len(snaps))
	}
	if snaps[0].RxTotal != 100 || snaps[0].SendersCount != 2 {
		t.Errorf("fields wrong: %+v", snaps[0])
	}
}

// ---- Availability ----

func TestInsertAndLoadAvailability(t *testing.T) {
	d := newMemDB(t)
	now := time.Now().Unix()
	d.InsertAvailability(AvailabilityEvent{Time: now - 200, NodeNum: 0x10, Event: "online"})
	d.InsertAvailability(AvailabilityEvent{Time: now - 100, NodeNum: 0x10, Event: "offline"})
	d.InsertAvailability(AvailabilityEvent{Time: now, NodeNum: 0x20, Event: "online"})

	// Per-node.
	av := d.LoadAvailability(0x10, 50)
	if len(av) != 2 || av[0].Event != "online" || av[1].Event != "offline" {
		t.Errorf("per-node: %+v", av)
	}
	// All.
	all := d.LoadAllAvailability(50)
	if len(all) != 3 {
		t.Errorf("all: want 3, got %d", len(all))
	}
}

// ---- Channel Snapshots ----

func TestInsertAndLoadChannelSnapshots(t *testing.T) {
	d := newMemDB(t)
	now := time.Now().Unix()
	d.InsertChannelSnapshot(ChannelSnapshot{Time: now, NodesReporting: 3, AvgChanUtil: 15, MaxChanUtil: 45, AvgAirUtil: 5, MaxAirUtil: 20, TopTalkerNum: 0x42, TopTalkerUtil: 20})
	d.InsertChannelSnapshot(ChannelSnapshot{Time: now + 60, NodesReporting: 2, AvgChanUtil: 10, MaxChanUtil: 30, AvgAirUtil: 3, MaxAirUtil: 10})

	snaps := d.LoadChannelSnapshots(10)
	if len(snaps) != 2 {
		t.Fatalf("want 2, got %d", len(snaps))
	}
	if snaps[0].NodesReporting != 3 {
		t.Errorf("oldest first: %+v", snaps[0])
	}
}

// ---- percentile (unexported) ----

func TestPercentile(t *testing.T) {
	sorted := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := percentile(sorted, 0.50); got != 6 {
		t.Errorf("p50=%v want 6 (nearest-rank)", got)
	}
	if got := percentile(sorted, 0.90); got != 9 {
		t.Errorf("p90=%v want 9", got)
	}
	if got := percentile(sorted, 1.0); got != 10 {
		t.Errorf("p100=%v want 10", got)
	}
	if got := percentile(sorted, 0.0); got != 1 {
		t.Errorf("p0=%v want 1", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("empty=%v want 0", got)
	}
	// single element
	if got := percentile([]float64{42}, 0.5); got != 42 {
		t.Errorf("single=%v want 42", got)
	}
}

// ---- ChUtil insert + history ----

func TestInsertChUtilAndHistory(t *testing.T) {
	d := newMemDB(t)
	now := time.Now().Unix()
	d.InsertChUtilSample(ChUtilSample{NodeNum: 0x30, Time: now - 100, ChanUtil: 10, AirUtil: 3})
	d.InsertChUtilSample(ChUtilSample{NodeNum: 0x30, Time: now - 50, ChanUtil: 20, AirUtil: 5})
	d.InsertChUtilSample(ChUtilSample{NodeNum: 0x40, Time: now, ChanUtil: 30, AirUtil: 8})

	hist := d.ChUtilHistory(0x30, 24)
	if len(hist) != 2 {
		t.Fatalf("want 2, got %d", len(hist))
	}
	if hist[0].ChanUtil != 10 || hist[1].ChanUtil != 20 {
		t.Errorf("oldest first: %+v", hist)
	}
}

// ---- ClassCountsSince ----

func TestClassCountsSince(t *testing.T) {
	d := newMemDB(t)
	now := time.Now().UTC()
	since := now.Add(-1 * time.Hour).Unix()
	d.InsertEvent(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xA, ToNode: 0xB, Class: "transit", Details: map[string]any{}})
	d.InsertEvent(&decoder.Event{Time: now.Add(-2 * time.Hour), Type: decoder.EventTextMessage, FromNode: 0xC, ToNode: 0xFFFF, Class: "broadcast", Details: map[string]any{}})
	d.InsertEvent(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xD, ToNode: 0xE, Class: "transit", Details: map[string]any{}})

	cc := d.ClassCountsSince(since)
	if cc["transit"] != 2 {
		t.Errorf("transit=%d want 2", cc["transit"])
	}
	// The broadcast event is outside the since window.
	if cc["broadcast"] != 0 {
		t.Errorf("broadcast=%d want 0 (outside window)", cc["broadcast"])
	}
}

// ---- Temporal Heatmap ----

func TestTemporalHeatmap(t *testing.T) {
	d := newMemDB(t)
	now := time.Now() // local time, matches SQLite strftime localtime
	// Insert 3 events at decreasing times; heatmap groups by (weekday, hour) in
	// local time, so exact grouping depends on timezone. We just verify the query
	// succeeds and returns cells with correct aggregate semantics.
	for i := 0; i < 5; i++ {
		d.InsertEvent(&decoder.Event{Time: now.Add(-time.Duration(i) * time.Hour),
			Type: decoder.EventTextMessage, FromNode: uint32(0xA + i),
			RSSI: int32(-80 + i), SNR: float32(4 + float64(i)*0.5), Details: map[string]any{}})
	}
	d.InsertEvent(&decoder.Event{Time: now.Add(-2 * time.Hour), Type: decoder.EventPosition,
		FromNode: 0xA, RSSI: -90, SNR: 3, Details: map[string]any{}})
	// LOG_RECORD must be excluded.
	d.InsertEvent(&decoder.Event{Time: now, Type: decoder.EventLogRecord, FromNode: 0xFF, Details: map[string]any{}})

	cells, err := d.TemporalHeatmap(30)
	if err != nil {
		t.Fatalf("TemporalHeatmap: %v", err)
	}
	if len(cells) == 0 {
		t.Fatal("expected at least one cell")
	}
	// Sum across all cells.
	total := 0
	for _, c := range cells {
		total += c.Count
	}
	if total != 6 {
		t.Errorf("total count across cells = %d, want 6 (5 text + 1 pos, no log_record)", total)
	}
}

func TestTemporalHeatmapDefaults(t *testing.T) {
	d := newMemDB(t)
	d.InsertEvent(&decoder.Event{Time: time.Now(), Type: decoder.EventTextMessage, Details: map[string]any{}})
	cells, err := d.TemporalHeatmap(0) // 0 → defaults to 30
	if err != nil {
		t.Fatalf("TemporalHeatmap(0): %v", err)
	}
	if len(cells) == 0 {
		t.Error("default days should return data")
	}
}

// ---- Heatmap Cell Detail ----

func TestHeatmapCellDetailForOOB(t *testing.T) {
	d := newMemDB(t)
	det, err := d.HeatmapCellDetailFor(-1, 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	if det.Total != 0 {
		t.Error("out-of-bounds weekday should return empty cell")
	}
	det2, err := d.HeatmapCellDetailFor(0, 24, 30)
	if err != nil {
		t.Fatal(err)
	}
	if det2.Total != 0 {
		t.Error("out-of-bounds hour should return empty cell")
	}
}

func TestHeatmapCellDetailForEmpty(t *testing.T) {
	d := newMemDB(t)
	now := time.Now()
	wd := int(now.Weekday()) // Go weekday = SQLite strftime('%w') with localtime
	hr := now.Hour()
	// Insert at current (wd, hr); query for a different hour slot.
	d.InsertEvent(&decoder.Event{Time: now, Type: decoder.EventTextMessage, Details: map[string]any{}})

	det, err := d.HeatmapCellDetailFor(wd, (hr+1)%24, 30)
	if err != nil {
		t.Fatal(err)
	}
	if det == nil {
		t.Error("expected non-nil detail")
	}
}

func TestHeatmapCellDetailForWithData(t *testing.T) {
	d := newMemDB(t)
	now := time.Now() // local time — matches SQLite strftime localtime
	wd := int(now.Weekday())
	hr := now.Hour()

	for i := 0; i < 3; i++ {
		d.InsertEvent(&decoder.Event{Time: now.Add(-time.Duration(i) * time.Minute),
			Type: decoder.EventTextMessage, FromNode: uint32(0x10 + i),
			RSSI: -80, SNR: 5, Details: map[string]any{}})
	}
	d.InsertEvent(&decoder.Event{Time: now, Type: decoder.EventPosition, FromNode: 0x10, RSSI: -75, SNR: 6, Details: map[string]any{}})

	det, err := d.HeatmapCellDetailFor(wd, hr, 30)
	if err != nil {
		t.Fatal(err)
	}
	if det == nil {
		t.Fatal("expected non-nil detail")
	}
	if det.Total != 4 {
		t.Errorf("total=%d want 4", det.Total)
	}
	if len(det.TopNodes) == 0 {
		t.Error("expected top_nodes")
	}
	if len(det.Types) == 0 {
		t.Error("expected type breakdown")
	}
}

func TestHeatmapCellDetailDefaults(t *testing.T) {
	d := newMemDB(t)
	now := time.Now() // local time, matches SQLite strftime localtime
	wd := int(now.Weekday())
	hr := now.Hour()
	d.InsertEvent(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 1, Details: map[string]any{}})
	det, err := d.HeatmapCellDetailFor(wd, hr, 0) // 0 → defaults to 30
	if err != nil {
		t.Fatal(err)
	}
	if det.Days != 30 {
		t.Errorf("default days = %d want 30", det.Days)
	}
}
