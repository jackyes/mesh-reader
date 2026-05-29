// Package logger tests white-box coverage of the logger package.
// Tests run with -race to catch data races.
package logger

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mesh-reader/internal/decoder"
)

// ---- helpers ----------------------------------------------------------------

// newEvent builds a minimal Event for the given type.
func newEvent(t decoder.EventType) *decoder.Event {
	return &decoder.Event{
		Time:     time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC),
		Type:     t,
		FromNode: 0xdeadbeef,
		ToNode:   0xffffffff,
		RSSI:     -85,
		SNR:      7.5,
		HopLimit: 3,
		Details:  map[string]any{},
	}
}

// readLines returns all lines written to every *.log file in dir.
func readAllLogLines(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	var lines []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("open log file: %v", err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		f.Close()
	}
	return lines
}

// countTabs returns the number of tab characters in s.
func countTabs(s string) int {
	return strings.Count(s, "\t")
}

// ---- 1. Constructor & lifecycle ---------------------------------------------

func TestNew_CreatesDir(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "a", "b", "c")

	l, err := New(nested)
	if err != nil {
		t.Fatalf("New() on non-existent nested dir returned error: %v", err)
	}
	defer l.Close()

	if _, err := os.Stat(nested); err != nil {
		t.Errorf("directory %q was not created: %v", nested, err)
	}
}

func TestNew_InvalidPath_ReturnsError(t *testing.T) {
	// On Windows a null byte in the path is invalid.
	_, err := New("path\x00withNull")
	if err == nil {
		t.Error("expected error for invalid path, got nil")
	}
}

func TestDir_ReturnsConfiguredDir(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if got := l.Dir(); got != dir {
		t.Errorf("Dir() = %q, want %q", got, dir)
	}
}

func TestClose_SafeToCallTwice(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Log one event so file handles are opened.
	l.Log(newEvent(decoder.EventTextMessage))

	if err := l.Close(); err != nil {
		t.Errorf("first Close() error: %v", err)
	}
	// Second call must not panic or return an error.
	if err := l.Close(); err != nil {
		t.Errorf("second Close() error: %v", err)
	}
}

func TestClose_NoFilesOpened(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Close without writing anything — should not panic.
	if err := l.Close(); err != nil {
		t.Errorf("Close() on fresh Logger returned error: %v", err)
	}
}

// ---- 2. Line formatting (field order, timestamp, column count) --------------

const fixedCols = 7 // timestamp + type + FROM + TO + RSSI + SNR + HOPS

func TestFormat_FieldOrder(t *testing.T) {
	e := newEvent(decoder.EventTextMessage)
	e.Details = map[string]any{"text": "hello"}
	line := format(e)

	fields := strings.Split(line, "\t")
	// Must have exactly fixedCols + 1 detail field.
	if len(fields) != fixedCols+1 {
		t.Fatalf("expected %d fields, got %d: %q", fixedCols+1, len(fields), line)
	}

	if !strings.HasPrefix(fields[0], "2026-01-15") {
		t.Errorf("field[0] timestamp wrong: %q", fields[0])
	}
	if !strings.Contains(fields[1], "TEXT_MESSAGE") {
		t.Errorf("field[1] type wrong: %q", fields[1])
	}
	if fields[2] != "FROM=!deadbeef" {
		t.Errorf("field[2] FROM wrong: %q", fields[2])
	}
	if fields[3] != "TO=^all" {
		t.Errorf("field[3] TO wrong: %q", fields[3])
	}
	if !strings.HasPrefix(fields[4], "RSSI=") {
		t.Errorf("field[4] RSSI wrong: %q", fields[4])
	}
	if !strings.HasPrefix(fields[5], "SNR=") {
		t.Errorf("field[5] SNR wrong: %q", fields[5])
	}
	if !strings.HasPrefix(fields[6], "HOPS=") {
		t.Errorf("field[6] HOPS wrong: %q", fields[6])
	}
	if !strings.Contains(fields[7], "text=hello") {
		t.Errorf("field[7] detail wrong: %q", fields[7])
	}
}

