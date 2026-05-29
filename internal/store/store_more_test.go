package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"mesh-reader/internal/decoder"
)

func ev(from uint32, typ decoder.EventType, ts time.Time) *decoder.Event {
	return &decoder.Event{Time: ts, Type: typ, FromNode: from, Details: map[string]any{}}
}

// ---------------------------------------------------------------------------
// Ring buffer: capacity + eviction + limit
// ---------------------------------------------------------------------------

func TestRingBufferExactCap(t *testing.T) {
	const n = 5
	s := New(n)
	now := time.Now()
	for i := 0; i < n; i++ {
		s.Add(ev(uint32(i+1), decoder.EventTextMessage, now.Add(time.Duration(i)*time.Second)))
	}
	got := s.RecentEvents(100, "")
	if len(got) != n {
		t.Fatalf("want %d, got %d", n, len(got))
	}
	if got[0].FromNode != uint32(n) || got[n-1].FromNode != 1 {
		t.Errorf("order: newest=%d(exp %d), oldest=%d(exp 1)", got[0].FromNode, n, got[n-1].FromNode)
	}
}

func TestRingBufferOverflowEvictsOldest(t *testing.T) {
	s := New(4)
	now := time.Now()
	for i := 0; i < 6; i++ {
		s.Add(ev(uint32(i+1), decoder.EventTextMessage, now.Add(time.Duration(i)*time.Second)))
	}
	if s.count != 6 {
		t.Errorf("s.count should be 6 (lifetime total), got %d", s.count)
	}
	got := s.RecentEvents(100, "")
	if len(got) != 4 {
		t.Fatalf("ring should hold 4, got %d", len(got))
	}
	if got[0].FromNode != 6 || got[3].FromNode != 3 {
		t.Errorf("survivors: newest=%d exp 6, oldest=%d exp 3", got[0].FromNode, got[3].FromNode)
	}
}

func TestRingBufferLimitCap(t *testing.T) {
	s := New(100)
	now := time.Now()
	for i := 0; i < 10; i++ {
		s.Add(ev(uint32(i+1), decoder.EventTextMessage, now))
	}
	got := s.RecentEvents(3, "")
	if len(got) != 3 {
		t.Errorf("limit=3 should return 3, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// classifyPacketLocked + class counting
// ---------------------------------------------------------------------------

func TestClassifyAllBranches(t *testing.T) {
	me := uint32(0xAAAA0001)
	cases := []struct {
		name        string
		myNum, from, to uint32
		want        string
	}{
		{"firmware-internal from=0", me, 0, 0x42, ""},
		{"broadcast to=0", me, 0xB001, 0, PacketClassBroadcast},
		{"broadcast to=0xFFFFFFFF", me, 0xB001, 0xFFFFFFFF, PacketClassBroadcast},
		{"from_me", me, me, 0xB001, PacketClassFromMe},
		{"personal to_us", me, 0xB002, me, PacketClassPersonal},
		{"transit neither", me, 0xB003, 0xB004, PacketClassTransit},
		{"unknown myNum unicast => transit", 0, 0xB001, 0xB002, PacketClassTransit},
		{"unknown myNum to_us => transit", 0, 0xB001, me, PacketClassTransit},
		{"broadcast-from-self to=FFFFFFFF", me, me, 0xFFFFFFFF, PacketClassBroadcast},
		{"broadcast-from-self to=0", me, me, 0, PacketClassBroadcast},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(100)
			s.myNodeNum = c.myNum
			ev := &decoder.Event{Time: time.Now(), Type: decoder.EventTextMessage, FromNode: c.from, ToNode: c.to, Details: map[string]any{}}
			s.Add(ev)
			if ev.Class != c.want {
				t.Errorf("class = %q, want %q", ev.Class, c.want)
			}
		})
	}
}

func TestClassCounts(t *testing.T) {
	s := New(100)
	s.myNodeNum = 0xAAAA0001
	now := time.Now()
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xB001, ToNode: 0xB002, Details: map[string]any{}}) // transit
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xB003, ToNode: 0xB004, Details: map[string]any{}}) // transit
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xAAAA0001, ToNode: 0xB001, Details: map[string]any{}}) // from_me
	s.Add(&decoder.Event{Time: now, Type: decoder.EventMyInfo, FromNode: 0, Details: map[string]any{}}) // no class
	cc := s.ClassCounts()
	if cc[PacketClassTransit] != 2 {
		t.Errorf("transit=%d want 2", cc[PacketClassTransit])
	}
	if cc[PacketClassFromMe] != 1 {
		t.Errorf("from_me=%d want 1", cc[PacketClassFromMe])
	}
	if _, ok := cc[""]; ok {
		t.Error("empty class should not be counted")
	}
}

