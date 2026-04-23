// Package store — anomaly detection module.
//
// Anomaly types tracked:
//   - GPS teleport: a node's reported position jumps by >50 km at >200 km/h
//     sustained speed (likely GPS spoof, time-warp, or stale cached fix).
//   - Spammer: a single sender emits >30 packets/min sustained (firmware bug,
//     misconfigured client, or actual abuse).
//   - SNR jump: SNR for a known sender swings by >20 dB in <5 minutes
//     (interference event, antenna disconnect, sudden propagation change).
//
// All anomalies are kept in a 200-slot ring buffer in memory and exposed
// via /api/anomalies. Each (type, node) pair is rate-limited to one
// anomaly per dedup window to avoid flooding the UI.
package store

import (
	"fmt"
	"math"
	"sync"
	"time"

	"mesh-reader/internal/decoder"
)

// Anomaly type constants used by the API/UI.
const (
	AnomalyGPSTeleport = "gps_teleport"
	AnomalySpammer     = "spammer"
	AnomalySNRJump     = "snr_jump"
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
)

type anomLastPos struct {
	lat, lon float64
	t        time.Time
}
type anomLastSNR struct {
	snr float32
	t   time.Time
}

// anomalyData holds anomaly detection state and a recent-anomalies ring buffer.
// Accessed under Store.mu (no separate lock needed — keeps things simple).
type anomalyData struct {
	_        sync.Mutex // tag for go vet hygiene; unused
	lastPos  map[uint32]anomLastPos
	lastSNR  map[uint32]anomLastSNR
	pktTimes map[uint32][]time.Time // recent packet timestamps per sender (last 60s)
	flagged  map[string]time.Time   // dedup key -> last flagged
	ring     []Anomaly
	head     int
	count    int
	cap      int
}

func newAnomalyData() *anomalyData {
	const cap = 200
	return &anomalyData{
		lastPos:  make(map[uint32]anomLastPos),
		lastSNR:  make(map[uint32]anomLastSNR),
		pktTimes: make(map[uint32][]time.Time),
		flagged:  make(map[string]time.Time),
		ring:     make([]Anomaly, cap),
		cap:      cap,
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
