// Package store — anomaly detection module.
//
// Anomaly types tracked:
//   - GPS teleport: a node's reported position jumps by >50 km at >200 km/h
//     sustained speed (likely GPS spoof, time-warp, or stale cached fix).
//   - Spammer: a single sender emits >30 packets/min sustained (firmware bug,
//     misconfigured client, or actual abuse).
//   - SNR jump: SNR for a known sender swings by >20 dB in <5 minutes
//     (interference event, antenna disconnect, sudden propagation change).
//   - Routing loop: same (from_node, packet_id) received ≥3 times in 60s,
//     indicating a flood / mesh storm / mis-rebroadcast loop.
//   - Ghost relay: a packet's relay_node byte does not match any known node
//     (silent forwarder we have never heard speak directly, or spoofed id).
//   - MQTT mirror: a node is observed in both radio and MQTT origin in the
//     same minute, i.e. an MQTT bridge is rebroadcasting native radio traffic
//     (creates duplicates + saturates airtime).
//   - Cross-channel: a single node emits on more than one channel index
//     within an hour — almost always misconfiguration.
//   - Encrypted spike: ≥30% of decoded packets in the last hour were
//     ENCRYPTED, suggesting a missing channel key or a foreign PSK.
//   - Pair skew: a high-volume A↔B conversation that is almost entirely
//     one-sided (e.g. A keeps DMing B but B never replies) — common when
//     B is offline or a one-way sensor was misconfigured to use unicast.
//   - Pair flood: a brand-new pair has already accumulated many packets
//     in the first few minutes (genuine new node bursts in fast, but >30
//     packets in <5 min between previously-silent nodes is suspicious).
//   - Hop violation: a packet whose HopLimit exceeds its HopStart, which
//     is impossible in firmware that builds the header correctly →
//     corruption, replay, or spoofing.
//   - Packet-id reuse: same (from, packet_id) heard ≥30 min apart — IDs
//     are random uint32 so natural collisions are ~1 in 4 billion. Real
//     causes are sender reboot, duplicate node_id on the mesh, or replay.
//
// All anomalies are kept in a 200-slot ring buffer in memory and exposed
// via /api/anomalies. Each (type, node) pair is rate-limited to one
// anomaly per dedup window to avoid flooding the UI.
package store

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"mesh-reader/internal/decoder"
)

// Anomaly type constants used by the API/UI.
const (
	AnomalyGPSTeleport     = "gps_teleport"
	AnomalySpammer         = "spammer"
	AnomalySNRJump         = "snr_jump"
	AnomalyRoutingLoop     = "routing_loop"
	AnomalyGhostRelay      = "ghost_relay"
	AnomalyMQTTMirror      = "mqtt_mirror"
	AnomalyCrossChannel    = "cross_channel"
	AnomalyEncryptedSpike  = "encrypted_spike"
	AnomalyPairSkew        = "pair_skew"
	AnomalyPairFlood       = "pair_flood"
	AnomalyHopViolation    = "hop_violation"
	AnomalyIDReuse         = "id_reuse"
)

// Anomaly is a flagged event worth surfacing to the operator.
type Anomaly struct {
	Time     int64   `json:"time"`     // unix seconds
	Type     string  `json:"type"`     // gps_teleport / spammer / snr_jump
	NodeNum  uint32  `json:"node_num"`
	NodeName string  `json:"node_name,omitempty"`
	Severity string  `json:"severity"` // info / warning / critical
	Message  string  `json:"message"`
	Value    float64 `json:"value,omitempty"` // numeric value (km, pkt/min, dB)
}