// ---------------------------------------------------------------------------
// Node index + telemetry merge
// ---------------------------------------------------------------------------

func TestNodeCreatedFromNodeInfo(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(&decoder.Event{
		Time: now, Type: decoder.EventNodeInfo, FromNode: 0x55, RSSI: -80, SNR: 3.5, HopLimit: 3,
		Details: map[string]any{
			"id": "!00000055", "long_name": "Long", "short_name": "L5",
			"hw_model": "HELTEC", "role": "ROUTER", "lat": 45.0, "lon": 9.0, "altitude": int32(100),
		},
	})
	n, ok := s.NodeByNum(0x55)
	if !ok {
		t.Fatal("node not found")
	}
	if n.LongName != "Long" || n.ShortName != "L5" || n.Role != "ROUTER" {
		t.Errorf("identity: %+v", n)
	}
	if n.Lat != 45.0 || n.Lon != 9.0 || n.Altitude != 100 {
		t.Errorf("position: %+v", n)
	}
}

func TestPositionUpdatePreservesIdentity(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(&decoder.Event{Time: now, Type: decoder.EventNodeInfo, FromNode: 0x55,
		Details: map[string]any{"id": "!55", "long_name": "Long", "short_name": "L"}})
	s.Add(&decoder.Event{Time: now.Add(time.Minute), Type: decoder.EventPosition, FromNode: 0x55,
		Details: map[string]any{"lat": 46.0, "lon": 10.0, "altitude_m": int32(200)}})
	n, _ := s.NodeByNum(0x55)
	if n.Lat != 46.0 || n.LongName != "Long" {
		t.Errorf("position updated but identity lost: lat=%f name=%s", n.Lat, n.LongName)
	}
}

func TestTelemetryMerge(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTelemetry, FromNode: 0x66,
		Details: map[string]any{"type": "device", "battery_level_%": uint32(77), "voltage_v": float32(3.9), "channel_utilization_%": float32(12.5)}})
	s.Add(&decoder.Event{Time: now.Add(time.Second), Type: decoder.EventTelemetry, FromNode: 0x66,
		Details: map[string]any{"type": "environment", "temperature_c": float32(21.5), "relative_humidity_%": float32(55), "barometric_pressure_hpa": float32(1013)}})
	n, _ := s.NodeByNum(0x66)
	if n.BatteryLevel != 77 || n.Voltage != 3.9 || n.ChannelUtilization != 12.5 {
		t.Errorf("device telemetry: %+v", n)
	}
	if n.Temperature != 21.5 || n.Humidity != 55 || n.BarometricPressure != 1013 {
		t.Errorf("environment telemetry: %+v", n)
	}
}

func TestSignalNotOverwrittenByZero(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0x77, RSSI: -90, SNR: 5, HopLimit: 4, Details: map[string]any{}})
	s.Add(&decoder.Event{Time: now.Add(time.Second), Type: decoder.EventTextMessage, FromNode: 0x77, RSSI: 0, SNR: 0, HopLimit: 0, Details: map[string]any{}})
	n, _ := s.NodeByNum(0x77)
	if n.RSSI != -90 || n.SNR != 5 || n.HopLimit != 4 {
		t.Errorf("zero values clobbered signal: RSSI=%d SNR=%f HL=%d", n.RSSI, n.SNR, n.HopLimit)
	}
}

// ---------------------------------------------------------------------------
// MyInfo / Metadata / ConfigLora / ModuleNeighbor
// ---------------------------------------------------------------------------

func TestMyInfoSetsSelf(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(&decoder.Event{
		Time: now, Type: decoder.EventMyInfo, FromNode: 0xABCDEF01,
		Details: map[string]any{"my_node_num": "!abcdef01", "reboot_count": uint32(7), "pio_env": "tbeam", "nodedb_count": uint32(42)},
	})
	if s.MyNodeNum() != 0xABCDEF01 {
		t.Errorf("myNodeNum=%x", s.MyNodeNum())
	}
	ln := s.LocalNode()
	if ln.NodeNum != 0xABCDEF01 || ln.RebootCount != 7 {
		t.Errorf("local node: %+v", ln)
	}
}

