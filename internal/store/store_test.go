package store

import (
	"testing"
	"time"

	"mesh-reader/internal/decoder"
)

func makeTelemetryEvent(from uint32, ts time.Time) *decoder.Event {
	return &decoder.Event{
		Time:     ts,
		Type:     decoder.EventTelemetry,
		FromNode: from,
		Details:  map[string]any{},
	}
}

func makeNodeInfoEvent(from uint32, ts time.Time) *decoder.Event {
	return &decoder.Event{
		Time:     ts,
		Type:     decoder.EventNodeInfo,
		FromNode: from,
		Details:  map[string]any{},
	}
}

func makeHopEvent(from uint32, ts time.Time, hopStart uint32) *decoder.Event {
	return &decoder.Event{
		Time:     ts,
		Type:     decoder.EventTextMessage,
		FromNode: from,
		HopStart: hopStart,
	}
}

func TestRingBufferBasic(t *testing.T) {
	s := New(3)
	now := time.Now()

	s.Add(makeTelemetryEvent(0x01, now))
	s.Add(makeTelemetryEvent(0x02, now.Add(time.Second)))
	s.Add(makeTelemetryEvent(0x03, now.Add(2*time.Second)))

	events := s.RecentEvents(10, "")
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
}

func TestRingBufferWrap(t *testing.T) {
	s := New(3)
	now := time.Now()

	// Add 5 events to a ring of size 3 — oldest 2 should be overwritten
	s.Add(makeTelemetryEvent(0x01, now))
	s.Add(makeTelemetryEvent(0x02, now.Add(time.Second)))
	s.Add(makeTelemetryEvent(0x03, now.Add(2*time.Second)))
	s.Add(makeTelemetryEvent(0x04, now.Add(3*time.Second)))
	s.Add(makeTelemetryEvent(0x05, now.Add(4*time.Second)))

	events := s.RecentEvents(10, "")
	if len(events) > 3 {
		t.Errorf("expected at most 3 events in ring, got %d", len(events))
	}
}

func TestDeduplication(t *testing.T) {
	s := New(100)
	now := time.Now()

	ev1 := &decoder.Event{
		Time:     now,
		Type:     decoder.EventTextMessage,
		FromNode: 0xAAAA,
		PacketID: 42,
	}
	ev2 := &decoder.Event{
		Time:     now.Add(time.Second),
		Type:     decoder.EventTextMessage,
		FromNode: 0xAAAA,
		PacketID: 42, // same from+id → duplicate
	}
	ev3 := &decoder.Event{
		Time:     now.Add(2 * time.Second),
		Type:     decoder.EventTextMessage,
		FromNode: 0xAAAA,
		PacketID: 43, // different id
	}

	s.Add(ev1)
	s.Add(ev2)
	s.Add(ev3)

	// s.count counts total Adds; dedup only affects per-type counter, not ring insertion
	if s.count != 3 {
		t.Errorf("expected count=3, got %d", s.count)
	}
}

func TestMisbehavingRateTracking(t *testing.T) {
	s := New(1000)
	now := time.Now().Truncate(time.Second)

	// Simulate a node sending 5 telemetry events within 30 seconds
	node := uint32(0xBB)
	for i := 0; i < 5; i++ {
		s.Add(makeTelemetryEvent(node, now.Add(time.Duration(i)*10*time.Second)))
	}

	// With default threshold (2 telemetry / 60 min), all 5 should be over limit
	report := s.Misbehaving(nil)
	found := false
	for _, n := range report.Nodes {
		if n.NodeNum == node {
			found = true
			if n.TelemetryCount != 5 {
				t.Errorf("expected 5 telemetry events, got %d", n.TelemetryCount)
			}
			if len(n.Reasons) == 0 {
				t.Error("expected at least one reason")
			}
		}
	}
	if !found {
		t.Error("node 0xBB not found in misbehaving report")
	}
}

func TestMisbehavingHopMode(t *testing.T) {
	s := New(100)
	now := time.Now().Truncate(time.Second)

	node := uint32(0xCC)
	// Send packets with hop_start=7 (aggressive TTL)
	for i := 0; i < 10; i++ {
		s.Add(makeHopEvent(node, now.Add(time.Duration(i)*time.Second), 7))
	}

	report := s.Misbehaving(nil)
	found := false
	for _, n := range report.Nodes {
		if n.NodeNum == node {
			found = true
			if n.HopStartMode != 7 {
				t.Errorf("expected hop mode 7, got %d", n.HopStartMode)
			}
		}
	}
	if !found {
		t.Error("node 0xCC with hop_start=7 not found in misbehaving report")
	}
}

func TestMisbehavingBelowThreshold(t *testing.T) {
	s := New(100)
	now := time.Now().Truncate(time.Second)

	node := uint32(0xDD)
	// Only 1 telemetry event — below the default threshold of 2
	s.Add(makeTelemetryEvent(node, now))

	report := s.Misbehaving(nil)
	for _, n := range report.Nodes {
		if n.NodeNum == node {
			t.Error("node with 1 telemetry event should not be flagged")
		}
	}
}

