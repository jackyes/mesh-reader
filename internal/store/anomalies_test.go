package store

import (
	"testing"
	"time"

	"mesh-reader/internal/decoder"
)

// findAnomaly returns the most recent anomaly of the given type from the
// store ring buffer, or nil if none. Helper for tests.
func findAnomaly(s *Store, typ string) *Anomaly {
	for _, a := range s.Anomalies(50) {
		if a.Type == typ {
			cp := a
			return &cp
		}
	}
	return nil
}

func TestAnomalyRoutingLoop(t *testing.T) {
	s := New(100)
	now := time.Now()

	// 3 receptions of the same (from, pid) within 60s → loop.
	for i := 0; i < routingLoopThreshold; i++ {
		s.Add(&decoder.Event{
			Time:     now.Add(time.Duration(i) * time.Second),
			Type:     decoder.EventTextMessage,
			FromNode: 0xB001,
			PacketID: 42,
			Details:  map[string]any{},
		})
	}
	if a := findAnomaly(s, AnomalyRoutingLoop); a == nil {
		t.Fatal("expected routing_loop anomaly, got none")
	} else if a.NodeNum != 0xB001 || int(a.Value) < routingLoopThreshold {
		t.Errorf("routing_loop fields wrong: %+v", a)
	}
}

func TestAnomalyRoutingLoopNotFiredOnSingleReception(t *testing.T) {
	s := New(100)
	s.Add(&decoder.Event{
		Time:     time.Now(),
		Type:     decoder.EventTextMessage,
		FromNode: 0xB001,
		PacketID: 42,
		Details:  map[string]any{},
	})
	if a := findAnomaly(s, AnomalyRoutingLoop); a != nil {
		t.Errorf("unexpected routing_loop anomaly on single reception: %+v", a)
	}
}

func TestAnomalyGhostRelay(t *testing.T) {
	s := New(100)
	// Seed one known node whose last byte is 0x01.
	s.Add(&decoder.Event{
		Time:     time.Now(),
		Type:     decoder.EventNodeInfo,
		FromNode: 0xAAAA0001,
		Details:  map[string]any{"short_name": "AAAA"},
	})

	// Packet with relay_node = 0xFE — no known node ends in 0xFE.
	s.Add(&decoder.Event{
		Time:      time.Now(),
		Type:      decoder.EventTextMessage,
		FromNode:  0xAAAA0001,
		ToNode:    0xFFFFFFFF,
		RelayNode: 0xFE,
		Details:   map[string]any{},
	})
	if a := findAnomaly(s, AnomalyGhostRelay); a == nil {
		t.Fatal("expected ghost_relay anomaly, got none")
	} else if uint8(a.Value) != 0xFE {
		t.Errorf("ghost_relay value should be relay byte 0xFE, got %v", a.Value)
	}
}

func TestAnomalyGhostRelayNotFiredOnKnownRelay(t *testing.T) {
	s := New(100)
	// Seed a known node whose last byte is 0x42.
	s.Add(&decoder.Event{
		Time:     time.Now(),
		Type:     decoder.EventNodeInfo,
		FromNode: 0xAAAA0042,
		Details:  map[string]any{"short_name": "AAAA"},
	})
	// Packet relayed by ..42 — should NOT fire.
	s.Add(&decoder.Event{
		Time:      time.Now(),
		Type:      decoder.EventTextMessage,
		FromNode:  0xB001,
		ToNode:    0xFFFFFFFF,
		RelayNode: 0x42,
		Details:   map[string]any{},
	})
	if a := findAnomaly(s, AnomalyGhostRelay); a != nil {
		t.Errorf("unexpected ghost_relay for a known relay: %+v", a)
	}
}

func TestAnomalyMQTTMirror(t *testing.T) {
	s := New(100)
	now := time.Now()
	// Radio observation first.
	s.Add(&decoder.Event{
		Time:     now,
		Type:     decoder.EventTelemetry,
		FromNode: 0xB001,
		ViaMqtt:  false,
		Details:  map[string]any{},
	})
	// Then an MQTT observation within the window → should fire.
	s.Add(&decoder.Event{
		Time:     now.Add(5 * time.Second),
		Type:     decoder.EventTelemetry,
		FromNode: 0xB001,
		ViaMqtt:  true,
		Details:  map[string]any{},
	})
	if a := findAnomaly(s, AnomalyMQTTMirror); a == nil {
		t.Fatal("expected mqtt_mirror anomaly, got none")
	} else if a.NodeNum != 0xB001 {
		t.Errorf("mqtt_mirror NodeNum wrong: %+v", a)
	}
}