func TestMetadataApplied(t *testing.T) {
	s := New(100)
	s.Add(&decoder.Event{Time: time.Now(), Type: decoder.EventMetadata, FromNode: 0,
		Details: map[string]any{"firmware_version": "2.5.0", "hw_model": "TBEAM", "role": "ROUTER",
			"has_wifi": true, "has_bluetooth": true, "has_pkc": true, "can_shutdown": true, "device_state_version": uint32(23)}})
	ln := s.LocalNode()
	if ln.FirmwareVersion != "2.5.0" || !ln.HasWifi || !ln.HasBluetooth {
		t.Errorf("metadata not applied: %+v", ln)
	}
}

func TestConfigLoraApplied(t *testing.T) {
	s := New(100)
	s.Add(&decoder.Event{Time: time.Now(), Type: decoder.EventConfigLora, FromNode: 0,
		Details: map[string]any{"region": "EU_868", "modem_preset": "LONG_FAST", "use_preset": true,
			"hop_limit": uint32(3), "tx_power": int32(27), "tx_enabled": true, "channel_num": uint32(20)}})
	ln := s.LocalNode()
	if ln.Region != "EU_868" || ln.HopLimit != 3 || ln.ChannelNum != 20 {
		t.Errorf("config lora: %+v", ln)
	}
}

// ---------------------------------------------------------------------------
// Diagnostics: MarkNodeHeard / ScanOffline / NodeUptime
// ---------------------------------------------------------------------------

func TestMarkNodeHeardTransition(t *testing.T) {
	s := New(100)
	tr := s.MarkNodeHeard(0x42, time.Now())
	if tr == nil || tr.Event != "online" {
		t.Errorf("first heard should be online: %+v", tr)
	}
	tr2 := s.MarkNodeHeard(0x42, time.Now())
	if tr2 != nil {
		t.Error("second call should be nil (already online)")
	}
	if s.MarkNodeHeard(0, time.Now()) != nil {
		t.Error("nodeNum=0 should be skipped")
	}
}

func TestScanOffline(t *testing.T) {
	s := New(100)
	old := time.Now().Add(-2 * time.Hour)
	s.MarkNodeHeard(0x42, old)
	tr := s.ScanOffline()
	if len(tr) != 1 || tr[0].Event != "offline" {
		t.Errorf("expected 1 offline: %+v", tr)
	}
	tr2 := s.ScanOffline()
	if len(tr2) != 0 {
		t.Error("second scan should be empty")
	}
}

func TestNodeUptimeCalculation(t *testing.T) {
	// Edge cases.
	if NodeUptime(nil, 0, 100) != 0 {
		t.Error("empty → 0")
	}
	if NodeUptime(nil, 100, 50) != 0 {
		t.Error("inverted window → 0")
	}
	// Always online before window.
	ev := []AvailTransition{{Time: 0, Event: "online", NodeNum: 1}}
	u := NodeUptime(ev, 50, 150)
	if u <= 0.99 || u > 1.01 {
		t.Errorf("always-on = %f, want ~1.0", u)
	}
	// Toggling.
	ev2 := []AvailTransition{
		{Time: 0, Event: "offline", NodeNum: 1},
		{Time: 75, Event: "online", NodeNum: 1},
	}
	u2 := NodeUptime(ev2, 50, 150)
	if u2 < 0.74 || u2 > 0.76 {
		t.Errorf("partial uptime = %f, want ~0.75", u2)
	}
}

// ---------------------------------------------------------------------------
// AggregateChannelUtil + NodesDiag
// ---------------------------------------------------------------------------

func TestAggregateChannelUtil(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTelemetry, FromNode: 0xA1,
		Details: map[string]any{"type": "device", "channel_utilization_%": float32(15), "air_util_tx_%": float32(5)}})
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTelemetry, FromNode: 0xA2,
		Details: map[string]any{"type": "device", "channel_utilization_%": float32(45), "air_util_tx_%": float32(20)}})
	agg := s.AggregateChannelUtil()
	if agg.NodesReporting != 2 {
		t.Errorf("nodes=%d", agg.NodesReporting)
	}
	if !agg.Congested {
		t.Error("avg=30 >25, congested should be true")
	}
	if agg.TopTalkerNum != 0xA2 {
		t.Errorf("top talker=%x", agg.TopTalkerNum)
	}
}