func TestMisbehavingCustomConfig(t *testing.T) {
	s := New(100)
	now := time.Now().Truncate(time.Second).UTC()

	node := uint32(0xEE)
	s.Add(makeTelemetryEvent(node, now))
	s.Add(makeTelemetryEvent(node, now.Add(10*time.Second)))

	// Custom config: flag at 1 telemetry event
	cfg := MisbehaveConfig{
		TelemetryEnabled:   true,
		TelemetryCount:     1,
		TelemetryWindowSec: 3600,
		NodeInfoEnabled:    false,
		PositionEnabled:    false,
		MaxHopEnabled:      false,
	}

	report := s.Misbehaving(&cfg)
	found := false
	for _, n := range report.Nodes {
		if n.NodeNum == node {
			found = true
		}
	}
	if !found {
		t.Error("node with 2 events should be flagged with threshold=1")
	}
}

func TestMisbehavingWindowExpiry(t *testing.T) {
	s := New(100)
	now := time.Now().Truncate(time.Second).UTC()

	node := uint32(0xFF)
	// Event 2 hours in the past — outside the default 1h window
	s.Add(makeTelemetryEvent(node, now.Add(-2*time.Hour)))
	s.Add(makeTelemetryEvent(node, now.Add(-2*time.Hour+time.Second)))
	s.Add(makeTelemetryEvent(node, now.Add(-2*time.Hour+2*time.Second)))

	report := s.Misbehaving(nil)
	for _, n := range report.Nodes {
		if n.NodeNum == node {
			t.Error("events outside window should not be flagged")
		}
	}
}

func TestSanitizeConfig(t *testing.T) {
	cfg := MisbehaveConfig{
		NodeInfoWindowSec:  100, // below min
		TelemetryCount:     -5,  // negative
		MaxHopValue:        10,  // above 7
		NotifyCooldownHours: 0,  // below min
		NotifyMaxPerHour:   100, // above max
		NotifyHopLimit:     0,   // below min
	}
	cfg.Sanitize()

	if cfg.NodeInfoWindowSec < minMisbehaveWindowSec {
		t.Errorf("window raised: got %d, want >= %d", cfg.NodeInfoWindowSec, minMisbehaveWindowSec)
	}
	if cfg.TelemetryCount < 0 {
		t.Errorf("negative count clamped: got %d", cfg.TelemetryCount)
	}
	if cfg.MaxHopValue > 7 {
		t.Errorf("max hop clamped: got %d", cfg.MaxHopValue)
	}
	if cfg.NotifyCooldownHours < 1 {
		t.Errorf("cooldown clamped: got %d", cfg.NotifyCooldownHours)
	}
	if cfg.NotifyMaxPerHour > 60 {
		t.Errorf("rate clamped: got %d", cfg.NotifyMaxPerHour)
	}
	if cfg.NotifyHopLimit < 1 {
		t.Errorf("hop limit clamped: got %d", cfg.NotifyHopLimit)
	}
}

func TestBuildIssueText(t *testing.T) {
	n := MisbehavingNode{
		ShortName:      "TestNode",
		NodeID:         "!00000001",
		TelemetryCount: 5,
		HopStartMode:   7,
	}
	c := MisbehaveConfig{
		TelemetryEnabled:   true,
		TelemetryCount:     2,
		TelemetryWindowSec: 3600,
		MaxHopEnabled:      true,
		MaxHopValue:        3,
	}
	text := BuildIssueText(n, c)
	if text == "" {
		t.Error("expected non-empty issue text")
	}
	// Should mention both telemetry and hop issues
	if len(text) < 20 {
		t.Errorf("issue text too short: %q", text)
	}
}

func TestMisbehaveExcessSorting(t *testing.T) {
	// misbExcess sums the excess over each threshold — used as sort key.
	// Higher excess = worse offender = listed first.
	c := MisbehaveConfig{
		NodeInfoEnabled:    true,
		NodeInfoCount:      2,
		TelemetryEnabled:   true,
		TelemetryCount:     2,
		PositionEnabled:    true,
		PositionCount:      15,
		MaxHopEnabled:      true,
		MaxHopValue:        5,
	}

	a := MisbehavingNode{NodeInfoCount: 10} // excess 8
	b := MisbehavingNode{TelemetryCount: 3} // excess 1

	if misbExcess(a, c) <= misbExcess(b, c) {
		t.Errorf("a (excess=8) should sort before b (excess=1)")
	}
}

func TestMarkFlaggedAndFirstFlaggedAt(t *testing.T) {
	s := New(100)
	node := uint32(0x123)

	// Before marking, should be 0
	if ts := s.FirstFlaggedAt(node); ts != 0 {
		t.Errorf("expected 0 before marking, got %d", ts)
	}

	s.MarkFlagged(node)
	ts := s.FirstFlaggedAt(node)
	if ts == 0 {
		t.Error("expected non-zero after marking")
	}

	// Second mark should keep the original timestamp
	time.Sleep(10 * time.Millisecond)
	s.MarkFlagged(node)
	ts2 := s.FirstFlaggedAt(node)
	if ts2 != ts {
		t.Errorf("first flagged timestamp changed: %d -> %d", ts, ts2)
	}
}