func TestAnomalyMQTTMirrorOnlyOneSourceDoesNotFire(t *testing.T) {
	s := New(100)
	now := time.Now()
	// Many radio-only packets — never any mqtt — must NOT fire.
	for i := 0; i < 5; i++ {
		s.Add(&decoder.Event{
			Time:     now.Add(time.Duration(i) * time.Second),
			Type:     decoder.EventTelemetry,
			FromNode: 0xB001,
			ViaMqtt:  false,
			Details:  map[string]any{},
		})
	}
	if a := findAnomaly(s, AnomalyMQTTMirror); a != nil {
		t.Errorf("unexpected mqtt_mirror anomaly: %+v", a)
	}
}

func TestAnomalyCrossChannel(t *testing.T) {
	s := New(100)
	now := time.Now()
	// Same node emits first on channel 0, then on channel 3 → cross-channel.
	s.Add(&decoder.Event{
		Time:     now,
		Type:     decoder.EventTextMessage,
		FromNode: 0xB001,
		Channel:  0,
		Details:  map[string]any{},
	})
	s.Add(&decoder.Event{
		Time:     now.Add(time.Second),
		Type:     decoder.EventTextMessage,
		FromNode: 0xB001,
		Channel:  3,
		Details:  map[string]any{},
	})
	if a := findAnomaly(s, AnomalyCrossChannel); a == nil {
		t.Fatal("expected cross_channel anomaly, got none")
	} else if int(a.Value) != 2 {
		t.Errorf("cross_channel value (#channels) wrong: got %v want 2", a.Value)
	}
}

func TestAnomalyCrossChannelSingleChannelDoesNotFire(t *testing.T) {
	s := New(100)
	now := time.Now()
	for i := 0; i < 10; i++ {
		s.Add(&decoder.Event{
			Time:     now.Add(time.Duration(i) * time.Second),
			Type:     decoder.EventTextMessage,
			FromNode: 0xB001,
			Channel:  0, // same channel every time
			Details:  map[string]any{},
		})
	}
	if a := findAnomaly(s, AnomalyCrossChannel); a != nil {
		t.Errorf("unexpected cross_channel on single-channel emission: %+v", a)
	}
}

func TestAnomalyEncryptedSpike(t *testing.T) {
	s := New(2000)
	now := time.Now()
	// Need >= encryptedSpikeMinTotal events, >=30% encrypted.
	// Build a stream of 100 packets with 50 encrypted (50% > 30%).
	for i := 0; i < encryptedSpikeMinTotal/2; i++ {
		s.Add(&decoder.Event{
			Time:     now.Add(time.Duration(i) * time.Millisecond),
			Type:     decoder.EventTextMessage,
			FromNode: uint32(0xB000 + i),
			Details:  map[string]any{},
		})
		s.Add(&decoder.Event{
			Time:     now.Add(time.Duration(i) * time.Millisecond),
			Type:     decoder.EventEncrypted,
			FromNode: uint32(0xC000 + i),
			Details:  map[string]any{},
		})
	}
	if a := findAnomaly(s, AnomalyEncryptedSpike); a == nil {
		t.Fatal("expected encrypted_spike anomaly, got none")
	} else if a.Value < encryptedSpikeThreshold*100 {
		t.Errorf("encrypted_spike ratio too low: %v", a.Value)
	}
}

func TestAnomalyEncryptedSpikeBelowMinSampleDoesNotFire(t *testing.T) {
	s := New(100)
	now := time.Now()
	// Only 10 encrypted out of 10 total — 100% encrypted but sample too small.
	for i := 0; i < 10; i++ {
		s.Add(&decoder.Event{
			Time:     now.Add(time.Duration(i) * time.Second),
			Type:     decoder.EventEncrypted,
			FromNode: uint32(0xC000 + i),
			Details:  map[string]any{},
		})
	}
	if a := findAnomaly(s, AnomalyEncryptedSpike); a != nil {
		t.Errorf("unexpected encrypted_spike below min sample: %+v", a)
	}
}