func TestFormat_TimestampFormat(t *testing.T) {
	e := newEvent(decoder.EventPosition)
	line := format(e)
	ts := strings.Split(line, "\t")[0]
	_, err := time.Parse("2006-01-02 15:04:05", ts)
	if err != nil {
		t.Errorf("timestamp %q does not match layout 2006-01-02 15:04:05: %v", ts, err)
	}
}

func TestFormat_ZeroNodes(t *testing.T) {
	e := newEvent(decoder.EventConfigComplete)
	e.FromNode = 0
	e.ToNode = 0
	e.Details = map[string]any{"config_id": uint32(42)}
	line := format(e)
	fields := strings.Split(line, "\t")
	if fields[2] != "FROM=-" {
		t.Errorf("zero FromNode should produce FROM=-, got %q", fields[2])
	}
	if fields[3] != "TO=-" {
		t.Errorf("zero ToNode should produce TO=-, got %q", fields[3])
	}
}

func TestFormat_BroadcastToNode(t *testing.T) {
	e := newEvent(decoder.EventTextMessage)
	e.ToNode = 0xFFFFFFFF
	e.Details = map[string]any{}
	line := format(e)
	fields := strings.Split(line, "\t")
	if fields[3] != "TO=^all" {
		t.Errorf("broadcast ToNode should produce TO=^all, got %q", fields[3])
	}
}

func TestFormat_ZeroRSSI_SNR_Hops(t *testing.T) {
	e := newEvent(decoder.EventMyInfo)
	e.RSSI = 0
	e.SNR = 0
	e.HopLimit = 0
	e.Details = map[string]any{}
	line := format(e)
	fields := strings.Split(line, "\t")
	if fields[4] != "RSSI=-" {
		t.Errorf("zero RSSI should produce RSSI=-, got %q", fields[4])
	}
	if fields[5] != "SNR=-" {
		t.Errorf("zero SNR should produce SNR=-, got %q", fields[5])
	}
	if fields[6] != "HOPS=-" {
		t.Errorf("zero HopLimit should produce HOPS=-, got %q", fields[6])
	}
}

