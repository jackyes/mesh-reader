// Package logger writes decoded Meshtastic events to rotating daily text files.
//
// Each day produces a file named  mesh-YYYY-MM-DD.log  in the configured directory.
// Every line is human-readable and tab-separated for easy parsing with tools like
// grep, awk, or Python's csv module (using \t as delimiter).
//
// Format:
//   TIMESTAMP  TYPE  FROM  TO  RSSI  SNR  HOPS  key=value ...
package logger

import (
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mesh-reader/internal/decoder"
)

// Logger writes events to rotating daily log files.
type Logger struct {
	dir     string
	mu      sync.Mutex
	file    *os.File
	curDate string

	// Optional raw log (JSONL, one full packet per line including hex bytes).
	rawEnabled bool
	rawFile    *os.File
	rawDate    string

	// Firmware debug log (one LogRecord per line).
	fwFile *os.File
	fwDate string

	// Console verbosity: 0=quiet (no event lines to stdout),
	// 1=normal mesh events, 2=also firmware debug log lines.
	verbose int
}

// New creates a Logger that writes files into dir (which is created if needed).
func New(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir %q: %w", dir, err)
	}
	return &Logger{dir: dir}, nil
}

// SetVerbose sets console verbosity:
//
//	0 = quiet (events only go to log files, nothing to stdout)
//	1 = normal mesh events printed to stdout
//	2 = also firmware debug log lines printed to stdout
func (l *Logger) SetVerbose(level int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.verbose = level
}

// EnableRawLog turns on the raw packet log (JSONL format).
// Call this right after New().
func (l *Logger) EnableRawLog() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rawEnabled = true
}

// Dir returns the directory where logs are written.
func (l *Logger) Dir() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dir
}

// CompressOldLogs scans the log directory and gzips any .log/.jsonl files
// older than olderThanDays days (based on filename date). The currently open
// files are never touched. Returns the count of files compressed and space saved.
func (l *Logger) CompressOldLogs(olderThanDays int) (compressed int, savedBytes int64, err error) {
	if olderThanDays <= 0 {
		return 0, 0, nil
	}
	l.mu.Lock()
	dir := l.dir
	cutoffDate := time.Now().AddDate(0, 0, -olderThanDays).Format("2006-01-02")
	curDate := l.curDate
	fwDate := l.fwDate
	rawDate := l.rawDate
	l.mu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Only process our own files, skip already-compressed
		if strings.HasSuffix(name, ".gz") {
			continue
		}
		if !(strings.HasPrefix(name, "mesh-") && (strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".jsonl"))) {
			continue
		}
		// Extract YYYY-MM-DD date from filename
		date := extractDate(name)
		if date == "" || date >= cutoffDate {
			continue
		}
		// Never compress files currently being written
		if date == curDate || date == fwDate || date == rawDate {
			continue
		}
		src := filepath.Join(dir, name)
		dst := src + ".gz"
		// Skip if destination already exists
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		origSize, err := gzipFile(src, dst)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[logger] gzip %s: %v\n", name, err)
			continue
		}
		newSize, _ := os.Stat(dst)
		var saved int64
		if newSize != nil {
			saved = origSize - newSize.Size()
		}
		if err := os.Remove(src); err != nil {
			fmt.Fprintf(os.Stderr, "[logger] remove original %s: %v\n", name, err)
			continue
		}
		compressed++
		savedBytes += saved
	}
	return compressed, savedBytes, nil
}

// extractDate returns the YYYY-MM-DD portion of a "mesh-<prefix-?>YYYY-MM-DD.<ext>" name.
func extractDate(name string) string {
	// Strip extension
	for _, ext := range []string{".log", ".jsonl"} {
		if strings.HasSuffix(name, ext) {
			name = strings.TrimSuffix(name, ext)
			break
		}
	}
	// Take last 10 chars
	if len(name) < 10 {
		return ""
	}
	d := name[len(name)-10:]
	if d[4] != '-' || d[7] != '-' {
		return ""
	}
	return d
}

// gzipFile writes a gzipped copy of src to dst and returns the original size.
func gzipFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	info, _ := in.Stat()
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	gz, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		return 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, err
	}
	if info != nil {
		return info.Size(), nil
	}
	return 0, nil
}

// Close flushes and closes the current log files.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
	}
	if l.rawFile != nil {
		_ = l.rawFile.Close()
	}
	if l.fwFile != nil {
		_ = l.fwFile.Close()
	}
	return nil
}

// Log writes event to the current day's log file and also prints it to stdout.
func (l *Logger) Log(event *decoder.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Firmware debug log records go to their own file (mesh-fwlog-YYYY-MM-DD.log)
	// and are NOT duplicated into the normal mesh log.
	if event.Type == decoder.EventLogRecord {
		if err := l.ensureFwFile(event.Time); err != nil {
			fmt.Fprintf(os.Stderr, "[logger] cannot open fw file: %v\n", err)
			return
		}
		line := formatFwLog(event)
		fmt.Fprintln(l.fwFile, line)
		if l.verbose >= 2 {
			fmt.Println(line)
		}
		return
	}

	if err := l.ensureFile(event.Time); err != nil {
		fmt.Fprintf(os.Stderr, "[logger] cannot open file: %v\n", err)
		return
	}

	line := format(event)
	fmt.Fprintln(l.file, line)
	if l.verbose >= 1 {
		fmt.Println(line)
	}

	// Raw log (full packet as JSONL with hex bytes)
	if l.rawEnabled {
		if err := l.ensureRawFile(event.Time); err != nil {
			fmt.Fprintf(os.Stderr, "[logger] cannot open raw file: %v\n", err)
			return
		}
		if rawLine, err := formatRaw(event); err == nil {
			fmt.Fprintln(l.rawFile, rawLine)
		}
	}
}