// Tunable thresholds. Adjust if the network legitimately has high-mobility
// or high-rate nodes (e.g. trackers).
const (
	teleportKmAbsolute = 50.0  // ignore small jumps (GPS noise)
	teleportKmPerHour  = 200.0 // sustained speed cap (faster than any vehicle / aircraft handheld)
	spammerPktPerMin   = 30
	snrJumpDb          = 20.0

	// Routing loop: ≥3 receptions of the same (from, pid) inside the
	// window. With a healthy mesh and dedup, a unicast packet should be
	// heard once (or twice if we also hear a single relay rebroadcast).
	routingLoopThreshold = 3
	routingLoopWindow    = 60 * time.Second

	// MQTT mirror: how long a node may have radio-origin packets co-existing
	// with mqtt-origin packets before we consider it a bridge mirror.
	mqttMirrorWindow = 60 * time.Second

	// Cross-channel: a node emitting on more than one channel index in this
	// rolling window is suspicious. We use 1h so brief recovery operations
	// (channel switch + reboot) don't trigger a false positive.
	crossChannelWindow = 1 * time.Hour

	// Encrypted spike: global ratio over a 60-min sliding window with a
	// minimum sample size so a quiet mesh with a single encrypted packet
	// doesn't trigger.
	encryptedSpikeWindow    = 60 * time.Minute
	encryptedSpikeMinTotal  = 100
	encryptedSpikeThreshold = 0.30

	// Pair skew: high-volume conversation that is almost entirely one-sided.
	// Below 20 packets the ratio is too noisy to be meaningful.
	pairSkewMinCount = 20
	pairSkewRatio    = 0.95

	// Pair flood: a fresh pair that has already accumulated many packets.
	// FirstSeen must be within 5 min for the anomaly to fire.
	pairFloodWindow   = 5 * time.Minute
	pairFloodMinCount = 30

	// Packet-id reuse: same (from, pid) heard ≥30 min apart. With random
	// 32-bit IDs natural collisions are vanishingly rare; the realistic
	// causes are sender reboot, duplicate node_id, or replay.
	idReuseWindow    = 30 * time.Minute
	idReuseGCWindow  = 2 * time.Hour
	idReuseMaxEntries = 8192
)

type anomLastPos struct {
	lat, lon float64
	t        time.Time
}
type anomLastSNR struct {
	snr float32
	t   time.Time
}

// mqttBuckets keeps separate radio vs MQTT reception timestamps for one node.
type mqttBuckets struct {
	radio []time.Time // packets observed with via_mqtt=false
	mqtt  []time.Time // packets observed with via_mqtt=true
}

// anomalyData holds anomaly detection state and a recent-anomalies ring buffer.
// Accessed under Store.mu (no separate lock needed — keeps things simple).
type anomalyData struct {
	_        sync.Mutex // tag for go vet hygiene; unused
	lastPos  map[uint32]anomLastPos
	lastSNR  map[uint32]anomLastSNR
	pktTimes map[uint32][]time.Time // recent packet timestamps per sender (last 60s)

	// Routing-loop: timestamps of receptions per (from<<32 | pid).
	pktDup map[uint64][]time.Time

	// Cross-channel: per-node observed channels and their most recent ts.
	nodeChannels map[uint32]map[uint32]time.Time

	// MQTT-mirror: per-node split radio/mqtt timestamps.
	mqttSeen map[uint32]*mqttBuckets

	// Encrypted-spike: rolling-window total/encrypted timestamps (60min).
	totalSeen     []time.Time
	encryptedSeen []time.Time

	// ID-reuse: first time each (from<<32 | pid) was observed. GC'd when the
	// map grows past idReuseMaxEntries by removing entries older than
	// idReuseGCWindow.
	pktFirstSeen map[uint64]time.Time

	flagged map[string]time.Time // dedup key -> last flagged
	ring    []Anomaly
	head    int
	count   int
	cap     int
}

func newAnomalyData() *anomalyData {
	const cap = 200
	return &anomalyData{
		lastPos:      make(map[uint32]anomLastPos),
		lastSNR:      make(map[uint32]anomLastSNR),
		pktTimes:     make(map[uint32][]time.Time),
		pktDup:       make(map[uint64][]time.Time),
		nodeChannels: make(map[uint32]map[uint32]time.Time),
		mqttSeen:     make(map[uint32]*mqttBuckets),
		pktFirstSeen: make(map[uint64]time.Time),
		flagged:      make(map[string]time.Time),
		ring:         make([]Anomaly, cap),
		cap:          cap,
	}
}