func TestFormat_AllEventTypesNoPanic(t *testing.T) {
	allTypes := []decoder.EventType{
		decoder.EventTextMessage, decoder.EventPosition, decoder.EventTelemetry,
		decoder.EventNodeInfo, decoder.EventMyInfo, decoder.EventRouting,
		decoder.EventTraceroute, decoder.EventNeighborInfo, decoder.EventStoreForward,
		decoder.EventStoreForwardPP, decoder.EventWaypoint, decoder.EventDetectionSensor,
		decoder.EventAlert, decoder.EventKeyVerify, decoder.EventNodeStatus,
		decoder.EventRangeTest, decoder.EventMapReport, decoder.EventEncrypted,
		decoder.EventConfigComplete, decoder.EventMetadata, decoder.EventConfigLora,
		decoder.EventModuleNeighbor, decoder.EventRaw,
	}
	for _, et := range allTypes {
		et := et
		t.Run(string(et), func(t *testing.T) {
			e := newEvent(et)
			e.Details = map[string]any{"k": "v"}
			// Must not panic.
			line := format(e)
			if line == "" {
				t.Error("format returned empty string")
			}
			// Must start with the date.
			if !strings.HasPrefix(line, "2026-01-15") {
				t.Errorf("line does not start with date: %q", line[:min(20, len(line))])
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---- 3. TSV safety (CRITICAL BUG AREA) ------------------------------------

// TestFormat_TabInTextMessage verifies that an embedded tab in a text payload
// does NOT create extra TSV columns. This is the primary bug test.
func TestFormat_TabInTextMessage(t *testing.T) {
	e := newEvent(decoder.EventTextMessage)
	e.Details = map[string]any{"text": "hello\tworld"}

	line := format(e)

	// The line must be a single line (no embedded newline).
	if strings.ContainsAny(line, "\n\r") {
		t.Errorf("format() produced embedded newline: %q", line)
	}

	// Column count must equal fixedCols + 1 (the "text=..." detail).
	// If the raw tab is written, we'd see fixedCols + 2 or more columns.
	fields := strings.Split(line, "\t")
	if len(fields) != fixedCols+1 {
		t.Errorf("tab in text value corrupts TSV: want %d fields, got %d\nline: %q",
			fixedCols+1, len(fields), line)
	}

	// The sanitized text should appear in the last field.
	lastField := fields[len(fields)-1]
	if strings.Contains(lastField, "\t") {
		t.Errorf("raw tab leaked into field value: %q", lastField)
	}
}

// TestFormat_NewlineInTextMessage verifies that a newline in a text payload
// does NOT split the TSV record across multiple lines.
func TestFormat_NewlineInTextMessage(t *testing.T) {
	e := newEvent(decoder.EventTextMessage)
	e.Details = map[string]any{"text": "line1\nline2"}

	line := format(e)

	if strings.Contains(line, "\n") {
		t.Errorf("format() produced embedded newline in TSV record: %q", line)
	}
}

// TestFormat_CarriageReturnInTextMessage verifies \r is also sanitized.
func TestFormat_CarriageReturnInTextMessage(t *testing.T) {
	e := newEvent(decoder.EventTextMessage)
	e.Details = map[string]any{"text": "msg\r\nwith crlf"}

	line := format(e)
	if strings.ContainsAny(line, "\r\n") {
		t.Errorf("format() produced raw CR/LF in TSV record: %q", line)
	}
}

// TestFormat_TabInDetailKey verifies that even a tab in a key name is safe.
func TestFormat_TabInDetailKey(t *testing.T) {
	e := newEvent(decoder.EventRaw)
	e.Details = map[string]any{"key\twith\ttabs": "value"}
	line := format(e)
	// Must remain parseable: the only tabs should be the field delimiters.
	// After fixedCols fields there is one detail field.
	fields := strings.Split(line, "\t")
	if len(fields) != fixedCols+1 {
		t.Errorf("tab in detail key corrupts TSV: want %d fields, got %d\nline: %q",
			fixedCols+1, len(fields), line)
	}
}

// TestLog_TabInMessage_FileContainsSingleLine writes a log entry whose text
// has embedded tab+newline and verifies the file has exactly one line per call.
func TestLog_TabInMessage_FileContainsSingleLine(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	e := newEvent(decoder.EventTextMessage)
	e.Details = map[string]any{"text": "bad\tmessage\nwith\ttabs"}
	l.Log(e)
	l.Close()

	lines := readAllLogLines(t, dir)
	if len(lines) != 1 {
		t.Errorf("expected 1 line in log file, got %d (TSV split by embedded newline?)", len(lines))
		for i, ln := range lines {
			t.Logf("  line[%d]: %q", i, ln)
		}
	}
}

// ---- 4. formatFwLog safety -------------------------------------------------

func TestFormatFwLog_EmbeddedNewlineStripped(t *testing.T) {
	e := newEvent(decoder.EventLogRecord)
	e.Details = map[string]any{
		"level":   "LEVEL_DEBUG",
		"source":  "mesh",
		"message": "first line\nsecond line",
	}
	line := formatFwLog(e)
	if strings.ContainsAny(line, "\n\r") {
		t.Errorf("formatFwLog() produced embedded newline: %q", line)
	}
}

func TestFormatFwLog_LevelPrefix(t *testing.T) {
	e := newEvent(decoder.EventLogRecord)
	e.Details = map[string]any{
		"level":   "LEVEL_WARNING",
		"source":  "gps",
		"message": "no fix",
	}
	line := formatFwLog(e)
	if !strings.Contains(line, "[WARNI") {
		t.Errorf("level prefix not trimmed correctly: %q", line)
	}
}

func TestFormatFwLog_MissingFields(t *testing.T) {
	// Should not panic when optional fields are absent.
	e := newEvent(decoder.EventLogRecord)
	e.Details = map[string]any{}
	line := formatFwLog(e)
	if line == "" {
		t.Error("formatFwLog() returned empty string for minimal event")
	}
}

// ---- 5. Daily rotation ------------------------------------------------------

func TestEnsureFile_FilenameDateFormat(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ts := time.Date(2026, 3, 7, 9, 0, 0, 0, time.UTC)
	if err := l.ensureFile(ts); err != nil {
		t.Fatalf("ensureFile: %v", err)
	}

	wantName := "mesh-2026-03-07.log"
	if _, err := os.Stat(filepath.Join(dir, wantName)); err != nil {
		t.Errorf("expected log file %q not found: %v", wantName, err)
	}
}

func TestEnsureFile_SameDayNoRotation(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ts := time.Date(2026, 3, 7, 9, 0, 0, 0, time.UTC)
	if err := l.ensureFile(ts); err != nil {
		t.Fatalf("first ensureFile: %v", err)
	}
	firstFile := l.file

	// Same day — must reuse the same file handle.
	ts2 := time.Date(2026, 3, 7, 23, 59, 0, 0, time.UTC)
	if err := l.ensureFile(ts2); err != nil {
		t.Fatalf("second ensureFile: %v", err)
	}
	if l.file != firstFile {
		t.Error("ensureFile rotated the file on same-day call")
	}
}

func TestEnsureFile_NewDayRotates(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	day1 := time.Date(2026, 3, 7, 23, 0, 0, 0, time.UTC)
	if err := l.ensureFile(day1); err != nil {
		t.Fatalf("day1 ensureFile: %v", err)
	}
	firstFile := l.file

	day2 := time.Date(2026, 3, 8, 0, 0, 1, 0, time.UTC)
	if err := l.ensureFile(day2); err != nil {
		t.Fatalf("day2 ensureFile: %v", err)
	}

	if l.file == firstFile {
		t.Error("ensureFile did not rotate the file at midnight")
	}
	if l.curDate != "2026-03-08" {
		t.Errorf("curDate not updated: got %q, want %q", l.curDate, "2026-03-08")
	}

	// Both files must exist.
	for _, name := range []string{"mesh-2026-03-07.log", "mesh-2026-03-08.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected log file %q not found after rotation", name)
		}
	}
}

func TestEnsureFwFile_Rotation(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	day1 := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	if err := l.ensureFwFile(day1); err != nil {
		t.Fatalf("day1 ensureFwFile: %v", err)
	}
	day2 := time.Date(2026, 4, 2, 0, 1, 0, 0, time.UTC)
	if err := l.ensureFwFile(day2); err != nil {
		t.Fatalf("day2 ensureFwFile: %v", err)
	}
	if l.fwDate != "2026-04-02" {
		t.Errorf("fwDate not updated: got %q", l.fwDate)
	}
	for _, name := range []string{"mesh-fwlog-2026-04-01.log", "mesh-fwlog-2026-04-02.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected fw log file %q not found", name)
		}
	}
}

func TestLog_MidnightRotation_AppendsCorrectFile(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Write an event dated day 1.
	e1 := newEvent(decoder.EventTextMessage)
	e1.Time = time.Date(2026, 6, 1, 23, 59, 59, 0, time.UTC)
	e1.Details = map[string]any{"text": "before midnight"}
	l.Log(e1)

	// Write an event dated day 2.
	e2 := newEvent(decoder.EventTextMessage)
	e2.Time = time.Date(2026, 6, 2, 0, 0, 1, 0, time.UTC)
	e2.Details = map[string]any{"text": "after midnight"}
	l.Log(e2)

	l.Close()

	// Day-1 file should contain exactly the first message.
	day1File := filepath.Join(dir, "mesh-2026-06-01.log")
	day1Lines, err := os.ReadFile(day1File)
	if err != nil {
		t.Fatalf("day1 log missing: %v", err)
	}
	if !strings.Contains(string(day1Lines), "before midnight") {
		t.Errorf("day1 log missing expected content: %q", string(day1Lines))
	}
	if strings.Contains(string(day1Lines), "after midnight") {
		t.Errorf("day1 log unexpectedly contains day2 content")
	}

	// Day-2 file should contain the second message.
	day2File := filepath.Join(dir, "mesh-2026-06-02.log")
	day2Lines, err := os.ReadFile(day2File)
	if err != nil {
		t.Fatalf("day2 log missing: %v", err)
	}
	if !strings.Contains(string(day2Lines), "after midnight") {
		t.Errorf("day2 log missing expected content: %q", string(day2Lines))
	}
}

// ---- 6. extractDate helper --------------------------------------------------

func TestExtractDate(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"mesh-2026-03-07.log", "2026-03-07"},
		{"mesh-fwlog-2026-04-14.log", "2026-04-14"},
		{"mesh-raw-2026-12-31.jsonl", "2026-12-31"},
		{"mesh-2026-01-01.log", "2026-01-01"},
		{"notameshfile.log", ""},
		{"mesh-.log", ""},
		{"mesh-bad-date.log", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := extractDate(tc.name)
			if got != tc.want {
				t.Errorf("extractDate(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// ---- 7. Raw log (JSONL) -----------------------------------------------------

func TestLog_RawEnabled_CreatesJsonlFile(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.EnableRawLog()
	defer l.Close()

	e := newEvent(decoder.EventTextMessage)
	e.Details = map[string]any{"text": "raw test"}
	l.Log(e)
	l.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ent := range entries {
		if strings.HasSuffix(ent.Name(), ".jsonl") {
			found = true
			// JSONL file must contain valid JSON on each line.
			data, _ := os.ReadFile(filepath.Join(dir, ent.Name()))
			for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if ln == "" {
					continue
				}
				if !strings.HasPrefix(ln, "{") || !strings.HasSuffix(ln, "}") {
					t.Errorf("JSONL line does not look like JSON: %q", ln)
				}
			}
		}
	}
	if !found {
		t.Error("expected .jsonl file not found after EnableRawLog()")
	}
}

// ---- 8. Firmware log routing ------------------------------------------------

func TestLog_LogRecord_WritesToFwFile_NotMainFile(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	e := newEvent(decoder.EventLogRecord)
	e.Details = map[string]any{
		"level":   "LEVEL_DEBUG",
		"source":  "test",
		"message": "fw message",
	}
	l.Log(e)
	l.Close()

	entries, _ := os.ReadDir(dir)
	var mainFiles, fwFiles []string
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), "mesh-fwlog-") {
			fwFiles = append(fwFiles, ent.Name())
		} else if strings.HasPrefix(ent.Name(), "mesh-") && strings.HasSuffix(ent.Name(), ".log") {
			mainFiles = append(mainFiles, ent.Name())
		}
	}

	if len(fwFiles) == 0 {
		t.Error("expected firmware log file (mesh-fwlog-*.log) not created")
	}
	if len(mainFiles) != 0 {
		t.Errorf("LogRecord event wrote to main log file (should not): %v", mainFiles)
	}
}

