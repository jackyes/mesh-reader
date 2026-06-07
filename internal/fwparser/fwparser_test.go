package fwparser

import (
	"math"
	"testing"
)

// ─────────────────────────────────────────────
// ParseRawRx
// ─────────────────────────────────────────────

const (
	// Documented example – plain (no MQTT).
	rxPlain = "Lora RX (id=0x4a74e63a fr=0xaedee7b0 to=0xffffffff, transport = 0, WantAck=0, HopLim=0 Ch=0x1f encrypted len=51 rxSNR=-5.5 rxRSSI=-103 hopStart=4 relay=0xc8)"

	// Documented example – via MQTT variant.
	// "via MQTT" is placed between rxRSSI and hopStart, as the regex requires.
	rxMqtt = "Lora RX (id=0x4a74e63a fr=0xaedee7b0 to=0xffffffff, transport = 0, WantAck=1, HopLim=3 Ch=0x1f encrypted len=51 rxSNR=2.5 rxRSSI=-80 via MQTT hopStart=7 relay=0xc8)"

	// Edge: zero hops, zero relay (direct), maxUint32 IDs, WantAck≠0.
	rxEdge = "Lora RX (id=0xffffffff fr=0x00000001 to=0x00000002, transport = 0, WantAck=1, HopLim=0 Ch=0x00 encrypted len=0 rxSNR=0 rxRSSI=0 hopStart=0 relay=0x0)"

	// Uppercase hex digits – the regex accepts [0-9a-fA-F].
	rxUpper = "Lora RX (id=0x4A74E63A fr=0xAEDEE7B0 to=0xFFFFFFFF, transport = 0, WantAck=0, HopLim=0 Ch=0x1F encrypted len=51 rxSNR=-5.5 rxRSSI=-103 hopStart=4 relay=0xC8)"
)

func TestParseRawRx_Plain(t *testing.T) {
	r := ParseRawRx(rxPlain)
	if r == nil {
		t.Fatal("expected non-nil result for plain RX line")
	}
	if r.ID != 0x4a74e63a {
		t.Errorf("ID: got 0x%x, want 0x4a74e63a", r.ID)
	}
	if r.From != 0xaedee7b0 {
		t.Errorf("From: got 0x%x, want 0xaedee7b0", r.From)
	}
	if r.To != 0xffffffff {
		t.Errorf("To: got 0x%x, want 0xffffffff", r.To)
	}
	if r.HopLimit != 0 {
		t.Errorf("HopLimit: got %d, want 0", r.HopLimit)
	}
	if r.HopStart != 4 {
		t.Errorf("HopStart: got %d, want 4", r.HopStart)
	}
	if r.Len != 51 {
		t.Errorf("Len: got %d, want 51", r.Len)
	}
	if r.SNR != float32(-5.5) {
		t.Errorf("SNR: got %v, want -5.5", r.SNR)
	}
	if r.RSSI != -103 {
		t.Errorf("RSSI: got %d, want -103", r.RSSI)
	}
	if r.Relay != 0xc8 {
		t.Errorf("Relay: got 0x%x, want 0xc8", r.Relay)
	}
	if r.ChHash != 0x1f {
		t.Errorf("ChHash: got 0x%x, want 0x1f", r.ChHash)
	}
	if r.ViaMqtt {
		t.Error("ViaMqtt: got true, want false for plain line")
	}
	if r.WantAck {
		t.Error("WantAck: got true, want false (WantAck=0)")
	}
}

func TestParseRawRx_ViaMqtt(t *testing.T) {
	r := ParseRawRx(rxMqtt)
	if r == nil {
		t.Fatal("expected non-nil result for MQTT RX line")
	}
	if !r.ViaMqtt {
		t.Error("ViaMqtt: got false, want true")
	}
	if !r.WantAck {
		t.Error("WantAck: got false, want true (WantAck=1)")
	}
	if r.HopLimit != 3 {
		t.Errorf("HopLimit: got %d, want 3", r.HopLimit)
	}
	if r.HopStart != 7 {
		t.Errorf("HopStart: got %d, want 7", r.HopStart)
	}
	if r.SNR != float32(2.5) {
		t.Errorf("SNR: got %v, want 2.5", r.SNR)
	}
	if r.RSSI != -80 {
		t.Errorf("RSSI: got %d, want -80", r.RSSI)
	}
	// All other fields shared with plain line
	if r.ID != 0x4a74e63a {
		t.Errorf("ID: got 0x%x, want 0x4a74e63a", r.ID)
	}
	if r.From != 0xaedee7b0 {
		t.Errorf("From: got 0x%x, want 0xaedee7b0", r.From)
	}
	if r.To != 0xffffffff {
		t.Errorf("To: got 0x%x, want 0xffffffff", r.To)
	}
}

