// Radio-health metrics: everything derived from the firmware debug log that
// gives a view of what's happening on the radio BEFORE the firmware dedupes /
// decrypts / routes packets.
//
// These counters are separate from the normal event pipeline because:
//   - raw RX = every radio reception, including duplicates via other relays
//   - the normal event pipeline only sees 1 event per unique mesh packet
//
// The data is populated by main.go when LogRecord events arrive and the
// fwparser recognises them.

package store

import (
	"fmt"
	"sort"
	"time"

	"mesh-reader/internal/fwparser"
)

// RadioHealth is the JSON-friendly snapshot returned by Store.RadioHealth().
type RadioHealth struct {
	Enabled      bool  `json:"enabled"`       // true if we've ever received fwlog data
	FirstSeen    int64 `json:"first_seen"`    // unix time of first raw RX we parsed
	WindowSecs   int64 `json:"window_secs"`   // seconds elapsed since FirstSeen
	RawRxTotal   int   `json:"raw_rx_total"`  // all Lora RX lines parsed
	RawDupTotal  int   `json:"raw_dup_total"` // firmware "Ignore dupe" count
	RawMqttTotal int   `json:"raw_mqtt_total"`
	// DupRate is RawDupTotal / RawRxTotal (fraction in [0,1]).
	DupRate float64 `json:"dup_rate"`

	// Sliding windows (last 5 / 60 minutes).
	RxLast5Min   int     `json:"rx_last_5min"`
	DupLast5Min  int     `json:"dup_last_5min"`
	DupRate5Min  float64 `json:"dup_rate_5min"`
	RxLast60Min  int     `json:"rx_last_60min"`
	DupLast60Min int     `json:"dup_last_60min"`

	// Per-sender best-relay map (top 30 by count).
	Senders []RadioSender `json:"senders"`
	// Relay tallies (last byte) from raw RX — like RelayStats but from the
	// firmware's view, so counts include the duplicate "heard-via" receptions.
	RawRelays []RelayStat `json:"raw_relays"`
	// Hop-used histogram: key = hops (0..7 or "?"), value = count.
	HopUsed map[string]int `json:"hop_used"`
	// Max-hop histogram: distribution of HopStart (TTL configured at sender),
	// key = hop_start (0..7 or "?"), value = count. Reveals which senders set
	// aggressive TTLs that inflate airtime.
	MaxHop map[string]int `json:"max_hop"`
	// Channel-hash histogram: hash -> count (shows how many "other" channels we hear).
	ChannelHashes map[string]int `json:"channel_hashes"`
	// No-rebroadcast reason counters.
	NoRebcReasons map[string]int `json:"no_rebc_reasons,omitempty"`

	// Frequency offset stats from "Corrected frequency offset" lines.
	FreqOffset *FreqOffsetStats `json:"freq_offset,omitempty"`

	// RouterCandidates ranks relays by how many distinct senders rely on
	// them as their best (highest-SNR) entry point. Actionable diagnostic:
	// "this relay would be the best to promote to router".
	RouterCandidates []RouterCandidate `json:"router_candidates,omitempty"`
}

// FreqOffsetStats summarises the TX frequency-drift distribution measured by
// the local radio across all received packets.
type FreqOffsetStats struct {
	Count     int            `json:"count"`
	MeanHz    float64        `json:"mean_hz"`
	MinHz     float64        `json:"min_hz"`
	MaxHz     float64        `json:"max_hz"`
	StdDevHz  float64        `json:"stddev_hz"`
	Histogram map[string]int `json:"histogram"` // ordered bucket labels -> count
}

// RouterCandidate is a relay ranked by how many senders it is "best for".
type RouterCandidate struct {
	Relay       string  `json:"relay"`         // "!xxxxxxxx" if unique, "..xx" otherwise
	Name        string  `json:"name,omitempty"`
	BestForN    int     `json:"best_for_n"`    // distinct senders where this relay has top SNR
	TotalPkts   int     `json:"total_pkts"`    // total raw RX contributed
	AvgBestSNR  float32 `json:"avg_best_snr"`  // avg best-SNR across the senders it's best for
	SnrAdvantage float32 `json:"snr_advantage"` // mean dB advantage vs 2nd-best relay for those senders
}

