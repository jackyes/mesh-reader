// Package fwparser extracts structured information from Meshtastic firmware
// debug-log lines (LogRecord messages).
//
// The firmware emits free-form text, but a handful of router/radio lines have
// a very regular shape and carry valuable telemetry that is NOT exposed
// anywhere else in the API (e.g. raw pre-dedup radio reception, duplicate
// packet detection, decision to rebroadcast or not). This package parses the
// ones we care about.
package fwparser

import (
	"regexp"
	"strconv"
	"strings"
)

// RawRx is a single radio reception as seen by the firmware BEFORE
// deduplication / decryption. One underlying mesh packet may produce several
// RawRx entries when heard via multiple relays.
type RawRx struct {
	ID       uint32
	From     uint32
	To       uint32
	HopLimit uint32
	HopStart uint32
	Len      int
	SNR      float32
	RSSI     int32
	// Relay is the last byte of the relaying node number (0 = direct).
	Relay   uint32
	ChHash  uint32
	ViaMqtt bool
	WantAck bool
}

// Dupe is a firmware-detected duplicate packet rejection.
type Dupe struct {
	ID   uint32
	From uint32
}

var (
	// Example:
	// Lora RX (id=0x4a74e63a fr=0xaedee7b0 to=0xffffffff, transport = 0, WantAck=0, HopLim=0 Ch=0x1f encrypted len=51 rxSNR=-5.5 rxRSSI=-103 hopStart=4 relay=0xc8)
	// Lora RX (... via MQTT hopStart=7 relay=0xc8)
	rxRe = regexp.MustCompile(
		`Lora RX \(id=0x([0-9a-fA-F]+) fr=0x([0-9a-fA-F]+) to=0x([0-9a-fA-F]+), transport = \d+, WantAck=(\d+), HopLim=(\d+) Ch=0x([0-9a-fA-F]+) encrypted len=(\d+) rxSNR=(-?[0-9.]+) rxRSSI=(-?\d+)(?: via MQTT)? hopStart=(\d+) relay=0x([0-9a-fA-F]+)\)`,
	)

	// Example:
	// Ignore dupe incoming msg (id=0xb3178150 fr=0x433ea02c ...
	dupeRe = regexp.MustCompile(
		`Ignore dupe incoming msg \(id=0x([0-9a-fA-F]+) fr=0x([0-9a-fA-F]+)`,
	)

	// Example:
	// No rebroadcast: Role = CLIENT_MUTE or Rebroadcast Mode = NONE
	noRebcRe = regexp.MustCompile(`No rebroadcast: (.+)$`)

	// Example:
	// Corrected frequency offset: -120.125000
	freqOffsetRe = regexp.MustCompile(`Corrected frequency offset: (-?\d+(?:\.\d+)?)`)

	// HopScaling module main stats line. Example:
	// [HOPSCALE] hop=7 histActive=0 fill=70% samp=1/8 filt=1/8 entries=90 lastCounted=0 polite=2/4 nextRoll=60min
	// Gate on the distinctive "fill=N% samp=N/N" combination, then extract each
	// field independently so a firmware change to one field doesn't lose the rest.
	hopScaleGateRe = regexp.MustCompile(`fill=\d+% +samp=\d+/\d+`)
	hsHopRe        = regexp.MustCompile(`\bhop=(\d+)`)
	hsHistRe       = regexp.MustCompile(`histActive=(\d+)`)
	hsFillRe       = regexp.MustCompile(`fill=(\d+)%`)
	hsSampRe       = regexp.MustCompile(`samp=(\d+)/(\d+)`)
	hsFiltRe       = regexp.MustCompile(`filt=(\d+)/(\d+)`)
	hsEntriesRe    = regexp.MustCompile(`entries=(\d+)`)
	hsLastRe       = regexp.MustCompile(`lastCounted=(\d+)`)
	hsPoliteRe     = regexp.MustCompile(`polite=(\d+)/(\d+)`)
	hsNextRollRe   = regexp.MustCompile(`nextRoll=(\d+)`)

	// HopScaling per-hop arrays. Examples:
	// [HOPSCALE] nodes perHop: [0 0 0 0 0 0 0 0]
	// [HOPSCALE] last scaled perHop: [0 0 0 0 0 0 0 0]
	perHopRe = regexp.MustCompile(`(nodes|last scaled) perHop: \[([\d ]+)\]`)

	// TrafficManagement NodeInfo cache line. Example:
	// [TM] NodeInfo PSRAM cache entries: 1/1945 target (2048 packed slots, ...)
	tmCacheRe = regexp.MustCompile(`cache entries: (\d+)/(\d+) target`)
)