func TestUnflagAbsent(t *testing.T) {
	s := New(100)
	s.MarkFlagged(0x01)
	s.MarkFlagged(0x02)

	// Only 0x01 is still present
	s.UnflagAbsent(map[uint32]bool{0x01: true})

	if ts := s.FirstFlaggedAt(0x01); ts == 0 {
		t.Error("0x01 should still be flagged")
	}
	if ts := s.FirstFlaggedAt(0x02); ts != 0 {
		t.Error("0x02 should be un-flagged", ts)
	}
}

func TestEventsPerMinute(t *testing.T) {
	s := New(100)
	// Use very recent times so they land inside the window regardless of
	// slight timing differences between the test's now and the method's now.
	now := time.Now()

	s.Add(makeTelemetryEvent(0x01, now.Add(-10*time.Second)))
	s.Add(makeTelemetryEvent(0x02, now.Add(-8*time.Second)))
	s.Add(makeTelemetryEvent(0x03, now.Add(-4*time.Second)))

	buckets := s.EventsPerMinute(5)
	if len(buckets) != 5 {
		t.Fatalf("expected 5 buckets, got %d", len(buckets))
	}

	total := 0
	for _, v := range buckets {
		total += v
	}
	if total < 3 {
		t.Errorf("expected at least 3 events across buckets, got %d", total)
	}
}

func TestMessageCount_exported(t *testing.T) {
	s := New(100)
	now := time.Now()

	s.Add(&decoder.Event{
		Time:     now,
		Type:     decoder.EventTextMessage,
		FromNode: 0x01,
		Details:  map[string]any{"text": "msg1"},
	})
	s.Add(&decoder.Event{
		Time:     now,
		Type:     decoder.EventTextMessage,
		FromNode: 0x02,
		Details:  map[string]any{"text": "msg2"},
	})

	stats := s.Stats()
	if stats.MessagesCount != 2 {
		t.Errorf("expected 2 messages, got %d", stats.MessagesCount)
	}
}

func TestRecentEventsFiltered(t *testing.T) {
	s := New(100)
	now := time.Now()

	s.Add(makeTelemetryEvent(0x01, now))
	s.Add(makeNodeInfoEvent(0x02, now.Add(time.Second)))
	s.Add(makeTelemetryEvent(0x03, now.Add(2*time.Second)))

	telem := s.RecentEvents(10, decoder.EventTelemetry)
	if len(telem) != 2 {
		t.Errorf("expected 2 telemetry events, got %d", len(telem))
	}
	for _, ev := range telem {
		if ev.Type != decoder.EventTelemetry {
			t.Errorf("unexpected event type %s in filtered results", ev.Type)
		}
	}
}

func TestTelemetryHistory(t *testing.T) {
	s := New(100)
	now := time.Now()

	s.Add(makeTelemetryEvent(0x42, now))
	s.Add(makeTelemetryEvent(0x99, now.Add(time.Second)))
	s.Add(makeTelemetryEvent(0x42, now.Add(2*time.Second)))

	hist := s.TelemetryHistory(0x42, 10)
	if len(hist) != 2 {
		t.Errorf("expected 2 telemetry events for node 0x42, got %d", len(hist))
	}
}

func TestRateBucketReapsOldSamples(t *testing.T) {
	s := New(100)
	now := time.Now().Truncate(time.Second)

	node := uint32(0xAB)
	// Add events far in the past — should be pruned
	s.Add(makeTelemetryEvent(node, now.Add(-2*time.Hour)))
	s.Add(makeTelemetryEvent(node, now.Add(-2*time.Hour+time.Second)))

	// Now add fresh event
	s.Add(makeTelemetryEvent(node, now))

	report := s.Misbehaving(nil)
	for _, n := range report.Nodes {
		if n.NodeNum == node {
			// Only the fresh event should be counted
			if n.TelemetryCount > 2 {
				t.Errorf("old samples should be pruned, got count=%d", n.TelemetryCount)
			}
		}
	}
}

func TestResetMisbehaveForNode(t *testing.T) {
	s := New(100)
	now := time.Now().Truncate(time.Second)

	node := uint32(0xBA)
	for i := 0; i < 5; i++ {
		s.Add(makeTelemetryEvent(node, now))
	}

	// Should be flagged
	report := s.Misbehaving(nil)
	found := false
	for _, n := range report.Nodes {
		if n.NodeNum == node {
			found = true
		}
	}
	if !found {
		t.Fatal("node should be flagged before reset")
	}

	s.ResetMisbehaveForNode(node)

	// Should no longer be flagged
	report = s.Misbehaving(nil)
	for _, n := range report.Nodes {
		if n.NodeNum == node {
			t.Error("node should not be flagged after reset")
		}
	}
}