func TestAnomalyHopViolation(t *testing.T) {
	s := New(100)
	// HopLimit=5 > HopStart=3 — impossible in correct firmware.
	s.Add(&decoder.Event{
		Time:     time.Now(),
		Type:     decoder.EventTextMessage,
		FromNode: 0xB001,
		PacketID: 42,
		HopStart: 3,
		HopLimit: 5,
		Details:  map[string]any{},
	})
	if a := findAnomaly(s, AnomalyHopViolation); a == nil {
		t.Fatal("expected hop_violation, got none")
	} else if int(a.Value) != 5 {
		t.Errorf("hop_violation value should be hop_limit=5, got %v", a.Value)
	}
}

func TestAnomalyHopViolationNormalPacketDoesNotFire(t *testing.T) {
	s := New(100)
	// Normal: HopLimit==HopStart (fresh) and HopLimit<HopStart (relayed).
	s.Add(&decoder.Event{
		Time: time.Now(), Type: decoder.EventTextMessage, FromNode: 0xB001,
		PacketID: 10, HopStart: 3, HopLimit: 3, Details: map[string]any{},
	})
	s.Add(&decoder.Event{
		Time: time.Now(), Type: decoder.EventTextMessage, FromNode: 0xB002,
		PacketID: 11, HopStart: 3, HopLimit: 1, Details: map[string]any{},
	})
	if a := findAnomaly(s, AnomalyHopViolation); a != nil {
		t.Errorf("unexpected hop_violation on normal packets: %+v", a)
	}
}

func TestAnomalyHopViolationHopStartZeroSkipped(t *testing.T) {
	s := New(100)
	// HopStart=0 must be skipped (some older firmware paths report 0 even
	// for valid packets — would yield false positives).
	s.Add(&decoder.Event{
		Time: time.Now(), Type: decoder.EventTextMessage, FromNode: 0xB001,
		PacketID: 10, HopStart: 0, HopLimit: 3, Details: map[string]any{},
	})
	if a := findAnomaly(s, AnomalyHopViolation); a != nil {
		t.Errorf("hop_violation should be skipped when HopStart=0: %+v", a)
	}
}

func TestAnomalyIDReuse(t *testing.T) {
	s := New(100)
	t0 := time.Now()
	// First reception.
	s.Add(&decoder.Event{
		Time: t0, Type: decoder.EventTextMessage, FromNode: 0xB001,
		PacketID: 7777, Details: map[string]any{},
	})
	// Same (from, pid) more than idReuseWindow later → flag.
	s.Add(&decoder.Event{
		Time: t0.Add(idReuseWindow + time.Minute), Type: decoder.EventTextMessage,
		FromNode: 0xB001, PacketID: 7777, Details: map[string]any{},
	})
	if a := findAnomaly(s, AnomalyIDReuse); a == nil {
		t.Fatal("expected id_reuse anomaly, got none")
	} else if a.NodeNum != 0xB001 {
		t.Errorf("id_reuse NodeNum wrong: %+v", a)
	}
}

func TestAnomalyIDReuseWithinWindowDoesNotFire(t *testing.T) {
	s := New(100)
	t0 := time.Now()
	// Two receptions a few seconds apart — that's just a relay rebroadcast,
	// NOT an id_reuse anomaly.
	for i := 0; i < 2; i++ {
		s.Add(&decoder.Event{
			Time:     t0.Add(time.Duration(i) * time.Second),
			Type:     decoder.EventTextMessage,
			FromNode: 0xB001,
			PacketID: 7777,
			Details:  map[string]any{},
		})
	}
	if a := findAnomaly(s, AnomalyIDReuse); a != nil {
		t.Errorf("unexpected id_reuse for closely-spaced receptions: %+v", a)
	}
}