// detectAnomalies inspects an event and stores any anomalies in the ring.
// MUST be called with s.mu held.
func (s *Store) detectAnomalies(ev *decoder.Event) {
	if ev == nil || ev.FromNode == 0 {
		return
	}
	if s.anom == nil {
		s.anom = newAnomalyData()
	}
	a := s.anom
	now := ev.Time
	if now.IsZero() {
		now = time.Now()
	}
	nodeName := s.resolveNodeName(ev.FromNode)

	// 1) Spammer detection — count packets in last 60s per sender.
	times := append(a.pktTimes[ev.FromNode], now)
	cutoff := now.Add(-60 * time.Second)
	i := 0
	for i < len(times) && times[i].Before(cutoff) {
		i++
	}
	times = times[i:]
	a.pktTimes[ev.FromNode] = times
	if len(times) > spammerPktPerMin {
		s.flagAnomaly(Anomaly{
			Time:     now.Unix(),
			Type:     AnomalySpammer,
			NodeNum:  ev.FromNode,
			NodeName: nodeName,
			Severity: "warning",
			Value:    float64(len(times)),
			Message:  fmt.Sprintf("%s emits %d pkt/min (threshold %d)", nodeName, len(times), spammerPktPerMin),
		}, fmt.Sprintf("spam:%d", ev.FromNode), 5*time.Minute)
	}

	// 2) SNR jump detection.
	if ev.SNR != 0 {
		if prev, ok := a.lastSNR[ev.FromNode]; ok {
			dt := now.Sub(prev.t)
			if dt > 0 && dt < 5*time.Minute {
				delta := math.Abs(float64(ev.SNR - prev.snr))
				if delta >= snrJumpDb {
					s.flagAnomaly(Anomaly{
						Time:     now.Unix(),
						Type:     AnomalySNRJump,
						NodeNum:  ev.FromNode,
						NodeName: nodeName,
						Severity: "info",
						Value:    delta,
						Message: fmt.Sprintf("%s SNR jumped %.1f dB in %.0fs (%.1f → %.1f)",
							nodeName, delta, dt.Seconds(), prev.snr, ev.SNR),
					}, fmt.Sprintf("snr:%d", ev.FromNode), 2*time.Minute)
				}
			}
		}
		a.lastSNR[ev.FromNode] = anomLastSNR{snr: ev.SNR, t: now}
	}

	// 4) Routing loop / mesh storm.
	s.detectRoutingLoop(ev, now, nodeName)

	// 5) Ghost relay (relay byte does not match any known node).
	s.detectGhostRelay(ev, now)

	// 6) MQTT mirror flooding.
	s.detectMQTTMirror(ev, now, nodeName)

	// 7) Cross-channel emission.
	s.detectCrossChannel(ev, now, nodeName)

	// 8) Encrypted spike (global, not per-node).
	s.detectEncryptedSpike(ev, now)

	// 9) Hop limit > hop start (impossible in correct firmware).
	s.detectHopViolation(ev, now, nodeName)

	// 10) Packet-id reuse separated by ≥30 min (reboot / duplicate / replay).
	s.detectIDReuse(ev, now, nodeName)

	// 11/12) Pair-level checks (skew + flood) — run after trackPair so the
	// updated PairStat is already in s.pairs.
	s.detectPairAnomalies(ev, now)

	// 3) GPS teleport detection — only on Position events with a valid fix.
	if ev.Type == decoder.EventPosition {
		d := ev.Details
		lat, lok := d["lat"].(float64)
		lon, oOk := d["lon"].(float64)
		if lok && oOk && lat != 0 && lon != 0 {
			if prev, ok := a.lastPos[ev.FromNode]; ok {
				km := haversineKmInternal(prev.lat, prev.lon, lat, lon)
				dtH := now.Sub(prev.t).Hours()
				if dtH > 0 && km >= teleportKmAbsolute {
					speed := km / dtH
					if speed >= teleportKmPerHour {
						s.flagAnomaly(Anomaly{
							Time:     now.Unix(),
							Type:     AnomalyGPSTeleport,
							NodeNum:  ev.FromNode,
							NodeName: nodeName,
							Severity: "critical",
							Value:    km,
							Message:  fmt.Sprintf("%s moved %.1f km in %.1f h (~%.0f km/h) — possible GPS spoof", nodeName, km, dtH, speed),
						}, fmt.Sprintf("gps:%d", ev.FromNode), 30*time.Minute)
					}
				}
			}
			a.lastPos[ev.FromNode] = anomLastPos{lat: lat, lon: lon, t: now}
		}
	}
}