// HopScale is a snapshot of the firmware HopScaling module, parsed from its
// "[HOPSCALE] hop=... fill=...%" debug-log line. The module is not exposed as
// telemetry, so the log is the only source.
type HopScale struct {
	Hop         int
	HistActive  int
	FillPct     int
	SampNum     int
	SampDen     int
	FiltNum     int
	FiltDen     int
	Entries     int
	LastCounted int
	PoliteNum   int
	PoliteDen   int
	NextRollMin int
}

// TMCache is the NodeInfo cache state from a TrafficManagement "[TM] ... cache
// entries: N/M target" line.
type TMCache struct {
	Used   int
	Target int
}

// ParseHopScale returns a HopScale if msg is the HopScaling main stats line,
// otherwise nil. Fields absent from the line are left at zero.
func ParseHopScale(msg string) *HopScale {
	if !hopScaleGateRe.MatchString(msg) {
		return nil
	}
	h := &HopScale{
		Hop:         reInt(hsHopRe, msg),
		HistActive:  reInt(hsHistRe, msg),
		FillPct:     reInt(hsFillRe, msg),
		Entries:     reInt(hsEntriesRe, msg),
		LastCounted: reInt(hsLastRe, msg),
		NextRollMin: reInt(hsNextRollRe, msg),
	}
	h.SampNum, h.SampDen = reFrac(hsSampRe, msg)
	h.FiltNum, h.FiltDen = reFrac(hsFiltRe, msg)
	h.PoliteNum, h.PoliteDen = reFrac(hsPoliteRe, msg)
	return h
}

// ParseHopScalePerHop parses a "nodes perHop: [..]" / "last scaled perHop: [..]"
// line. kind is "nodes" or "last scaled"; ok is false when the line doesn't match.
func ParseHopScalePerHop(msg string) (kind string, vals []int, ok bool) {
	m := perHopRe.FindStringSubmatch(msg)
	if m == nil {
		return "", nil, false
	}
	for _, f := range strings.Fields(m[2]) {
		vals = append(vals, parseUint(f, 10))
	}
	return m[1], vals, true
}

// ParseTMCache returns the NodeInfo cache state if msg is a TrafficManagement
// "cache entries: N/M target" line, otherwise nil.
func ParseTMCache(msg string) *TMCache {
	m := tmCacheRe.FindStringSubmatch(msg)
	if m == nil {
		return nil
	}
	return &TMCache{Used: parseUint(m[1], 10), Target: parseUint(m[2], 10)}
}

// reInt returns the first capture group of re in s as an int, or 0.
func reInt(re *regexp.Regexp, s string) int {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	return parseUint(m[1], 10)
}

// reFrac returns the first two capture groups of re in s as ints, or 0, 0.
func reFrac(re *regexp.Regexp, s string) (int, int) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, 0
	}
	return parseUint(m[1], 10), parseUint(m[2], 10)
}

// ParseRawRx returns a RawRx if msg is a "Lora RX" firmware line, otherwise nil.
func ParseRawRx(msg string) *RawRx {
	m := rxRe.FindStringSubmatch(msg)
	if m == nil {
		return nil
	}
	snr, _ := strconv.ParseFloat(m[8], 32)
	rssi, _ := strconv.Atoi(m[9])
	return &RawRx{
		ID:       parseHex32(m[1]),
		From:     parseHex32(m[2]),
		To:       parseHex32(m[3]),
		WantAck:  m[4] != "0",
		HopLimit: uint32(parseUint(m[5], 10)),
		ChHash:   parseHex32(m[6]),
		Len:      parseUint(m[7], 10),
		SNR:      float32(snr),
		RSSI:     int32(rssi),
		HopStart: uint32(parseUint(m[10], 10)),
		Relay:    parseHex32(m[11]),
		ViaMqtt:  strings.Contains(msg, "via MQTT"),
	}
}

// ParseDupe returns a Dupe if msg is an "Ignore dupe incoming msg" line.
func ParseDupe(msg string) *Dupe {
	m := dupeRe.FindStringSubmatch(msg)
	if m == nil {
		return nil
	}
	return &Dupe{
		ID:   parseHex32(m[1]),
		From: parseHex32(m[2]),
	}
}

// ParseNoRebroadcast returns the reason text if msg is a "No rebroadcast" line,
// otherwise "".
func ParseNoRebroadcast(msg string) string {
	m := noRebcRe.FindStringSubmatch(msg)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// ParseFreqOffset returns the frequency offset in Hz if msg is a
// "Corrected frequency offset" line. The second return value is false
// when the line does not match.
func ParseFreqOffset(msg string) (float64, bool) {
	m := freqOffsetRe.FindStringSubmatch(msg)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseHex32(s string) uint32 {
	n, _ := strconv.ParseUint(s, 16, 32)
	return uint32(n)
}

func parseUint(s string, base int) int {
	n, _ := strconv.ParseUint(s, base, 32)
	return int(n)
}