func TestNodesDiag(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(&decoder.Event{Time: now, Type: decoder.EventNodeInfo, FromNode: 0xB1,
		Details: map[string]any{"long_name": "Diag1", "short_name": "D1"}})
	s.Add(&decoder.Event{Time: now.Add(-time.Second), Type: decoder.EventNodeInfo, FromNode: 0xB2,
		Details: map[string]any{"short_name": "D2"}})
	diags := s.NodesDiag()
	if len(diags) != 2 {
		t.Fatalf("want 2, got %d", len(diags))
	}
	if diags[0].NodeNum != 0xB1 {
		t.Errorf("newest first: got %x", diags[0].NodeNum)
	}
}

// ---------------------------------------------------------------------------
// HopStart histogram + mode + max
// ---------------------------------------------------------------------------

func TestHopStartCountValid(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(makeNodeInfoEvent(0x88, now)) // will record hop_start=0
	// Only valid HopStart values (0-7) are recorded.
	for _, h := range []uint32{3, 3, 3, 5, 7} {
		s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0x88, HopStart: h, Details: map[string]any{}})
	}
	n, _ := s.NodeByNum(0x88)
	// Mode should be 3 (count=3), max should be 7.
	if n.HopStartMode != 3 {
		t.Errorf("mode=%d want 3", n.HopStartMode)
	}
	if n.HopStartMax != 7 {
		t.Errorf("max=%d want 7", n.HopStartMax)
	}
}

func TestHopStartAbove7Skipped(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(makeNodeInfoEvent(0x89, now))
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0x89, HopStart: 9, Details: map[string]any{}})
	n, _ := s.NodeByNum(0x89)
	if n.HopStartHist["9"] != 0 {
		t.Errorf("hop_start=9 should NOT be in hist: %+v", n.HopStartHist)
	}
}

// ---------------------------------------------------------------------------
// IsolatedNodesReport
// ---------------------------------------------------------------------------

func TestIsolatedNodesRiskCategories(t *testing.T) {
	s := New(100)
	s.myNodeNum = 0xAAAA0001
	now := time.Now()
	// "weak": direct-only + marginal RSSI < -115, >=3 packets.
	for i := 0; i < 4; i++ {
		s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xC01, ToNode: 0xFFFF, RSSI: -120, SNR: -2, HopStart: 3, HopLimit: 3, Details: map[string]any{}})
	}
	// "direct-only": >=3 direct hits, good signal.
	for i := 0; i < 3; i++ {
		s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xC02, ToNode: 0xFFFF, RSSI: -80, SNR: 5, HopStart: 3, HopLimit: 3, Details: map[string]any{}})
	}
	// "spof": 1 relay only.
	for i := 0; i < 3; i++ {
		s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xC03, ToNode: 0xFFFF, RSSI: -70, SNR: 10, HopStart: 3, HopLimit: 1, RelayNode: 0xAA, Details: map[string]any{}})
	}
	// "healthy": 2+ relays.
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xC04, ToNode: 0xFFFF, RSSI: -60, SNR: 12, RelayNode: 0xAA, Details: map[string]any{}})
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xC04, ToNode: 0xFFFF, RSSI: -60, SNR: 12, RelayNode: 0xBB, Details: map[string]any{}})
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xC04, ToNode: 0xFFFF, RSSI: -60, SNR: 12, RelayNode: 0xCC, Details: map[string]any{}})

	rep := s.IsolatedNodesReport(3)
	risks := map[string]int{}
	for _, r := range rep {
		risks[r.Risk]++
	}
	if risks["weak"] != 1 || risks["direct-only"] != 1 || risks["spof"] != 1 || risks["healthy"] != 1 {
		t.Errorf("risk distribution: %+v (want 1 each)", risks)
	}
	// Verify ordering: weak < direct-only < spof < healthy.
	order := map[string]int{"weak": 0, "direct-only": 1, "spof": 2, "healthy": 3}
	for i := 1; i < len(rep); i++ {
		if order[rep[i-1].Risk] > order[rep[i].Risk] {
			t.Errorf("order broken: [%d]=%s > [%d]=%s", i-1, rep[i-1].Risk, i, rep[i].Risk)
		}
	}
}