// flagAnomaly stores an anomaly in the ring buffer subject to a per-key
// dedup window (so the same condition doesn't flood the list).
// Caller must hold s.mu.
func (s *Store) flagAnomaly(a Anomaly, dedupKey string, dedupWindow time.Duration) {
	ad := s.anom
	if last, ok := ad.flagged[dedupKey]; ok && time.Since(last) < dedupWindow {
		return
	}
	ad.flagged[dedupKey] = time.Now()
	ad.ring[ad.head] = a
	ad.head = (ad.head + 1) % ad.cap
	if ad.count < ad.cap {
		ad.count++
	}
}

// Anomalies returns the most recent anomalies (newest first), capped at limit.
func (s *Store) Anomalies(limit int) []Anomaly {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.anom == nil || s.anom.count == 0 {
		return []Anomaly{}
	}
	if limit <= 0 || limit > s.anom.count {
		limit = s.anom.count
	}
	out := make([]Anomaly, 0, limit)
	for i := 0; i < limit; i++ {
		idx := (s.anom.head - 1 - i + s.anom.cap) % s.anom.cap
		out = append(out, s.anom.ring[idx])
	}
	return out
}

// resolveNodeName returns the best available display name for a node.
// Caller must hold s.mu (read or write).
func (s *Store) resolveNodeName(num uint32) string {
	if n, ok := s.nodes[num]; ok {
		if n.LongName != "" {
			return n.LongName
		}
		if n.ShortName != "" {
			return n.ShortName
		}
		if n.ID != "" {
			return n.ID
		}
	}
	return fmt.Sprintf("!%08x", num)
}