// RadioSender summarises the radio-level view of one sender node: total
// receptions, which relays carried it, and which of those gave the best SNR.
type RadioSender struct {
	NodeNum  uint32  `json:"node_num"`
	NodeID   string  `json:"node_id"`
	Name     string  `json:"name,omitempty"`
	Count    int     `json:"count"`
	BestSNR  float32 `json:"best_snr"`
	BestRSSI int32   `json:"best_rssi"`
	// BestRelay is the relay that contributed the packet with the best SNR.
	BestRelay string `json:"best_relay"`
	// ViaRelays lists every distinct relay that carried packets for this node,
	// sorted by count descending.
	ViaRelays []SenderRelayLink `json:"via_relays"`
}

// SenderRelayLink is one (sender, relay) pairing with aggregate signal stats.
type SenderRelayLink struct {
	Relay    string  `json:"relay"` // "!xxxxxxxx" if resolved, "..xx" otherwise
	Count    int     `json:"count"`
	BestSNR  float32 `json:"best_snr"`
	BestRSSI int32   `json:"best_rssi"`
	AvgSNR   float32 `json:"avg_snr"`
	AvgRSSI  int32   `json:"avg_rssi"`
}

// senderRelayAcc is the mutable accumulator behind SenderRelayLink.
type senderRelayAcc struct {
	count    int
	bestSNR  float32
	bestRSSI int32
	sumSNR   float64
	sumRSSI  int64
	hasAny   bool
}

func (a *senderRelayAcc) addRx(snr float32, rssi int32) {
	if !a.hasAny || snr > a.bestSNR {
		a.bestSNR = snr
	}
	if !a.hasAny || rssi > a.bestRSSI {
		a.bestRSSI = rssi
	}
	a.count++
	a.sumSNR += float64(snr)
	a.sumRSSI += int64(rssi)
	a.hasAny = true
}

// radioMinute is one bucket of the per-minute sliding window.
type radioMinute struct {
	ts  int64 // unix minute (seconds / 60)
	rx  int
	dup int
}

// radioHealthData is the internal, mutable state behind RadioHealth().
// All fields are protected by Store.mu.
type radioHealthData struct {
	firstSeen int64

	rawRxTotal   int
	rawDupTotal  int
	rawMqttTotal int

	// senders[fromNum][relayLastByte] → acc
	senders map[uint32]map[uint32]*senderRelayAcc

	rawRelays     map[uint32]int
	hopUsed       map[int]int
	maxHop        map[int]int
	channelHashes map[uint32]int
	noRebcReasons map[string]int

	// Frequency offset accumulator.
	freqCount   int
	freqSum     float64
	freqSumSq   float64
	freqMin     float64
	freqMax     float64
	freqBuckets map[int]int // bucket index (100 Hz) -> count

	// Sliding window: last 60 minutes, newest at tail.
	history []radioMinute
}

func newRadioHealthData() *radioHealthData {
	return &radioHealthData{
		senders:       make(map[uint32]map[uint32]*senderRelayAcc),
		rawRelays:     make(map[uint32]int),
		hopUsed:       make(map[int]int),
		maxHop:        make(map[int]int),
		channelHashes: make(map[uint32]int),
		noRebcReasons: make(map[string]int),
		freqBuckets:   make(map[int]int),
	}
}

// bucketFor returns the minute bucket for t, creating or reusing the tail.
func (r *radioHealthData) bucketFor(t time.Time) *radioMinute {
	minute := t.Unix() / 60
	if n := len(r.history); n > 0 && r.history[n-1].ts == minute {
		return &r.history[n-1]
	}
	r.history = append(r.history, radioMinute{ts: minute})
	// Evict entries older than 60 minutes.
	cutoff := minute - 60
	i := 0
	for i < len(r.history) && r.history[i].ts < cutoff {
		i++
	}
	if i > 0 {
		r.history = r.history[i:]
	}
	return &r.history[len(r.history)-1]
}