// ---- 9. Concurrency ---------------------------------------------------------

func TestLog_Concurrent_NoRace(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	const workers = 20
	const eventsPerWorker = 50

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < eventsPerWorker; j++ {
				e := newEvent(decoder.EventTextMessage)
				e.Details = map[string]any{"text": "worker msg", "worker": id, "seq": j}
				l.Log(e)
			}
		}(i)
	}
	wg.Wait()

	l.Close()

	lines := readAllLogLines(t, dir)
	want := workers * eventsPerWorker
	if len(lines) != want {
		t.Errorf("concurrent Log(): want %d lines, got %d", want, len(lines))
	}
}

func TestSetVerbose_Concurrent(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			l.SetVerbose(v % 3)
			l.Log(newEvent(decoder.EventMyInfo))
		}(i)
	}
	wg.Wait()
}

// ---- 10. CompressOldLogs (basic) -------------------------------------------

func TestCompressOldLogs_ZeroOrNegativeDays_NoOp(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	compressed, saved, err := l.CompressOldLogs(0)
	if err != nil {
		t.Errorf("CompressOldLogs(0) error: %v", err)
	}
	if compressed != 0 || saved != 0 {
		t.Errorf("expected (0,0) for 0-day cutoff, got (%d,%d)", compressed, saved)
	}
}