func TestRelayLabel(t *testing.T) {
	cases := []struct{ in uint32; want string }{
		{0x00, "..00"}, {0x42, "..42"}, {0xFF, "..ff"}, {0xAB, "..ab"},
	}
	for _, c := range cases {
		if got := relayLabel(c.in); got != c.want {
			t.Errorf("relayLabel(0x%02x) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Traceroute / NeighborInfo
// ---------------------------------------------------------------------------

func TestTracerouteRecorded(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(&decoder.Event{
		Time: now, Type: decoder.EventTraceroute, FromNode: 0xD1, ToNode: 0xD2,
		Details: map[string]any{
			"route":       []string{"!000000d1", "!000000d2"},
			"route_back":  []string{"!000000d2", "!000000d1"},
			"snr_towards": []int32{10, 8},
			"snr_back":    []int32{9, 7},
		},
	})
	tr := s.Traceroutes()
	if len(tr) != 1 || len(tr[0].Route) != 2 || len(tr[0].SnrTowards) != 2 {
		t.Errorf("traceroute: %+v", tr)
	}
}

func TestNeighborInfoAddsLinks(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(&decoder.Event{
		Time: now, Type: decoder.EventNeighborInfo, FromNode: 0xE1,
		Details: map[string]any{
			"node_id_num": uint32(0xE1), "broadcast_secs": uint32(3600),
			"neighbors": []map[string]any{
				{"node_id": "!000000e2", "snr": float32(7.5)},
				{"node_id": "!000000e3", "snr": float32(-3.0)},
			},
		},
	})
	links := s.Links()
	if len(links) != 2 {
		t.Fatalf("want 2 neighbor links, got %d", len(links))
	}
	for _, l := range links {
		if !l.Neighbor {
			t.Error("link should be marked Neighbor=true")
		}
	}
}

// ---------------------------------------------------------------------------
// AddSilent
// ---------------------------------------------------------------------------

func TestAddSilentDoesNotCount(t *testing.T) {
	s := New(100)
	s.AddSilent(&decoder.Event{Time: time.Now(), Type: decoder.EventNodeInfo, FromNode: 0xF1,
		Details: map[string]any{"long_name": "Silent"}})
	if _, ok := s.NodeByNum(0xF1); !ok {
		t.Fatal("AddSilent should update node state")
	}
	if s.Stats().TotalEvents != 0 {
		t.Error("AddSilent must not increment TotalEvents")
	}
	if len(s.RecentEvents(10, "")) != 0 {
		t.Error("AddSilent must not insert into ring buffer")
	}
}

// ---------------------------------------------------------------------------
// WebSocket pub/sub
// ---------------------------------------------------------------------------

func TestSubscribeReceive(t *testing.T) {
	s := New(100)
	id, ch := s.Subscribe()
	defer s.Unsubscribe(id)
	ev := ev(1, decoder.EventTextMessage, time.Now())
	s.Add(ev)
	select {
	case got := <-ch:
		if got != ev {
			t.Error("received wrong event")
		}
	case <-time.After(time.Second):
		t.Fatal("no event received")
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	s := New(100)
	id, ch := s.Subscribe()
	s.Unsubscribe(id)
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after unsubscribe")
	}
	s.Add(ev(1, decoder.EventTextMessage, time.Now())) // must not panic
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	s := New(1000)
	id, ch := s.Subscribe()
	defer s.Unsubscribe(id)
	done := make(chan struct{})
	go func() {
		now := time.Now()
		for i := 0; i < 1000; i++ {
			s.Add(ev(uint32(i+1), decoder.EventTextMessage, now))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Add blocked on slow subscriber")
	}
	_ = ch // never drain
}

func TestConcurrentPubSubRace(t *testing.T) {
	s := New(1000)
	var wg sync.WaitGroup
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(base uint32) {
			defer wg.Done()
			now := time.Now()
			for i := 0; i < 200; i++ {
				s.Add(ev(base+uint32(i), decoder.EventTextMessage, now))
			}
		}(uint32(p) * 10000)
	}
	for c := 0; c < 4; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				id, ch := s.Subscribe()
				for j := 0; j < 5; j++ {
					select { case <-ch: default: }
				}
				s.Unsubscribe(id)
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentReadWrite(t *testing.T) {
	s := New(500)
	s.myNodeNum = 0xAAAA0001
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		now := time.Now()
		for i := 0; i < 1000; i++ {
			s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: uint32(0xB000 + i%30), ToNode: uint32(0xC000 + i%20), RSSI: -80, SNR: 5, HopStart: 3, HopLimit: 3, PacketID: uint32(i+1), Details: map[string]any{}})
		}
		close(stop)
	}()

	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select { case <-stop: return; default: }
				_ = s.RecentEvents(50, "")
				_ = s.Nodes()
				_ = s.Stats()
				_ = s.Links()
				_ = s.Anomalies(10)
				_ = s.ClassCounts()
				_ = s.EventsPerMinute(5)
				_ = s.AggregateChannelUtil()
				_ = s.NodesDiag()
				_ = s.IsolatedNodesReport(1)
				_ = s.PairList(10, true)
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// EventsPerMinute
// ---------------------------------------------------------------------------

func TestEventsPerMinuteBuckets(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(ev(1, decoder.EventTextMessage, now.Add(-2*time.Second)))
	s.Add(ev(2, decoder.EventTextMessage, now.Add(-3*time.Minute-30*time.Second)))
	b := s.EventsPerMinute(5)
	if len(b) != 5 {
		t.Fatalf("want 5 buckets, got %d", len(b))
	}
	total := 0
	for _, v := range b {
		total += v
	}
	if total != 2 {
		t.Errorf("want 2 events total, got %d (%v)", total, b)
	}
}

func TestEventsPerMinuteDefaults60(t *testing.T) {
	s := New(100)
	b := s.EventsPerMinute(0)
	if len(b) != 60 {
		t.Errorf("default window 60, got %d", len(b))
	}
}

// ---------------------------------------------------------------------------
// SetMisbehaveConfig / Sanitize round-trip
// ---------------------------------------------------------------------------

func TestSetMisbehaveSanitize(t *testing.T) {
	s := New(100)
	in := MisbehaveConfig{TelemetryEnabled: true, TelemetryCount: -3, TelemetryWindowSec: 10}
	app := s.SetMisbehaveConfig(in)
	if app.TelemetryCount != 0 {
		t.Errorf("neg count should clamp to 0, got %d", app.TelemetryCount)
	}
	if app.TelemetryWindowSec < 60 {
		t.Errorf("small window should clamp up: %d", app.TelemetryWindowSec)
	}
	snap := s.MisbehaveConfigSnapshot()
	if snap.TelemetryWindowSec != app.TelemetryWindowSec {
		t.Error("snapshot mismatch")
	}
}

// ---------------------------------------------------------------------------
// LastEventAt / ActiveNodes / Stats
// ---------------------------------------------------------------------------

func TestLastEventAt(t *testing.T) {
	s := New(100)
	if !s.LastEventAt().IsZero() {
		t.Error("zero before any Add")
	}
	s.Add(ev(1, decoder.EventTextMessage, time.Now()))
	if s.LastEventAt().IsZero() {
		t.Error("nonzero after Add")
	}
}

func TestActiveNodes30MinWindow(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(makeNodeInfoEvent(1, now))
	s.Add(makeNodeInfoEvent(2, now.Add(-31*time.Minute)))
	st := s.Stats()
	if st.TotalNodes != 2 {
		t.Errorf("total=%d", st.TotalNodes)
	}
	if st.ActiveNodes != 1 {
		t.Errorf("active=%d want 1 (30-min window)", st.ActiveNodes)
	}
}

// ---------------------------------------------------------------------------
// LoadNodes / LoadEvents / LoadTraceroutes / RebuildLinks
// ---------------------------------------------------------------------------

func TestLoadNodes(t *testing.T) {
	s := New(100)
	s.LoadNodes([]NodeState{
		{NodeNum: 0xF1, LongName: "N1"},
		{NodeNum: 0xF2, LongName: "N2"},
	})
	if len(s.Nodes()) != 2 {
		t.Errorf("want 2 nodes loaded, got %d", len(s.Nodes()))
	}
	n, _ := s.NodeByNum(0xF1)
	if n.PacketsByType == nil {
		t.Error("PacketsByType should be initialised by LoadNodes")
	}
}

func TestLoadEventsAndSetCounts(t *testing.T) {
	s := New(100)
	s.LoadEvents([]*decoder.Event{
		{Time: time.Now(), Type: decoder.EventTextMessage, FromNode: 1, PacketID: 1, Details: map[string]any{}},
		{Time: time.Now(), Type: decoder.EventMyInfo, FromNode: 0x5000, Details: map[string]any{"my_node_num": "!00005000"}},
	})
	s.SetCounts(500, 42)
	st := s.Stats()
	if st.TotalEvents != 500 || st.MessagesCount != 42 {
		t.Errorf("SetCounts: e=%d m=%d", st.TotalEvents, st.MessagesCount)
	}
	if s.MyNodeNum() != 0x5000 {
		t.Errorf("myNodeNum from MyInfo: %x", s.MyNodeNum())
	}
}

func TestLoadTraceroutes(t *testing.T) {
	s := New(100)
	s.LoadTraceroutes([]TracerouteRecord{{From: 1, To: 2}, {From: 3, To: 4}})
	if len(s.Traceroutes()) != 2 {
		t.Errorf("want 2, got %d", len(s.Traceroutes()))
	}
}

func TestRebuildLinksFromNeighbors(t *testing.T) {
	s := New(100)
	now := time.Now().Unix()
	s.LoadNodes([]NodeState{
		{NodeNum: 0x100, NeighborsAt: now, LastHeard: now, Neighbors: []NeighborEntry{
			{NodeNum: 0x200, SNR: 5},
			{NodeNum: 0x300, SNR: 6},
			{NodeNum: 0x100, SNR: 1}, // self ref skipped
			{NodeNum: 0, SNR: 1},     // zero skipped
		}},
	})
	n := s.RebuildLinksFromNeighbors(24 * 3600)
	if n != 2 {
		t.Errorf("want 2 links (self+zero skipped), got %d", n)
	}
}

func TestRebuildLinksAgeFilter(t *testing.T) {
	s := New(100)
	old := time.Now().Add(-48 * time.Hour).Unix()
	s.LoadNodes([]NodeState{
		{NodeNum: 0x100, NeighborsAt: old, LastHeard: old, Neighbors: []NeighborEntry{{NodeNum: 0x200, SNR: 5}}},
	})
	n := s.RebuildLinksFromNeighbors(24 * 3600)
	if n != 0 {
		t.Errorf("stale snapshot should be dropped, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// SanitizeConfig edges
// ---------------------------------------------------------------------------

func TestSanitizeConfigDefaults(t *testing.T) {
	c := MisbehaveConfig{NotifyTemplate: "", NotifyChannel: 99, NotifyHopLimit: 99, NotifyMinFlagAgeSec: 10}
	c.Sanitize()
	if c.NotifyTemplate != DefaultNotifyTemplate {
		t.Error("empty template should get default")
	}
	if c.NotifyChannel != 7 {
		t.Errorf("channel clamp: %d", c.NotifyChannel)
	}
	if c.NotifyHopLimit != 7 {
		t.Errorf("hop limit clamp: %d", c.NotifyHopLimit)
	}
}

func TestLargestWindow(t *testing.T) {
	c := MisbehaveConfig{
		NodeInfoEnabled: true, NodeInfoWindowSec: 600,
		TelemetryEnabled: true, TelemetryWindowSec: 7200,
		PositionEnabled: false, PositionWindowSec: 99999,
	}
	if w := c.largestWindow(); w != 7200 {
		t.Errorf("largest=%d, want 7200", w)
	}
	empty := MisbehaveConfig{}

	if w := empty.largestWindow(); w < 1 {
		t.Errorf("all-disabled floor: %d", w)
	}
}

// ---------------------------------------------------------------------------
// RenderNotifyTemplate
// ---------------------------------------------------------------------------

func TestRenderNotifyTemplate(t *testing.T) {
	s := New(100)
	n := MisbehavingNode{ShortName: "Bob", LongName: "Bob Long", NodeID: "!09",
		TelemetryCount: 5, Reasons: []string{"Tel 5 / 60m (>2)"}}
	out := s.RenderNotifyTemplate("Hi {short} ({long}) from {me}: {reasons}", n, "Me", "Me Long", MisbehaveConfig{})
	if out == "" {
		t.Fatal("empty render")
	}
	for _, want := range []string{"Bob", "Bob Long", "Me", "Tel 5"} {
		if !contains(out, want) {
			t.Errorf("render missing %q in %q", want, out)
		}
	}
	// Fallback to NodeID when short/long empty.
	n2 := MisbehavingNode{NodeID: "!10"}
	out2 := s.RenderNotifyTemplate("{short}|{long}", n2, "", "", MisbehaveConfig{})
	if out2 != "!10|!10" {
		t.Errorf("fallback: %q", out2)
	}
}

func TestRenderNotifyTemplateTruncation(t *testing.T) {
	s := New(100)
	tpl := ""
	for i := 0; i < 500; i++ {
		tpl += "z"
	}
	out := s.RenderNotifyTemplate(tpl, MisbehavingNode{NodeID: "!1"}, "", "", MisbehaveConfig{})
	if len([]rune(out)) != 200 {
		t.Errorf("truncation: %d runes, want 200", len([]rune(out)))
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Dedup / seenPackets: duplicate doesn't block ring
// ---------------------------------------------------------------------------

func TestDedupDoesNotBlockRing(t *testing.T) {
	s := New(100)
	now := time.Now()
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xF001, PacketID: 0xABCD, Details: map[string]any{}})
	s.Add(&decoder.Event{Time: now.Add(time.Second), Type: decoder.EventTextMessage, FromNode: 0xF001, PacketID: 0xABCD, Details: map[string]any{}})
	if len(s.RecentEvents(10, "")) != 2 {
		t.Error("dedup should NOT block ring insertion")
	}
}

// ---------------------------------------------------------------------------
// BackfillDX / node name resolution
// ---------------------------------------------------------------------------

func TestBackfillDXEmpty(t *testing.T) {
	s := New(100)
	s.BackfillDX()
	if len(s.DXLeaderboard(10, false)) != 0 {
		t.Error("empty store should give empty leaderboard")
	}
}

func TestResolveRelayNodes(t *testing.T) {
	s := New(100)
	old := time.Now().Add(-time.Hour)
	recent := time.Now()
	s.Add(&decoder.Event{Time: old, Type: decoder.EventNodeInfo, FromNode: 0x11110055, Details: map[string]any{}})
	s.Add(&decoder.Event{Time: recent, Type: decoder.EventNodeInfo, FromNode: 0x22220055, Details: map[string]any{}})
	out := s.ResolveRelayNodes(0x55)
	if len(out) != 2 {
		t.Fatalf("want 2 matches, got %d", len(out))
	}
	if out[0] != 0x22220055 {
		t.Errorf("most-recent first: got %x", out[0])
	}
	if s.ResolveRelayNodes(0) != nil {
		t.Error("lastByte=0 should return nil")
	}
}

// ---------------------------------------------------------------------------
// PairList (per-pair stats)
// ---------------------------------------------------------------------------

func TestPairList(t *testing.T) {
	s := New(100)
	s.myNodeNum = 0xAAAA0001
	now := time.Now()
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xE001, ToNode: 0xE002, RSSI: -60, SNR: 10, HopStart: 3, HopLimit: 3, Channel: 2, Details: map[string]any{}})
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xE001, ToNode: 0xE002, RSSI: -65, SNR: 9, HopStart: 3, HopLimit: 3, Channel: 2, Details: map[string]any{}})
	s.Add(&decoder.Event{Time: now, Type: decoder.EventTextMessage, FromNode: 0xE003, ToNode: 0xE004, RSSI: -70, SNR: 8, HopStart: 3, HopLimit: 3, Channel: 2, Details: map[string]any{}})
	pl := s.PairList(10, true)
	if len(pl) != 2 {
		t.Fatalf("want 2 pairs, got %d", len(pl))
	}
	if pl[0].Count != 2 {
		t.Errorf("first pair count=%d want 2", pl[0].Count)
	}
}

// ---------------------------------------------------------------------------
// NodeID bit pattern (keeps fmt import)
// ---------------------------------------------------------------------------

func TestNodeIDFormat(t *testing.T) {
	if got := fmt.Sprintf("!%08x", uint32(0xABCD)); got != "!0000abcd" {
		t.Errorf("format: %q", got)
	}
}

// ---------------------------------------------------------------------------
// Haversine distance (used by GPS teleport detector + DX leaderboard)
// ---------------------------------------------------------------------------

func TestHaversineKm(t *testing.T) {
	// Same point → 0.
	if d := haversineKmInternal(45.0, 9.0, 45.0, 9.0); d != 0 {
		t.Errorf("same point = %f, want 0", d)
	}
	// Milan → Rome ≈ 477 km.
	d := haversineKmInternal(45.464, 9.190, 41.893, 12.483)
	if d < 470 || d > 485 {
		t.Errorf("Milan→Rome = %.0f km, want ~477", d)
	}
	// North Pole → South Pole = 20037 km (half circumference).
	d2 := haversineKmInternal(90, 0, -90, 0)
	if d2 < 20000 || d2 > 20050 {
		t.Errorf("N→S pole = %.0f km, want ~20037", d2)
	}
	// Equator, 180 degrees apart → 20037 km.
	d3 := haversineKmInternal(0, 0, 0, 180)
	if d3 < 20000 || d3 > 20050 {
		t.Errorf("equator 180M-BM-0 = %.0f km, want ~20037", d3)
	}
	// Very short distance: 1 degree lat ≈ 111.32 km.
	d4 := haversineKmInternal(45.0, 9.0, 46.0, 9.0)
	if d4 < 110 || d4 > 113 {
		t.Errorf("1M-BM-0 lat = %.1f km, want ~111.3", d4)
	}
}
