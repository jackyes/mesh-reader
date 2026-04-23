// Package reader handles the low-level serial connection to a Meshtastic device.
//
// Meshtastic serial framing:
//   [0x94][0xC3][MSB len][LSB len][protobuf bytes ...]
//
// The device communicates at 115200 baud by default.
package reader

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
)

const (
	magic1    = 0x94
	magic2    = 0xC3
	maxPacket = 512
)

// Reader reads framed Meshtastic packets from a serial port or TCP connection.
type Reader struct {
	port io.ReadWriteCloser
	wmu  sync.Mutex // protects writes so heartbeat goroutine is safe
}

// DetectPort scans all serial ports **in parallel** and returns the first one
// that responds to the Meshtastic framing magic bytes.
func DetectPort(baud int) (string, error) {
	ports, err := serial.GetPortsList()
	if err != nil {
		return "", fmt.Errorf("enumerate serial ports: %w", err)
	}
	if len(ports) == 0 {
		return "", fmt.Errorf("no serial ports found")
	}

	log.Printf("[detect] found %d serial port(s): %s", len(ports), strings.Join(ports, ", "))

	type result struct {
		port string
		ok   bool
	}
	ch := make(chan result, len(ports))

	for _, portName := range ports {
		go func(name string) {
			ch <- result{port: name, ok: probePort(name, baud)}
		}(portName)
	}

	var failed []string
	for range ports {
		r := <-ch
		if r.ok {
			log.Printf("[detect] %s — Meshtastic device detected!", r.port)
			return r.port, nil
		}
		failed = append(failed, r.port)
	}

	return "", fmt.Errorf("no Meshtastic device found on any of %d port(s): %s",
		len(ports), strings.Join(failed, ", "))
}

// probePort opens a single serial port and checks if a Meshtastic device is
// present.  Strategy:
//  1. Listen passively for magic bytes (catches already-active devices).
//  2. If nothing heard, send a WantConfig probe and listen again.
func probePort(portName string, baud int) bool {
	mode := &serial.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}

	p, err := serial.Open(portName, mode)
	if err != nil {
		log.Printf("[detect] %s — cannot open: %v", portName, err)
		return false
	}
	defer p.Close()

	// Do NOT set DTR/RTS — on ESP32 boards the auto-reset circuit
	// interprets DTR high as a reset signal, rebooting the device.

	// Phase 1: send WantConfig probe immediately to wake the device,
	// then listen for magic bytes.
	// Minimal WantConfig: ToRadio { want_config_id = 1 }
	// Protobuf: field 3 (varint) = 1 → 0x18 0x01
	probe := []byte{magic1, magic2, 0x00, 0x02, 0x18, 0x01}
	p.Write(probe) // ignore error, port might be read-only or non-Meshtastic

	if listenForMagic(p, 3*time.Second) {
		return true
	}

	// Phase 2: send probe again (device may have been busy booting)
	p.Write(probe)

	if listenForMagic(p, 3*time.Second) {
		return true
	}

	log.Printf("[detect] %s — no Meshtastic response", portName)
	return false
}

// listenForMagic reads from the port for up to timeout looking for the
// Meshtastic framing header 0x94 0xC3.
func listenForMagic(p serial.Port, timeout time.Duration) bool {
	p.SetReadTimeout(200 * time.Millisecond)
	buf := make([]byte, 256)
	var prev byte
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, _ := p.Read(buf)
		for i := 0; i < n; i++ {
			if prev == magic1 && buf[i] == magic2 {
				return true
			}
			prev = buf[i]
		}
	}
	return false
}

// New opens the serial port and returns a Reader ready to use.
func New(portName string, baud int) (*Reader, error) {
	mode := &serial.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}
	p, err := serial.Open(portName, mode)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", portName, err)
	}
	return &Reader{port: p}, nil
}

// NewTCP opens a TCP connection to a Meshtastic device over WiFi/Ethernet.
// The host parameter can be "192.168.1.42" or "192.168.1.42:4403".
// Default port is 4403 (Meshtastic TCP API).
func NewTCP(host string) (*Reader, error) {
	if !strings.Contains(host, ":") {
		host = host + ":4403"
	}
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", host, err)
	}
	return &Reader{port: conn}, nil
}

// Close releases the serial port.
func (r *Reader) Close() error {
	return r.port.Close()
}

// ReadPacket blocks until a complete Meshtastic packet is received and returns
// the raw protobuf bytes (without the framing header).
func (r *Reader) ReadPacket() ([]byte, error) {
	if err := r.syncMagic(); err != nil {
		return nil, err
	}

	// 2-byte big-endian length
	var lenBuf [2]byte
	if _, err := io.ReadFull(r.port, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}
	length := binary.BigEndian.Uint16(lenBuf[:])

	if length == 0 || int(length) > maxPacket {
		return nil, fmt.Errorf("invalid packet length: %d (expected 1–%d)", length, maxPacket)
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r.port, data); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}
	return data, nil
}

// WriteFrame sends a single Meshtastic-framed packet to the device.
// Frame format: [0x94][0xC3][MSB len][LSB len][protobuf bytes ...]
func (r *Reader) WriteFrame(data []byte) error {
	if len(data) > maxPacket {
		return fmt.Errorf("outgoing packet too large: %d bytes", len(data))
	}
	frame := make([]byte, 4+len(data))
	frame[0] = magic1
	frame[1] = magic2
	binary.BigEndian.PutUint16(frame[2:], uint16(len(data)))
	copy(frame[4:], data)
	r.wmu.Lock()
	_, err := r.port.Write(frame)
	r.wmu.Unlock()
	return err
}

// syncMagic consumes bytes until the two magic bytes 0x94 0xC3 are found.
func (r *Reader) syncMagic() error {
	var b [1]byte
	for {
		if _, err := io.ReadFull(r.port, b[:]); err != nil {
			return err
		}
		if b[0] != magic1 {
			continue
		}
		if _, err := io.ReadFull(r.port, b[:]); err != nil {
			return err
		}
		if b[0] == magic2 {
			return nil
		}
		// Second byte didn't match; if it equals magic1 itself, recheck next byte.
		if b[0] == magic1 {
			if _, err := io.ReadFull(r.port, b[:]); err != nil {
				return err
			}
			if b[0] == magic2 {
				return nil
			}
		}
	}
}