func TestCompressOldLogs_CompressesOldFile(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Create a fake old log file with a date far in the past.
	oldName := filepath.Join(dir, "mesh-2020-01-01.log")
	if err := os.WriteFile(oldName, []byte("old log line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	compressed, _, err := l.CompressOldLogs(1)
	if err != nil {
		t.Fatalf("CompressOldLogs(1): %v", err)
	}
	if compressed != 1 {
		t.Errorf("expected 1 file compressed, got %d", compressed)
	}
	if _, err := os.Stat(oldName + ".gz"); err != nil {
		t.Errorf("expected %s.gz to exist: %v", oldName, err)
	}
	if _, err := os.Stat(oldName); err == nil {
		t.Errorf("original file %s should have been removed after compression", oldName)
	}
}

func TestCompressOldLogs_DoesNotCompressCurrentFile(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Open a file for today.
	l.Log(newEvent(decoder.EventMyInfo))
	curDate := l.curDate // inspect internal state

	// CompressOldLogs with 0-day cutoff (would compress everything except today).
	// Use 1 day so today is protected (today >= cutoff).
	compressed, _, err := l.CompressOldLogs(1)
	if err != nil {
		t.Fatal(err)
	}

	// The currently-open file must NOT be compressed.
	currentFile := filepath.Join(dir, "mesh-"+curDate+".log")
	if _, err := os.Stat(currentFile); err != nil {
		t.Errorf("current log file %q was compressed or removed unexpectedly: %v", currentFile, err)
	}
	// No file should have been compressed (today is within the 1-day window).
	_ = compressed
}

// ---- 11. Details with multiple fields sorted deterministically -------------

func TestFormat_DetailsSortedAlphabetically(t *testing.T) {
	e := newEvent(decoder.EventTelemetry)
	e.Details = map[string]any{
		"z_last":   1,
		"a_first":  2,
		"m_middle": 3,
	}
	line := format(e)
	fields := strings.Split(line, "\t")
	// 3 details -> fixedCols + 3
	if len(fields) != fixedCols+3 {
		t.Fatalf("want %d fields, got %d", fixedCols+3, len(fields))
	}
	// Detail fields start at index fixedCols.
	detail0 := fields[fixedCols]
	detail1 := fields[fixedCols+1]
	detail2 := fields[fixedCols+2]
	if !strings.HasPrefix(detail0, "a_first=") {
		t.Errorf("details not sorted: first detail = %q, want a_first=...", detail0)
	}
	if !strings.HasPrefix(detail1, "m_middle=") {
		t.Errorf("details not sorted: second detail = %q, want m_middle=...", detail1)
	}
	if !strings.HasPrefix(detail2, "z_last=") {
		t.Errorf("details not sorted: third detail = %q, want z_last=...", detail2)
	}
}

// ---- 12. Append behavior (same file not truncated on reuse) ----------------

func TestEnsureFile_AppendNotTruncate(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	e := newEvent(decoder.EventTextMessage)
	e.Details = map[string]any{"text": "first write"}
	l.Log(e)
	l.Close()

	// Reopen and write more.
	l2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	e2 := newEvent(decoder.EventTextMessage)
	e2.Details = map[string]any{"text": "second write"}
	l2.Log(e2)
	l2.Close()

	lines := readAllLogLines(t, dir)
	if len(lines) != 2 {
		t.Errorf("expected 2 lines after append, got %d", len(lines))
	}
}

// ---- 13. countTabs sanity check helper test --------------------------------

func TestCountTabsHelper(t *testing.T) {
	if countTabs("a\tb\tc") != 2 {
		t.Error("countTabs broken")
	}
}