func TestParseRawRx_EdgeZeroRelayZeroHops(t *testing.T) {
	r := ParseRawRx(rxEdge)
	if r == nil {
		t.Fatal("expected non-nil for edge line")
	}
	if r.ID != 0xffffffff {
		t.Errorf("ID: got 0x%x, want 0xffffffff", r.ID)
	}
	if r.From != 0x00000001 {
		t.Errorf("From: got 0x%x, want 1", r.From)
	}
	if r.To != 0x00000002 {
		t.Errorf("To: got 0x%x, want 2", r.To)
	}
	if r.HopLimit != 0 {
		t.Errorf("HopLimit: got %d, want 0", r.HopLimit)
	}
	if r.HopStart != 0 {
		t.Errorf("HopStart: got %d, want 0", r.HopStart)
	}
	if r.Relay != 0 {
		t.Errorf("Relay: got %d, want 0 (direct)", r.Relay)
	}
	if r.Len != 0 {
		t.Errorf("Len: got %d, want 0", r.Len)
	}
	if r.SNR != float32(0) {
		t.Errorf("SNR: got %v, want 0", r.SNR)
	}
	if r.RSSI != 0 {
		t.Errorf("RSSI: got %d, want 0", r.RSSI)
	}
	if !r.WantAck {
		t.Error("WantAck: got false, want true (WantAck=1)")
	}
	if r.ViaMqtt {
		t.Error("ViaMqtt: got true, want false")
	}
}

func TestParseRawRx_UppercaseHex(t *testing.T) {
	r := ParseRawRx(rxUpper)
	if r == nil {
		t.Fatal("expected non-nil for uppercase-hex RX line")
	}
	if r.ID != 0x4a74e63a {
		t.Errorf("ID: got 0x%x, want 0x4a74e63a", r.ID)
	}
	if r.From != 0xaedee7b0 {
		t.Errorf("From: got 0x%x, want 0xaedee7b0", r.From)
	}
	if r.To != 0xffffffff {
		t.Errorf("To: got 0x%x, want 0xffffffff", r.To)
	}
	if r.Relay != 0xc8 {
		t.Errorf("Relay: got 0x%x, want 0xc8", r.Relay)
	}
}

func TestParseRawRx_NonMatch(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"empty string", ""},
		{"unrelated line", "Some other log line"},
		{"missing relay field", "Lora RX (id=0x4a74e63a fr=0xaedee7b0 to=0xffffffff, transport = 0, WantAck=0, HopLim=0 Ch=0x1f encrypted len=51 rxSNR=-5.5 rxRSSI=-103 hopStart=4)"},
		{"missing hopStart", "Lora RX (id=0x4a74e63a fr=0xaedee7b0 to=0xffffffff, transport = 0, WantAck=0, HopLim=0 Ch=0x1f encrypted len=51 rxSNR=-5.5 rxRSSI=-103 relay=0xc8)"},
		{"missing rxSNR", "Lora RX (id=0x4a74e63a fr=0xaedee7b0 to=0xffffffff, transport = 0, WantAck=0, HopLim=0 Ch=0x1f encrypted len=51 rxRSSI=-103 hopStart=4 relay=0xc8)"},
		{"wrong prefix", "LoRa RX (id=0x4a74e63a fr=0xaedee7b0 to=0xffffffff, transport = 0, WantAck=0, HopLim=0 Ch=0x1f encrypted len=51 rxSNR=-5.5 rxRSSI=-103 hopStart=4 relay=0xc8)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ParseRawRx(tc.msg) != nil {
				t.Errorf("expected nil for %q", tc.msg)
			}
		})
	}
}

// ─────────────────────────────────────────────
// ParseDupe
// ─────────────────────────────────────────────

func TestParseDupe_Match(t *testing.T) {
	// Documented example line
	msg := "Ignore dupe incoming msg (id=0xb3178150 fr=0x433ea02c ..."
	d := ParseDupe(msg)
	if d == nil {
		t.Fatal("expected non-nil Dupe")
	}
	if d.ID != 0xb3178150 {
		t.Errorf("ID: got 0x%x, want 0xb3178150", d.ID)
	}
	if d.From != 0x433ea02c {
		t.Errorf("From: got 0x%x, want 0x433ea02c", d.From)
	}
}

