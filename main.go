// mesh-reader — capture and log all communications from a Meshtastic serial node.
//
// Usage:
//
//	mesh-reader [--port COM3] [--baud 115200] [--log-dir ./logs] [--web-port :8080] [--db mesh.db] [--ignore-node MESA]
//
// If --port is omitted, the program scans all serial ports and auto-detects
// the first Meshtastic device by probing for the protocol magic bytes.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"mesh-reader/internal/app"
)

func main() {
	port := flag.String("port", "", "Serial port, e.g. COM3 (Windows) or /dev/ttyUSB0 (Linux/macOS)")
	host := flag.String("host", "", "WiFi/TCP host of the Meshtastic node, e.g. 192.168.1.42 (port 4403 by default)")
	baud := flag.Int("baud", 115200, "Baud rate")
	logDir := flag.String("log-dir", "./logs", "Directory where log files are stored")
	rawLog := flag.Bool("raw-log", false, "Also write a raw packet log (JSONL with hex bytes) in the log directory")
	enableDebugLog := flag.Bool("enable-debug-log", false, "Ask the node to stream firmware debug log (LogRecord) via the API")
	disableDebugLog := flag.Bool("disable-debug-log", false, "Ask the node to stop streaming firmware debug log, then exit")
	webPort := flag.String("web-port", ":8111", "HTTP port for the web dashboard, e.g. :8080 (use 'off' to disable)")
	dbPath := flag.String("db", "mesh.db", "SQLite database path for persistence")
	dbRetention := flag.Int("db-retention-days", 30, "Delete events/signals/snapshots older than N days (0 = keep forever)")
	logCompressDays := flag.Int("log-compress-days", 7, "Gzip .txt/.jsonl log files older than N days (0 = disabled)")
	ignoreNode := flag.String("ignore-node", "", "Short name of a node whose telemetry should be discarded (e.g. MESA)")
	notIgnoreSelf := flag.Bool("not-ignore-self", false, "Also count our own node's telemetry/position events")
	verbose := flag.Int("verbose", 0, "Console verbosity: 0=quiet, 1=packets, 2=debug")
	flag.Parse()

	// Normalize web port shorthand.
	if strings.EqualFold(*webPort, "off") || *webPort == "-" {
		*webPort = ""
	} else if *webPort != "" && !strings.Contains(*webPort, ":") {
		*webPort = ":" + *webPort
	}

	application, err := app.New(app.Config{
		Port:            *port,
		Host:            *host,
		Baud:            *baud,
		LogDir:          *logDir,
		RawLog:          *rawLog,
		EnableDebugLog:  *enableDebugLog,
		DisableDebugLog: *disableDebugLog,
		WebPort:         *webPort,
		DBPath:          *dbPath,
		DBRetention:     *dbRetention,
		LogCompressDays: *logCompressDays,
		IgnoreNode:      *ignoreNode,
		NotIgnoreSelf:   *notIgnoreSelf,
		Verbose:         *verbose,
	})
	if err != nil {
		app.ExitWithPause(err.Error())
	}

	// Run the app in a goroutine so we can catch SIGINT/SIGTERM.
	errCh := make(chan error, 1)
	go func() {
		errCh <- application.Run()
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sig:
		log.Println("[mesh-reader] shutting down...")
	case err := <-errCh:
		if err != nil {
			log.Printf("[mesh-reader] fatal error: %v", err)
		}
	}

	application.Shutdown()
}