// ensureFile opens (or rotates) the log file for the given time.
func (l *Logger) ensureFile(t time.Time) error {
	date := t.Format("2006-01-02")
	if date == l.curDate && l.file != nil {
		return nil
	}
	if l.file != nil {
		_ = l.file.Close()
	}
	name := filepath.Join(l.dir, fmt.Sprintf("mesh-%s.log", date))
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	l.file = f
	l.curDate = date
	return nil
}

// ensureFwFile opens (or rotates) the firmware debug log file.
func (l *Logger) ensureFwFile(t time.Time) error {
	date := t.Format("2006-01-02")
	if date == l.fwDate && l.fwFile != nil {
		return nil
	}
	if l.fwFile != nil {
		_ = l.fwFile.Close()
	}
	name := filepath.Join(l.dir, fmt.Sprintf("mesh-fwlog-%s.log", date))
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	l.fwFile = f
	l.fwDate = date
	return nil
}

// formatFwLog renders a LogRecord event as a single readable line:
//
//	2026-04-14 12:05:33  [DEBUG]  source  message
func formatFwLog(e *decoder.Event) string {
	var sb strings.Builder
	sb.WriteString(e.Time.Format("2006-01-02 15:04:05"))
	sb.WriteString("  ")

	level := "?"
	if v, ok := e.Details["level"].(string); ok {
		// level comes in as "LEVEL_DEBUG" etc. — trim the prefix
		level = strings.TrimPrefix(v, "LEVEL_")
	}
	sb.WriteString(fmt.Sprintf("[%-5s]", level))
	sb.WriteString("  ")

	if v, ok := e.Details["source"].(string); ok && v != "" {
		sb.WriteString(v)
		sb.WriteString("  ")
	}

	if v, ok := e.Details["message"].(string); ok {
		sb.WriteString(strings.TrimRight(v, "\r\n"))
	}
	return sb.String()
}

// ensureRawFile opens (or rotates) the raw JSONL file for the given time.
func (l *Logger) ensureRawFile(t time.Time) error {
	date := t.Format("2006-01-02")
	if date == l.rawDate && l.rawFile != nil {
		return nil
	}
	if l.rawFile != nil {
		_ = l.rawFile.Close()
	}
	name := filepath.Join(l.dir, fmt.Sprintf("mesh-raw-%s.jsonl", date))
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	l.rawFile = f
	l.rawDate = date
	return nil
}

// formatRaw returns a single-line JSON representation of the event,
// including the decoded details and the raw protobuf bytes in hex.
func formatRaw(e *decoder.Event) (string, error) {
	obj := map[string]any{
		"time":      e.Time.Format(time.RFC3339Nano),
		"type":      string(e.Type),
		"from":      fmt.Sprintf("!%08x", e.FromNode),
		"to":        fmt.Sprintf("!%08x", e.ToNode),
		"rssi":      e.RSSI,
		"snr":       e.SNR,
		"hop_limit": e.HopLimit,
		"hop_start": e.HopStart,
		"packet_id": e.PacketID,
		"via_mqtt":  e.ViaMqtt,
		"details":   e.Details,
		"raw_hex":   hex.EncodeToString(e.RawBytes),
		"raw_len":   len(e.RawBytes),
	}
	if e.RelayNode != 0 {
		obj["relay_node"] = fmt.Sprintf("..%02x", e.RelayNode&0xFF)
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// format converts an Event into a single human-readable line.
func format(e *decoder.Event) string {
	var sb strings.Builder

	// Timestamp
	sb.WriteString(e.Time.Format("2006-01-02 15:04:05"))
	sb.WriteByte('\t')

	// Event type (fixed-width for alignment)
	sb.WriteString(fmt.Sprintf("%-14s", string(e.Type)))
	sb.WriteByte('\t')

	// From / To (only meaningful for mesh packets)
	if e.FromNode != 0 {
		sb.WriteString(fmt.Sprintf("FROM=!%08x", e.FromNode))
	} else {
		sb.WriteString("FROM=-")
	}
	sb.WriteByte('\t')

	if e.ToNode == 0xFFFFFFFF {
		sb.WriteString("TO=^all")
	} else if e.ToNode != 0 {
		sb.WriteString(fmt.Sprintf("TO=!%08x", e.ToNode))
	} else {
		sb.WriteString("TO=-")
	}
	sb.WriteByte('\t')

	// Radio signal quality
	if e.RSSI != 0 {
		sb.WriteString(fmt.Sprintf("RSSI=%d", e.RSSI))
	} else {
		sb.WriteString("RSSI=-")
	}
	sb.WriteByte('\t')

	if e.SNR != 0 {
		sb.WriteString(fmt.Sprintf("SNR=%.1f", e.SNR))
	} else {
		sb.WriteString("SNR=-")
	}
	sb.WriteByte('\t')

	if e.HopLimit != 0 {
		sb.WriteString(fmt.Sprintf("HOPS=%d", e.HopLimit))
	} else {
		sb.WriteString("HOPS=-")
	}

	// Sorted details for deterministic output
	if len(e.Details) > 0 {
		keys := make([]string, 0, len(e.Details))
		for k := range e.Details {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sb.WriteByte('\t')
			sb.WriteString(fmt.Sprintf("%s=%v", k, e.Details[k]))
		}
	}

	return sb.String()
}