func TestAnomalyPairFlood(t *testing.T) {
	s := New(500)
	now := time.Now()
	// Two nodes exchange pairFloodMinCount packets in well under 5 minutes.
	for i := 0; i < pairFloodMinCount; i++ {
		from := uint32(0xB001)
		to := uint32(0xB002)
		if i%2 == 1 {
			from, to = to, from
		}
		s.Add(&decoder.Event{
			Time:     now.Add(time.Duration(i) * time.Second),
			Type:     decoder.EventTextMessage,
			FromNode: from,
			ToNode:   to,
			PacketID: uint32(1000 + i),
			Details:  map[string]any{},
		})
	}
	if a := findAnomaly(s, AnomalyPairFlood); a == nil {
		t.Fatal("expected pair_flood anomaly, got none")
	} else if int(a.Value) < pairFloodMinCount {
		t.Errorf("pair_flood value should be >= %d, got %v", pairFloodMinCount, a.Value)
	}
}

func TestAnomalyPairSkew(t *testing.T) {
	s := New(500)
	now := time.Now()
	// A speaks 20 times to B; B never replies → 100% skew.
	for i := 0; i < pairSkewMinCount; i++ {
		s.Add(&decoder.Event{
			Time:     now.Add(time.Duration(i) * time.Second),
			Type:     decoder.EventTextMessage,
			FromNode: 0xB001,
			ToNode:   0xB002,
			PacketID: uint32(2000 + i),
			Details:  map[string]any{},
		})
	}
	if a := findAnomaly(s, AnomalyPairSkew); a == nil {
		t.Fatal("expected pair_skew anomaly, got none")
	}
}

func TestAnomalyPairSkewBalancedDoesNotFire(t *testing.T) {
	s := New(500)
	now := time.Now()
	// A and B alternate — perfectly balanced, no skew.
	for i := 0; i < pairSkewMinCount; i++ {
		from := uint32(0xB001)
		to := uint32(0xB002)
		if i%2 == 1 {
			from, to = to, from
		}
		s.Add(&decoder.Event{
			Time:     now.Add(time.Duration(i) * time.Second),
			Type:     decoder.EventTextMessage,
			FromNode: from,
			ToNode:   to,
			PacketID: uint32(3000 + i),
			Details:  map[string]any{},
		})
	}
	if a := findAnomaly(s, AnomalyPairSkew); a != nil {
		t.Errorf("unexpected pair_skew on balanced pair: %+v", a)
	}
}

// ---- Spammer detection (>30 pkts/min) ----

func TestAnomalySpammer(t *testing.T) {
	s := New(200)
	now := time.Now()
	for i := 0; i < 31; i++ {
		s.Add(&decoder.Event{
			Time: now.Add(time.Duration(i) * time.Second),
			Type: decoder.EventTextMessage, FromNode: 0xABC,
			PacketID: uint32(i + 1), Details: map[string]any{},
		})
	}
	if a := findAnomaly(s, AnomalySpammer); a == nil {
		t.Error("expected spammer anomaly for 31 pkt/min")
	}
}

func TestAnomalySpammerBelowThreshold(t *testing.T) {
	s := New(200)
	now := time.Now()
	for i := 0; i < 30; i++ {
		s.Add(&decoder.Event{
			Time: now.Add(time.Duration(i) * time.Second),
			Type: decoder.EventTextMessage, FromNode: 0xDEF,
			PacketID: uint32(i + 1), Details: map[string]any{},
		})
	}
	if a := findAnomaly(s, AnomalySpammer); a != nil {
		t.Errorf("30 pkt/min should NOT trigger spammer: %+v", a)
	}
}

// ---- SNR jump detection (>=20 dB within 5 min) ----

func TestAnomalySNRJump(t *testing.T) {
	s := New(200)
	now := time.Now()
	s.Add(&decoder.Event{
		Time: now, Type: decoder.EventTextMessage, FromNode: 0xF01, SNR: 10,
		PacketID: 1, Details: map[string]any{},
	})
	s.Add(&decoder.Event{
		Time: now.Add(2 * time.Minute), Type: decoder.EventTextMessage, FromNode: 0xF01, SNR: -12,
		PacketID: 2, Details: map[string]any{},
	})
	if a := findAnomaly(s, AnomalySNRJump); a == nil {
		t.Error("expected snr_jump for 22 dB change in 2 min")
	}
}