func TestParseDupe_NonMatch(t *testing.T) {
	cases := []string{
		"",
		"Some other log line",
		// prefix mismatch (lowercase 'i')
		"ignore dupe incoming msg (id=0xb3178150 fr=0x433ea02c",
		// missing 'fr=' field
		"Ignore dupe incoming msg (id=0xb3178150)",
	}
	for _, msg := range cases {
		if ParseDupe(msg) != nil {
			t.Errorf("expected nil for %q", msg)
		}
	}
}

// ─────────────────────────────────────────────
// ParseNoRebroadcast
// ─────────────────────────────────────────────

func TestParseNoRebroadcast_Match(t *testing.T) {
	// Documented example
	msg := "No rebroadcast: Role = CLIENT_MUTE or Rebroadcast Mode = NONE"
	got := ParseNoRebroadcast(msg)
	want := "Role = CLIENT_MUTE or Rebroadcast Mode = NONE"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseNoRebroadcast_TrailingWhitespace(t *testing.T) {
	msg := "No rebroadcast: some reason   "
	got := ParseNoRebroadcast(msg)
	want := "some reason"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseNoRebroadcast_LeadingAndTrailingWhitespace(t *testing.T) {
	// The regex captures everything after ": "; leading space in reason is preserved
	// (TrimSpace trims both sides).
	msg := "No rebroadcast:   leading and trailing   "
	got := ParseNoRebroadcast(msg)
	want := "leading and trailing"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseNoRebroadcast_NonMatch(t *testing.T) {
	cases := []string{
		"",
		"Some other line",
		"no rebroadcast: lowercase prefix",
		"Rebroadcast: wrong keyword",
	}
	for _, msg := range cases {
		if got := ParseNoRebroadcast(msg); got != "" {
			t.Errorf("expected empty string for %q, got %q", msg, got)
		}
	}
}

// ─────────────────────────────────────────────
// ParseFreqOffset
// ─────────────────────────────────────────────

func TestParseFreqOffset(t *testing.T) {
	tests := []struct {
		name    string
		msg     string
		wantVal float64
		wantOk  bool
	}{
		{
			name:    "documented negative fractional",
			msg:     "Corrected frequency offset: -120.125000",
			wantVal: -120.125,
			wantOk:  true,
		},
		{
			name:    "positive fractional",
			msg:     "Corrected frequency offset: 3.14",
			wantVal: 3.14,
			wantOk:  true,
		},
		{
			name:    "integer only",
			msg:     "Corrected frequency offset: 42",
			wantVal: 42,
			wantOk:  true,
		},
		{
			name:    "zero",
			msg:     "Corrected frequency offset: 0",
			wantVal: 0,
			wantOk:  true,
		},
		{
			name:   "non-match – different prefix",
			msg:    "frequency offset: -120.125000",
			wantOk: false,
		},
		{
			name:   "empty string",
			msg:    "",
			wantOk: false,
		},
		{
			name:   "unrelated line",
			msg:    "Some totally unrelated log line",
			wantOk: false,
		},
		{
			// The regex requires at least one digit before optional fraction,
			// so "abc" won't match \d+ and the whole line won't match.
			name:   "malformed numeric – letters instead of digits",
			msg:    "Corrected frequency offset: abc",
			wantOk: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseFreqOffset(tc.msg)
			if ok != tc.wantOk {
				t.Fatalf("ok: got %v, want %v", ok, tc.wantOk)
			}
			if tc.wantOk && math.Abs(got-tc.wantVal) > 1e-9 {
				t.Errorf("value: got %v, want %v", got, tc.wantVal)
			}
			if !tc.wantOk && got != 0 {
				t.Errorf("on non-match, value should be 0, got %v", got)
			}
		})
	}
}

// ─────────────────────────────────────────────
// ParseHopScale / ParseHopScalePerHop / ParseTMCache
// ─────────────────────────────────────────────

func TestParseHopScale(t *testing.T) {
	// Real firmware line (the "[HOPSCALE]" tag is tolerated, not required).
	msg := "[HOPSCALE] hop=7 histActive=0 fill=70% samp=1/8 filt=1/8 entries=90 lastCounted=0 polite=2/4 nextRoll=60min"
	h := ParseHopScale(msg)
	if h == nil {
		t.Fatal("expected non-nil HopScale")
	}
	if h.Hop != 7 {
		t.Errorf("Hop = %d, want 7", h.Hop)
	}
	if h.FillPct != 70 {
		t.Errorf("FillPct = %d, want 70", h.FillPct)
	}
	if h.SampNum != 1 || h.SampDen != 8 {
		t.Errorf("Samp = %d/%d, want 1/8", h.SampNum, h.SampDen)
	}
	if h.FiltNum != 1 || h.FiltDen != 8 {
		t.Errorf("Filt = %d/%d, want 1/8", h.FiltNum, h.FiltDen)
	}
	if h.Entries != 90 {
		t.Errorf("Entries = %d, want 90", h.Entries)
	}
	if h.PoliteNum != 2 || h.PoliteDen != 4 {
		t.Errorf("Polite = %d/%d, want 2/4", h.PoliteNum, h.PoliteDen)
	}
	if h.NextRollMin != 60 {
		t.Errorf("NextRollMin = %d, want 60", h.NextRollMin)
	}
}

func TestParseHopScale_NonMatch(t *testing.T) {
	for _, msg := range []string{
		"",
		"Some other log line",
		// TM line must not be mistaken for a HOPSCALE line.
		"[TM] NodeInfo PSRAM cache entries: 1/1945 target (2048 packed slots)",
		// perHop line lacks fill=/samp=.
		"[HOPSCALE] nodes perHop: [0 0 0 0 0 0 0 0]",
	} {
		if ParseHopScale(msg) != nil {
			t.Errorf("expected nil for %q", msg)
		}
	}
}

func TestParseHopScalePerHop(t *testing.T) {
	kind, vals, ok := ParseHopScalePerHop("[HOPSCALE] nodes perHop: [1 2 0 0 3 0 0 0]")
	if !ok {
		t.Fatal("expected ok for nodes perHop line")
	}
	if kind != "nodes" {
		t.Errorf("kind = %q, want nodes", kind)
	}
	want := []int{1, 2, 0, 0, 3, 0, 0, 0}
	if len(vals) != len(want) {
		t.Fatalf("len(vals) = %d, want %d", len(vals), len(want))
	}
	for i := range want {
		if vals[i] != want[i] {
			t.Errorf("vals[%d] = %d, want %d", i, vals[i], want[i])
		}
	}
	k2, _, ok2 := ParseHopScalePerHop("[HOPSCALE] last scaled perHop: [0 0 0 0 0 0 0 0]")
	if !ok2 || k2 != "last scaled" {
		t.Errorf("last scaled: ok=%v kind=%q", ok2, k2)
	}
	if _, _, ok3 := ParseHopScalePerHop("unrelated"); ok3 {
		t.Error("expected ok=false for unrelated line")
	}
}

func TestParseTMCache(t *testing.T) {
	c := ParseTMCache("[TM] NodeInfo PSRAM cache entries: 1/1945 target (2048 packed slots, 12-bit tags)")
	if c == nil {
		t.Fatal("expected non-nil TMCache")
	}
	if c.Used != 1 || c.Target != 1945 {
		t.Errorf("got %d/%d, want 1/1945", c.Used, c.Target)
	}
	if ParseTMCache("no cache here") != nil {
		t.Error("expected nil for unrelated line")
	}
}

// ─────────────────────────────────────────────
// parseHex32 (white-box)
// ─────────────────────────────────────────────

func TestParseHex32(t *testing.T) {
	tests := []struct {
		input string
		want  uint32
	}{
		{"0", 0},
		{"1", 1},
		{"ff", 255},
		{"FF", 255},    // uppercase
		{"1f", 31},
		{"c8", 200},
		{"4a74e63a", 0x4a74e63a},
		{"ffffffff", 0xffffffff}, // max uint32
		{"FFFFFFFF", 0xffffffff}, // uppercase max
		{"aedee7b0", 0xaedee7b0},
		// Empty string → ParseUint error → returns 0
		{"", 0},
		// Invalid hex → ParseUint error → returns 0
		// (not strictly reachable from the regex, but tests parseHex32 directly)
	}
	for _, tc := range tests {
		got := parseHex32(tc.input)
		if got != tc.want {
			t.Errorf("parseHex32(%q): got 0x%x, want 0x%x", tc.input, got, tc.want)
		}
	}
}

func TestParseHex32_Overflow(t *testing.T) {
	// Value > MaxUint32: ParseUint returns max value on overflow (not 0).
	// This documents the actual behavior.
	got := parseHex32("1ffffffff") // 2^33-1, exceeds uint32
	// strconv.ParseUint with bitSize=32 returns MaxUint64 on overflow but
	// uint32(MaxUint64) == MaxUint32. We just verify the call doesn't panic.
	_ = got
}

func TestParseHex32_InvalidString(t *testing.T) {
	// Non-hex string → error → returns 0.
	got := parseHex32("xyz")
	if got != 0 {
		t.Errorf("parseHex32(\"xyz\"): got %d, want 0", got)
	}
}

// ─────────────────────────────────────────────
// parseUint (white-box)
// ─────────────────────────────────────────────

func TestParseUint(t *testing.T) {
	tests := []struct {
		input string
		base  int
		want  int
	}{
		{"0", 10, 0},
		{"51", 10, 51},
		{"103", 10, 103},
		{"4294967295", 10, 4294967295}, // MaxUint32 in base-10
		// Base 16
		{"ff", 16, 255},
		{"1f", 16, 31},
		// Invalid → 0
		{"xyz", 10, 0},
		{"", 10, 0},
		// Base is honored: "ff" in base 10 is invalid → 0
		{"ff", 10, 0},
	}
	for _, tc := range tests {
		got := parseUint(tc.input, tc.base)
		if got != tc.want {
			t.Errorf("parseUint(%q, %d): got %d, want %d", tc.input, tc.base, got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────
// Table-driven comprehensive ParseRawRx
// ─────────────────────────────────────────────

func TestParseRawRx_Table(t *testing.T) {
	tests := []struct {
		name  string
		msg   string
		isNil bool
		check func(t *testing.T, r *RawRx)
	}{
		{
			name: "plain documented example – all fields",
			msg:  rxPlain,
			check: func(t *testing.T, r *RawRx) {
				if r.ID != 0x4a74e63a {
					t.Errorf("ID 0x%x", r.ID)
				}
				if r.From != 0xaedee7b0 {
					t.Errorf("From 0x%x", r.From)
				}
				if r.To != 0xffffffff {
					t.Errorf("To 0x%x", r.To)
				}
				if r.HopLimit != 0 {
					t.Errorf("HopLimit %d", r.HopLimit)
				}
				if r.HopStart != 4 {
					t.Errorf("HopStart %d", r.HopStart)
				}
				if r.Len != 51 {
					t.Errorf("Len %d", r.Len)
				}
				if r.SNR != float32(-5.5) {
					t.Errorf("SNR %v", r.SNR)
				}
				if r.RSSI != -103 {
					t.Errorf("RSSI %d", r.RSSI)
				}
				if r.Relay != 0xc8 {
					t.Errorf("Relay 0x%x", r.Relay)
				}
				if r.ChHash != 0x1f {
					t.Errorf("ChHash 0x%x", r.ChHash)
				}
				if r.ViaMqtt {
					t.Error("ViaMqtt should be false")
				}
				if r.WantAck {
					t.Error("WantAck should be false (=0)")
				}
			},
		},
		{
			name: "via MQTT – ViaMqtt and WantAck true",
			msg:  rxMqtt,
			check: func(t *testing.T, r *RawRx) {
				if !r.ViaMqtt {
					t.Error("ViaMqtt should be true")
				}
				if !r.WantAck {
					t.Error("WantAck should be true (=1)")
				}
				if r.HopStart != 7 {
					t.Errorf("HopStart %d, want 7", r.HopStart)
				}
				if r.HopLimit != 3 {
					t.Errorf("HopLimit %d, want 3", r.HopLimit)
				}
			},
		},
		{
			name: "direct relay (relay=0x0)",
			msg:  rxEdge,
			check: func(t *testing.T, r *RawRx) {
				if r.Relay != 0 {
					t.Errorf("Relay %d, want 0", r.Relay)
				}
				if r.HopStart != 0 {
					t.Errorf("HopStart %d, want 0", r.HopStart)
				}
				if r.HopLimit != 0 {
					t.Errorf("HopLimit %d, want 0", r.HopLimit)
				}
			},
		},
		{name: "empty → nil", msg: "", isNil: true},
		{name: "unrelated → nil", msg: "Hello world", isNil: true},
		{
			name:  "close but missing relay → nil",
			msg:   "Lora RX (id=0x4a74e63a fr=0xaedee7b0 to=0xffffffff, transport = 0, WantAck=0, HopLim=0 Ch=0x1f encrypted len=51 rxSNR=-5.5 rxRSSI=-103 hopStart=4)",
			isNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseRawRx(tc.msg)
			if tc.isNil {
				if r != nil {
					t.Errorf("expected nil, got %+v", r)
				}
				return
			}
			if r == nil {
				t.Fatal("expected non-nil RawRx")
			}
			if tc.check != nil {
				tc.check(t, r)
			}
		})
	}
}