// AddRawRx records a single firmware-parsed Lora RX line.
func (s *Store) AddRawRx(rx *fwparser.RawRx, t time.Time) {
	if rx == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.radio == nil {
		s.radio = newRadioHealthData()
	}
	r := s.radio
	if r.firstSeen == 0 {
		r.firstSeen = t.Unix()
	}
	r.rawRxTotal++
	if rx.ViaMqtt {
		r.rawMqttTotal++
	}
	if rx.Relay != 0 {
		r.rawRelays[rx.Relay]++
	}
	hops := -1
	if rx.HopStart >= rx.HopLimit {
		hops = int(rx.HopStart - rx.HopLimit)
	}
	r.hopUsed[hops]++
	// Max-hop (TTL set at sender): HopStart is a 3-bit field, always 0..7.
	// Bucket anything larger as "?" defensively.
	mh := -1
	if rx.HopStart <= 7 {
		mh = int(rx.HopStart)
	}
	r.maxHop[mh]++
	r.channelHashes[rx.ChHash]++

	// Per-sender, per-relay accumulator.
	if rx.From != 0 {
		relays := r.senders[rx.From]
		if relays == nil {
			relays = make(map[uint32]*senderRelayAcc)
			r.senders[rx.From] = relays
		}
		acc := relays[rx.Relay]
		if acc == nil {
			acc = &senderRelayAcc{}
			relays[rx.Relay] = acc
		}
		acc.addRx(rx.SNR, rx.RSSI)
	}

	r.bucketFor(t).rx++
}

// AddRawDupe records a firmware "Ignore dupe" line.
func (s *Store) AddRawDupe(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.radio == nil {
		s.radio = newRadioHealthData()
	}
	s.radio.rawDupTotal++
	s.radio.bucketFor(t).dup++
}

// AddRawNoRebroadcast records a "No rebroadcast" reason.
func (s *Store) AddRawNoRebroadcast(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.radio == nil {
		s.radio = newRadioHealthData()
	}
	s.radio.noRebcReasons[reason]++
}

// AddFreqOffset records one measured TX frequency offset sample (in Hz).
func (s *Store) AddFreqOffset(hz float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.radio == nil {
		s.radio = newRadioHealthData()
	}
	r := s.radio
	if r.freqCount == 0 {
		r.freqMin = hz
		r.freqMax = hz
	} else {
		if hz < r.freqMin {
			r.freqMin = hz
		}
		if hz > r.freqMax {
			r.freqMax = hz
		}
	}
	r.freqCount++
	r.freqSum += hz
	r.freqSumSq += hz * hz
	// 100 Hz buckets: bucket index = floor(hz/100).
	bucket := int(hz / 100)
	if hz < 0 && float64(bucket)*100 != hz {
		bucket--
	}
	r.freqBuckets[bucket]++
}