func TestAnomalySNRJumpBelowThreshold(t *testing.T) {
	s := New(200)
	now := time.Now()
	s.Add(&decoder.Event{
		Time: now, Type: decoder.EventTextMessage, FromNode: 0xF02, SNR: 10,
		PacketID: 1, Details: map[string]any{},
	})
	s.Add(&decoder.Event{
		Time: now.Add(2 * time.Minute), Type: decoder.EventTextMessage, FromNode: 0xF02, SNR: -8,
		PacketID: 2, Details: map[string]any{},
	})
	if a := findAnomaly(s, AnomalySNRJump); a != nil {
		t.Errorf("18 dB change should NOT trigger snr_jump: %+v", a)
	}
}

func TestAnomalySNRJumpOutsideWindow(t *testing.T) {
	s := New(200)
	now := time.Now()
	s.Add(&decoder.Event{
		Time: now, Type: decoder.EventTextMessage, FromNode: 0xF03, SNR: 10,
		PacketID: 1, Details: map[string]any{},
	})
	s.Add(&decoder.Event{
		Time: now.Add(6 * time.Minute), Type: decoder.EventTextMessage, FromNode: 0xF03, SNR: -12,
		PacketID: 2, Details: map[string]any{},
	})
	if a := findAnomaly(s, AnomalySNRJump); a != nil {
		t.Errorf("SNR change outside 5-min window should NOT trigger: %+v", a)
	}
}

func TestAnomalySNRJumpSNRZeroSkipped(t *testing.T) {
	s := New(200)
	now := time.Now()
	s.Add(&decoder.Event{
		Time: now, Type: decoder.EventTextMessage, FromNode: 0xF04, SNR: 0,
		PacketID: 1, Details: map[string]any{},
	})
	s.Add(&decoder.Event{
		Time: now.Add(time.Minute), Type: decoder.EventTextMessage, FromNode: 0xF04, SNR: 25,
		PacketID: 2, Details: map[string]any{},
	})
	if a := findAnomaly(s, AnomalySNRJump); a != nil {
		t.Errorf("SNR=0 should be skipped, so no jump: %+v", a)
	}
}

// ---- GPS teleport detection ----

func TestAnomalyGPSTeleport(t *testing.T) {
	s := New(200)
	now := time.Now()
	s.Add(&decoder.Event{
		Time: now, Type: decoder.EventPosition, FromNode: 0x700,
		PacketID: 1,
		Details:  map[string]any{"lat": 45.464, "lon": 9.190, "altitude_m": int32(120)},
	})
	s.Add(&decoder.Event{
		Time: now.Add(30 * time.Minute), Type: decoder.EventPosition, FromNode: 0x700,
		PacketID: 2,
		Details:  map[string]any{"lat": 41.893, "lon": 12.483, "altitude_m": int32(50)},
	})
	if a := findAnomaly(s, AnomalyGPSTeleport); a == nil {
		t.Error("expected gps_teleport for ~940 km/h jump")
	}
}

func TestAnomalyGPSTeleportSmallJump(t *testing.T) {
	s := New(200)
	now := time.Now()
	s.Add(&decoder.Event{
		Time: now, Type: decoder.EventPosition, FromNode: 0x701,
		PacketID: 1,
		Details:  map[string]any{"lat": 45.464, "lon": 9.190},
	})
	s.Add(&decoder.Event{
		Time: now.Add(30 * time.Minute), Type: decoder.EventPosition, FromNode: 0x701,
		PacketID: 2,
		Details:  map[string]any{"lat": 45.554, "lon": 9.190},
	})
	if a := findAnomaly(s, AnomalyGPSTeleport); a != nil {
		t.Errorf("small jump should NOT trigger teleport: %+v", a)
	}
}

func TestAnomalyGPSTeleportNoPositionType(t *testing.T) {
	s := New(200)
	now := time.Now()
	s.Add(&decoder.Event{
		Time: now, Type: decoder.EventNodeInfo, FromNode: 0x702,
		PacketID: 1,
		Details:  map[string]any{"lat": 45.464, "lon": 9.190},
	})
	s.Add(&decoder.Event{
		Time: now.Add(10 * time.Minute), Type: decoder.EventNodeInfo, FromNode: 0x702,
		PacketID: 2,
		Details:  map[string]any{"lat": 41.893, "lon": 12.483},
	})
	if a := findAnomaly(s, AnomalyGPSTeleport); a != nil {
		t.Errorf("non-Position events should NOT trigger teleport: %+v", a)
	}
}