// haversineKmInternal computes great-circle distance in km between two points.
// Local copy so the store package has no dependency on web's helper.
func haversineKmInternal(lat1, lon1, lat2, lon2 float64) float64 {
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

// trimBefore returns ts with timestamps older than cutoff removed. Assumes
// ts is ordered ascending. Cheap because we always append at the tail.
func trimBefore(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	return ts[i:]
}

// resolveRelayNodesLocked is the unlocked variant of ResolveRelayNodes.
// Returned slice is unsorted (caller doesn't need order — only count).
// Must be called with s.mu held.
func (s *Store) resolveRelayNodesLocked(lastByte uint32) []uint32 {
	if lastByte == 0 {
		return nil
	}
	var matches []uint32
	for num := range s.nodes {
		if num&0xFF == lastByte&0xFF {
			matches = append(matches, num)
		}
	}
	return matches
}

// detectRoutingLoop fires when the same (from, packet_id) is observed
// ≥routingLoopThreshold times within routingLoopWindow. Both unicast and
// broadcast packets are tracked. Must be called with s.mu held.
func (s *Store) detectRoutingLoop(ev *decoder.Event, now time.Time, nodeName string) {
	if ev.PacketID == 0 || ev.FromNode == 0 {
		return
	}
	a := s.anom
	key := uint64(ev.FromNode)<<32 | uint64(ev.PacketID)
	cutoff := now.Add(-routingLoopWindow)
	ts := append(a.pktDup[key], now)
	ts = trimBefore(ts, cutoff)
	a.pktDup[key] = ts
	if len(ts) >= routingLoopThreshold {
		s.flagAnomaly(Anomaly{
			Time:     now.Unix(),
			Type:     AnomalyRoutingLoop,
			NodeNum:  ev.FromNode,
			NodeName: nodeName,
			Severity: "warning",
			Value:    float64(len(ts)),
			Message: fmt.Sprintf("%s pkt id=%d heard %d× in %.0fs (mesh storm / loop)",
				nodeName, ev.PacketID, len(ts), now.Sub(ts[0]).Seconds()),
		}, fmt.Sprintf("loop:%d:%d", ev.FromNode, ev.PacketID), 30*time.Minute)
	}
	// Cheap GC: when the map grows beyond ~8k entries, drop entries whose
	// last timestamp is older than the window — keeps memory bounded under
	// pathological churn.
	if len(a.pktDup) > 8192 {
		for k, v := range a.pktDup {
			if len(v) == 0 || v[len(v)-1].Before(cutoff) {
				delete(a.pktDup, k)
			}
		}
	}
}

// detectGhostRelay fires when a packet's relay_node byte does not match the
// last byte of ANY known node — i.e. it was forwarded by a node we have never
// heard speak in its own right (silent repeater or spoofed id). Must be
// called with s.mu held.
func (s *Store) detectGhostRelay(ev *decoder.Event, now time.Time) {
	if ev.RelayNode == 0 {
		return
	}
	if len(s.resolveRelayNodesLocked(ev.RelayNode)) > 0 {
		return
	}
	s.flagAnomaly(Anomaly{
		Time:     now.Unix(),
		Type:     AnomalyGhostRelay,
		NodeNum:  0,
		NodeName: fmt.Sprintf("relay ..%02x", ev.RelayNode&0xFF),
		Severity: "info",
		Value:    float64(ev.RelayNode & 0xFF),
		Message: fmt.Sprintf("packet from %s relayed by unknown node ..%02x (silent forwarder or spoofed id)",
			s.resolveNodeName(ev.FromNode), ev.RelayNode&0xFF),
	}, fmt.Sprintf("ghost:%02x", ev.RelayNode&0xFF), 1*time.Hour)
}

// detectMQTTMirror fires when the same from_node is observed with both
// via_mqtt=true and via_mqtt=false within mqttMirrorWindow — almost always
// an MQTT bridge that is rebroadcasting native-radio packets it shouldn't.
// Must be called with s.mu held.
func (s *Store) detectMQTTMirror(ev *decoder.Event, now time.Time, nodeName string) {
	if ev.FromNode == 0 {
		return
	}
	a := s.anom
	mb := a.mqttSeen[ev.FromNode]
	if mb == nil {
		mb = &mqttBuckets{}
		a.mqttSeen[ev.FromNode] = mb
	}
	cutoff := now.Add(-mqttMirrorWindow)
	mb.radio = trimBefore(mb.radio, cutoff)
	mb.mqtt = trimBefore(mb.mqtt, cutoff)
	if ev.ViaMqtt {
		mb.mqtt = append(mb.mqtt, now)
	} else {
		mb.radio = append(mb.radio, now)
	}
	if len(mb.radio) > 0 && len(mb.mqtt) > 0 {
		s.flagAnomaly(Anomaly{
			Time:     now.Unix(),
			Type:     AnomalyMQTTMirror,
			NodeNum:  ev.FromNode,
			NodeName: nodeName,
			Severity: "warning",
			Value:    float64(len(mb.radio) + len(mb.mqtt)),
			Message: fmt.Sprintf("%s observed via both radio (%d) and MQTT (%d) in <%.0fs — likely MQTT bridge mirror",
				nodeName, len(mb.radio), len(mb.mqtt), mqttMirrorWindow.Seconds()),
		}, fmt.Sprintf("mqtt:%d", ev.FromNode), 5*time.Minute)
	}
}

// detectCrossChannel fires when a single sender is observed transmitting on
// more than one channel index within crossChannelWindow. The dashboard
// surfaces both channels in the message to make remediation obvious.
// Must be called with s.mu held.
func (s *Store) detectCrossChannel(ev *decoder.Event, now time.Time, nodeName string) {
	if ev.FromNode == 0 {
		return
	}
	a := s.anom
	chans := a.nodeChannels[ev.FromNode]
	if chans == nil {
		chans = make(map[uint32]time.Time)
		a.nodeChannels[ev.FromNode] = chans
	}
	chans[ev.Channel] = now
	cutoff := now.Add(-crossChannelWindow)
	for c, t := range chans {
		if t.Before(cutoff) {
			delete(chans, c)
		}
	}
	if len(chans) <= 1 {
		return
	}
	// Build a deterministic, comma-separated channel list for the message.
	keys := make([]int, 0, len(chans))
	for c := range chans {
		keys = append(keys, int(c))
	}
	sort.Ints(keys)
	s.flagAnomaly(Anomaly{
		Time:     now.Unix(),
		Type:     AnomalyCrossChannel,
		NodeNum:  ev.FromNode,
		NodeName: nodeName,
		Severity: "warning",
		Value:    float64(len(keys)),
		Message: fmt.Sprintf("%s emitted on %d channel indexes in <1h: %v (misconfig?)",
			nodeName, len(keys), keys),
	}, fmt.Sprintf("xch:%d", ev.FromNode), 30*time.Minute)
}

// detectHopViolation flags a packet whose HopLimit exceeds its HopStart.
// HopLimit decrements from HopStart at each rebroadcast, so it must always
// be ≤ HopStart in firmware that builds the header correctly. The reverse
// indicates a corrupted/spoofed header or a buggy relay implementation.
// HopStart=0 is excluded because some older firmware paths report 0 even
// when a valid HopLimit is present, which would yield false positives.
// Must be called with s.mu held.
func (s *Store) detectHopViolation(ev *decoder.Event, now time.Time, nodeName string) {
	if ev.HopStart == 0 || ev.HopLimit <= ev.HopStart {
		return
	}
	if ev.FromNode == 0 {
		return
	}
	s.flagAnomaly(Anomaly{
		Time:     now.Unix(),
		Type:     AnomalyHopViolation,
		NodeNum:  ev.FromNode,
		NodeName: nodeName,
		Severity: "warning",
		Value:    float64(ev.HopLimit),
		Message: fmt.Sprintf("%s sent pkt id=%d with hop_limit=%d > hop_start=%d (corrupted header / replay / spoof)",
			nodeName, ev.PacketID, ev.HopLimit, ev.HopStart),
	}, fmt.Sprintf("hopv:%d", ev.FromNode), 10*time.Minute)
}

// detectIDReuse flags when the same (from, packet_id) is observed at two
// timestamps separated by ≥idReuseWindow. Packet IDs are random uint32, so
// a natural collision has probability ~1/4e9 per emit. Realistic causes
// are: (1) sender rebooted and the new RNG seed produced an old id;
// (2) two physically distinct radios share the same node_id (mis-cloning);
// (3) replay attack. The map is GC'd by age once it grows past
// idReuseMaxEntries to keep memory bounded on busy meshes.
// Must be called with s.mu held.
func (s *Store) detectIDReuse(ev *decoder.Event, now time.Time, nodeName string) {
	if ev.PacketID == 0 || ev.FromNode == 0 {
		return
	}
	a := s.anom
	key := uint64(ev.FromNode)<<32 | uint64(ev.PacketID)
	first, ok := a.pktFirstSeen[key]
	if !ok {
		a.pktFirstSeen[key] = now
	} else if dt := now.Sub(first); dt >= idReuseWindow {
		s.flagAnomaly(Anomaly{
			Time:     now.Unix(),
			Type:     AnomalyIDReuse,
			NodeNum:  ev.FromNode,
			NodeName: nodeName,
			Severity: "warning",
			Value:    dt.Minutes(),
			Message: fmt.Sprintf("%s pkt id=%d already seen %.0f min ago — reboot, duplicate node_id, or replay",
				nodeName, ev.PacketID, dt.Minutes()),
		}, fmt.Sprintf("idre:%d:%d", ev.FromNode, ev.PacketID), 1*time.Hour)
	}
	// Age-based GC: keep the map bounded. Cheap because we only walk when
	// it has actually grown past the cap.
	if len(a.pktFirstSeen) > idReuseMaxEntries {
		cutoff := now.Add(-idReuseGCWindow)
		for k, t := range a.pktFirstSeen {
			if t.Before(cutoff) {
				delete(a.pktFirstSeen, k)
			}
		}
	}
}

// detectPairAnomalies fires pair-level checks (skew + flood) using the
// PairStat just updated by trackPair. Must be called with s.mu held.
func (s *Store) detectPairAnomalies(ev *decoder.Event, now time.Time) {
	from := ev.FromNode
	to := ev.ToNode
	if from == 0 || to == 0 || to == 0xFFFFFFFF || from == to {
		return
	}
	pa, pb := from, to
	if pa > pb {
		pa, pb = pb, pa
	}
	key := uint64(pa)<<32 | uint64(pb)
	p, ok := s.pairs[key]
	if !ok {
		return
	}

	aName := s.resolveNodeName(p.NodeA)
	bName := s.resolveNodeName(p.NodeB)
	pairLabel := fmt.Sprintf("%s↔%s", aName, bName)

	// --- Pair flood: very young pair already busy.
	if p.Count >= pairFloodMinCount {
		if age := now.Sub(time.Unix(p.FirstSeen, 0)); age <= pairFloodWindow {
			s.flagAnomaly(Anomaly{
				Time:     now.Unix(),
				Type:     AnomalyPairFlood,
				NodeNum:  p.NodeA,
				NodeName: pairLabel,
				Severity: "warning",
				Value:    float64(p.Count),
				Message: fmt.Sprintf("%s exchanged %d packets in %.0fs of FirstSeen — burst / flood",
					pairLabel, p.Count, age.Seconds()),
			}, fmt.Sprintf("pflood:%d:%d", p.NodeA, p.NodeB), 30*time.Minute)
		}
	}

	// --- Pair skew: enough samples + one side dominates almost entirely.
	if p.Count >= pairSkewMinCount {
		dom := p.ATX
		quiet := p.BTX
		domName := aName
		quietName := bName
		if p.BTX > p.ATX {
			dom, quiet = p.BTX, p.ATX
			domName, quietName = bName, aName
		}
		if float64(dom)/float64(p.Count) >= pairSkewRatio {
			s.flagAnomaly(Anomaly{
				Time:     now.Unix(),
				Type:     AnomalyPairSkew,
				NodeNum:  p.NodeA,
				NodeName: pairLabel,
				Severity: "info",
				Value:    float64(dom-quiet) / float64(p.Count) * 100,
				Message: fmt.Sprintf("%s one-sided: %s spoke %d, %s spoke %d (%.0f%% skew) — peer offline?",
					pairLabel, domName, dom, quietName, quiet, float64(dom-quiet)/float64(p.Count)*100),
			}, fmt.Sprintf("pskew:%d:%d", p.NodeA, p.NodeB), 1*time.Hour)
		}
	}
}

// detectEncryptedSpike fires globally when the ratio of EventEncrypted to all
// decoded events in the last encryptedSpikeWindow exceeds the threshold AND
// the sample is large enough to be meaningful. Indicates a missing channel
// key (we hear chatter but can't read it) or a foreign PSK in range.
// Must be called with s.mu held.
func (s *Store) detectEncryptedSpike(ev *decoder.Event, now time.Time) {
	a := s.anom
	cutoff := now.Add(-encryptedSpikeWindow)
	a.totalSeen = trimBefore(append(a.totalSeen, now), cutoff)
	if ev.Type == decoder.EventEncrypted {
		a.encryptedSeen = trimBefore(append(a.encryptedSeen, now), cutoff)
	} else {
		a.encryptedSeen = trimBefore(a.encryptedSeen, cutoff)
	}
	total := len(a.totalSeen)
	enc := len(a.encryptedSeen)
	if total < encryptedSpikeMinTotal {
		return
	}
	ratio := float64(enc) / float64(total)
	if ratio < encryptedSpikeThreshold {
		return
	}
	s.flagAnomaly(Anomaly{
		Time:     now.Unix(),
		Type:     AnomalyEncryptedSpike,
		NodeNum:  0,
		NodeName: "mesh",
		Severity: "info",
		Value:    ratio * 100,
		Message: fmt.Sprintf("%.0f%% of last %d packets (%d) were ENCRYPTED — missing channel key or foreign PSK in range",
			ratio*100, total, enc),
	}, "encspike", 30*time.Minute)
}