// RadioHealth returns a JSON-friendly snapshot.
func (s *Store) RadioHealth() RadioHealth {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out RadioHealth
	if s.radio == nil || s.radio.rawRxTotal == 0 {
		out.HopUsed = map[string]int{}
		out.MaxHop = map[string]int{}
		out.ChannelHashes = map[string]int{}
		out.NoRebcReasons = map[string]int{}
		return out
	}
	r := s.radio
	out.Enabled = true
	out.FirstSeen = r.firstSeen
	if r.firstSeen != 0 {
		out.WindowSecs = time.Now().Unix() - r.firstSeen
	}
	out.RawRxTotal = r.rawRxTotal
	out.RawDupTotal = r.rawDupTotal
	out.RawMqttTotal = r.rawMqttTotal
	if r.rawRxTotal > 0 {
		out.DupRate = float64(r.rawDupTotal) / float64(r.rawRxTotal)
	}

	// Sliding windows from minute buckets.
	nowMin := time.Now().Unix() / 60
	for _, b := range r.history {
		age := nowMin - b.ts
		if age < 5 {
			out.RxLast5Min += b.rx
			out.DupLast5Min += b.dup
		}
		if age < 60 {
			out.RxLast60Min += b.rx
			out.DupLast60Min += b.dup
		}
	}
	if out.RxLast5Min > 0 {
		out.DupRate5Min = float64(out.DupLast5Min) / float64(out.RxLast5Min)
	}

	// Per-sender best-relay map. While iterating, also accumulate stats for
	// the router-candidate ranking (which relay is "best" for most senders).
	type routerAcc struct {
		bestForN     int
		totalPkts    int
		bestSNRSum   float64
		snrAdvantage float64
		relayByte    uint32
	}
	routers := make(map[uint32]*routerAcc)

	senders := make([]RadioSender, 0, len(r.senders))
	for from, relays := range r.senders {
		rs := RadioSender{NodeNum: from, NodeID: fmt.Sprintf("!%08x", from)}
		if n, ok := s.nodes[from]; ok {
			if n.ShortName != "" {
				rs.Name = n.ShortName
			} else if n.LongName != "" {
				rs.Name = n.LongName
			}
		}
		var bestRelayByte uint32
		firstBest := true
		links := make([]SenderRelayLink, 0, len(relays))
		for relayByte, acc := range relays {
			rs.Count += acc.count
			if firstBest || acc.bestSNR > rs.BestSNR {
				rs.BestSNR = acc.bestSNR
				rs.BestRSSI = acc.bestRSSI
				bestRelayByte = relayByte
				firstBest = false
			}
			link := SenderRelayLink{
				Relay:    s.relayDisplay(relayByte),
				Count:    acc.count,
				BestSNR:  acc.bestSNR,
				BestRSSI: acc.bestRSSI,
			}
			if acc.count > 0 {
				link.AvgSNR = float32(acc.sumSNR / float64(acc.count))
				link.AvgRSSI = int32(acc.sumRSSI / int64(acc.count))
			}
			links = append(links, link)
		}
		sort.Slice(links, func(i, j int) bool { return links[i].Count > links[j].Count })
		rs.ViaRelays = links
		rs.BestRelay = s.relayDisplay(bestRelayByte)
		senders = append(senders, rs)

		// Router-candidate accumulation: find the 2nd-best SNR among this
		// sender's relays to measure the "advantage" of the best one.
		if bestRelayByte != 0 && len(relays) > 0 {
			secondBest := float32(-999)
			foundSecond := false
			for rb, acc := range relays {
				if rb == bestRelayByte {
					continue
				}
				if !foundSecond || acc.bestSNR > secondBest {
					secondBest = acc.bestSNR
					foundSecond = true
				}
			}
			ra := routers[bestRelayByte]
			if ra == nil {
				ra = &routerAcc{relayByte: bestRelayByte}
				routers[bestRelayByte] = ra
			}
			ra.bestForN++
			ra.bestSNRSum += float64(rs.BestSNR)
			if foundSecond {
				ra.snrAdvantage += float64(rs.BestSNR - secondBest)
			}
		}
	}
	sort.Slice(senders, func(i, j int) bool { return senders[i].Count > senders[j].Count })
	if len(senders) > 30 {
		senders = senders[:30]
	}
	out.Senders = senders

	// Raw relay ranking.
	rawRelays := make([]RelayStat, 0, len(r.rawRelays))
	for relayByte, cnt := range r.rawRelays {
		rs := RelayStat{
			NodeID: fmt.Sprintf("..%02x", relayByte&0xFF),
			Count:  cnt,
		}
		var matches []uint32
		for num := range s.nodes {
			if num&0xFF == relayByte&0xFF {
				matches = append(matches, num)
			}
		}
		if len(matches) == 1 {
			rs.NodeID = fmt.Sprintf("!%08x", matches[0])
			if n, ok := s.nodes[matches[0]]; ok {
				if n.ShortName != "" {
					rs.Name = n.ShortName
				} else if n.LongName != "" {
					rs.Name = n.LongName
				}
			}
		} else if len(matches) > 1 {
			cands := make([]string, len(matches))
			for i, m := range matches {
				cands[i] = fmt.Sprintf("!%08x", m)
			}
			rs.Candidates = cands
		}
		rawRelays = append(rawRelays, rs)
	}
	sort.Slice(rawRelays, func(i, j int) bool { return rawRelays[i].Count > rawRelays[j].Count })
	out.RawRelays = rawRelays

	// Hop histogram: convert to string keys for stable JSON.
	out.HopUsed = make(map[string]int, len(r.hopUsed))
	for h, n := range r.hopUsed {
		key := "?"
		if h >= 0 {
			key = fmt.Sprintf("%d", h)
		}
		out.HopUsed[key] = n
	}

	// Max-hop histogram (HopStart values observed).
	out.MaxHop = make(map[string]int, len(r.maxHop))
	for h, n := range r.maxHop {
		key := "?"
		if h >= 0 {
			key = fmt.Sprintf("%d", h)
		}
		out.MaxHop[key] = n
	}

	// Channel-hash histogram as hex strings.
	out.ChannelHashes = make(map[string]int, len(r.channelHashes))
	for ch, n := range r.channelHashes {
		out.ChannelHashes[fmt.Sprintf("0x%02x", ch)] = n
	}

	// No-rebroadcast reasons.
	out.NoRebcReasons = make(map[string]int, len(r.noRebcReasons))
	for k, v := range r.noRebcReasons {
		out.NoRebcReasons[k] = v
	}

	// Router candidates — finalize the ranking.
	if len(routers) > 0 {
		rcs := make([]RouterCandidate, 0, len(routers))
		for _, ra := range routers {
			avgSNR := float32(0)
			adv := float32(0)
			if ra.bestForN > 0 {
				avgSNR = float32(ra.bestSNRSum / float64(ra.bestForN))
				adv = float32(ra.snrAdvantage / float64(ra.bestForN))
			}
			rcs = append(rcs, RouterCandidate{
				Relay:        s.relayDisplay(ra.relayByte),
				Name:         s.relayName(ra.relayByte),
				BestForN:     ra.bestForN,
				TotalPkts:    r.rawRelays[ra.relayByte],
				AvgBestSNR:   avgSNR,
				SnrAdvantage: adv,
			})
		}
		sort.Slice(rcs, func(i, j int) bool {
			if rcs[i].BestForN != rcs[j].BestForN {
				return rcs[i].BestForN > rcs[j].BestForN
			}
			return rcs[i].AvgBestSNR > rcs[j].AvgBestSNR
		})
		out.RouterCandidates = rcs
	}

	// Frequency offset stats.
	if r.freqCount > 0 {
		mean := r.freqSum / float64(r.freqCount)
		variance := r.freqSumSq/float64(r.freqCount) - mean*mean
		if variance < 0 {
			variance = 0
		}
		stddev := 0.0
		if variance > 0 {
			// cheap sqrt via Newton
			x := variance
			for i := 0; i < 10; i++ {
				x = 0.5 * (x + variance/x)
			}
			stddev = x
		}
		// Build histogram with stable ordered keys.
		keys := make([]int, 0, len(r.freqBuckets))
		for k := range r.freqBuckets {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		hist := make(map[string]int, len(keys))
		for _, k := range keys {
			lo := k * 100
			hi := lo + 100
			hist[fmt.Sprintf("%+d..%+d", lo, hi)] = r.freqBuckets[k]
		}
		out.FreqOffset = &FreqOffsetStats{
			Count:     r.freqCount,
			MeanHz:    mean,
			MinHz:     r.freqMin,
			MaxHz:     r.freqMax,
			StdDevHz:  stddev,
			Histogram: hist,
		}
	}

	return out
}

// relayName resolves a relay's last byte to a friendly short/long name, or "".
// Must be called with s.mu held.
func (s *Store) relayName(relayByte uint32) string {
	if relayByte == 0 {
		return ""
	}
	var match uint32
	hits := 0
	for num := range s.nodes {
		if num&0xFF == relayByte&0xFF {
			match = num
			hits++
			if hits > 1 {
				return ""
			}
		}
	}
	if hits != 1 {
		return ""
	}
	if n, ok := s.nodes[match]; ok {
		if n.ShortName != "" {
			return n.ShortName
		}
		return n.LongName
	}
	return ""
}

// relayDisplay returns the best human-readable form for a relay last-byte,
// resolving to the full node ID when there is exactly one match.
// Must be called with s.mu held (read or write).
func (s *Store) relayDisplay(relayByte uint32) string {
	if relayByte == 0 {
		return ""
	}
	var match uint32
	hits := 0
	for num := range s.nodes {
		if num&0xFF == relayByte&0xFF {
			match = num
			hits++
			if hits > 1 {
				break
			}
		}
	}
	if hits == 1 {
		return fmt.Sprintf("!%08x", match)
	}
	return fmt.Sprintf("..%02x", relayByte&0xFF)
}
