package reader

// White-box tests for the framing logic in reader.go.
//
// None of these tests require a real serial port: they exercise syncMagic,
// ReadPacket, and WriteFrame via an in-memory io.ReadWriteCloser (fakePipe).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// fakePipe is an in-memory, thread-safe ReadWriteCloser backed by bytes.Buffer.
// Reads block until data is available (or the pipe is closed).
type fakePipe struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
	cond   *sync.Cond
}

func newFakePipe() *fakePipe {
	p := &fakePipe{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// Write appends data and wakes any blocked readers.
func (p *fakePipe) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	n, err := p.buf.Write(data)
	p.cond.Broadcast()
	return n, err
}

// Read blocks until at least one byte is available or the pipe is closed.
func (p *fakePipe) Read(out []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.buf.Len() == 0 {
		if p.closed {
			return 0, io.EOF
		}
		p.cond.Wait()
	}
	return p.buf.Read(out)
}

func (p *fakePipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.cond.Broadcast()
	return nil
}

// writeStr writes bytes from a string representation into the pipe directly
// (convenience wrapper for byte-sequence tests).
func (p *fakePipe) push(data ...byte) {
	p.Write(data)
}

// buildFrame returns the 4+len(payload) wire bytes for a single frame.
func buildFrame(payload []byte) []byte {
	frame := make([]byte, 4+len(payload))
	frame[0] = magic1
	frame[1] = magic2
	binary.BigEndian.PutUint16(frame[2:], uint16(len(payload)))
	copy(frame[4:], payload)
	return frame
}

// readerWith returns a *Reader backed by a fakePipe that already contains
// the supplied bytes.
func readerWith(data []byte) (*Reader, *fakePipe) {
	p := newFakePipe()
	p.Write(data)
	return &Reader{port: p}, p
}

// ---------------------------------------------------------------------------
// WriteFrame tests
// ---------------------------------------------------------------------------

func TestWriteFrame_Basic(t *testing.T) {
	p := newFakePipe()
	r := &Reader{port: p}
	payload := []byte{0x01, 0x02, 0x03}
	if err := r.WriteFrame(payload); err != nil {
		t.Fatalf("WriteFrame returned error: %v", err)
	}
	got := p.buf.Bytes()
	want := buildFrame(payload)
	if !bytes.Equal(got, want) {
		t.Errorf("WriteFrame output = %x, want %x", got, want)
	}
}

func TestWriteFrame_Empty(t *testing.T) {
	// Empty payload: length field is 0 — valid at the wire level for WriteFrame
	// (the guard is only in ReadPacket).
	p := newFakePipe()
	r := &Reader{port: p}
	if err := r.WriteFrame(nil); err != nil {
		t.Fatalf("WriteFrame(nil) returned error: %v", err)
	}
	got := p.buf.Bytes()
	if len(got) != 4 {
		t.Errorf("expected 4 header bytes, got %d: %x", len(got), got)
	}
	if got[0] != magic1 || got[1] != magic2 {
		t.Errorf("magic bytes wrong: %x %x", got[0], got[1])
	}
	if got[2] != 0x00 || got[3] != 0x00 {
		t.Errorf("length bytes should be 0, got %x %x", got[2], got[3])
	}
}

func TestWriteFrame_MaxPayload(t *testing.T) {
	p := newFakePipe()
	r := &Reader{port: p}
	payload := bytes.Repeat([]byte{0xAB}, maxPacket)
	if err := r.WriteFrame(payload); err != nil {
		t.Fatalf("WriteFrame(%d bytes) returned error: %v", maxPacket, err)
	}
}

func TestWriteFrame_TooLarge(t *testing.T) {
	p := newFakePipe()
	r := &Reader{port: p}
	payload := bytes.Repeat([]byte{0xAB}, maxPacket+1)
	err := r.WriteFrame(payload)
	if err == nil {
		t.Fatal("expected error for oversized payload, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error should mention 'too large', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ReadPacket — single complete frame
// ---------------------------------------------------------------------------

func TestReadPacket_SingleFrame(t *testing.T) {
	payload := []byte{0xAA, 0xBB, 0xCC}
	r, _ := readerWith(buildFrame(payload))
	got, err := r.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %x, want %x", got, payload)
	}
}

func TestReadPacket_MaxSizeFrame(t *testing.T) {
	payload := bytes.Repeat([]byte{0xCC}, maxPacket)
	r, _ := readerWith(buildFrame(payload))
	got, err := r.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("max-size frame: payload mismatch (len %d vs %d)", len(got), len(payload))
	}
}

func TestReadPacket_SingleBytePayload(t *testing.T) {
	payload := []byte{0xFF}
	r, _ := readerWith(buildFrame(payload))
	got, err := r.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("single-byte payload: got %x, want %x", got, payload)
	}
}

// ---------------------------------------------------------------------------
// ReadPacket — multiple frames concatenated
// ---------------------------------------------------------------------------

func TestReadPacket_MultipleFrames(t *testing.T) {
	payloads := [][]byte{
		{0x01, 0x02},
		{0x03, 0x04, 0x05},
		{0x06},
	}
	var wire []byte
	for _, p := range payloads {
		wire = append(wire, buildFrame(p)...)
	}
	r, _ := readerWith(wire)
	for i, want := range payloads {
		got, err := r.ReadPacket()
		if err != nil {
			t.Fatalf("frame %d: ReadPacket error: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("frame %d: got %x, want %x", i, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// ReadPacket — garbage / noise bytes before valid frame
// ---------------------------------------------------------------------------

func TestReadPacket_GarbageBeforeFrame(t *testing.T) {
	payload := []byte{0xDE, 0xAD}
	noise := []byte{0x00, 0x11, 0x22, 0x33, 0xFF}
	wire := append(noise, buildFrame(payload)...)
	r, _ := readerWith(wire)
	got, err := r.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket with leading noise error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %x, want %x", got, payload)
	}
}

func TestReadPacket_Lone0x94BeforeFrame(t *testing.T) {
	// 0x94 not followed by 0xC3 — should be skipped, not treated as frame start.
	payload := []byte{0xBE, 0xEF}
	wire := []byte{0x94, 0x00, 0x11} // lone 0x94 then garbage
	wire = append(wire, buildFrame(payload)...)
	r, _ := readerWith(wire)
	got, err := r.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket after lone 0x94 error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %x, want %x", got, payload)
	}
}

func TestReadPacket_0x94ThenMagic1Again(t *testing.T) {
	// Sequence: 0x94 0x94 0xC3 — second 0x94 followed by 0xC3 is the real header.
	payload := []byte{0x01}
	// Frame with a spurious leading 0x94
	wire := []byte{0x94} // lone magic1
	wire = append(wire, buildFrame(payload)...)
	r, _ := readerWith(wire)
	got, err := r.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket with 0x94 0x94 0xC3 error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %x, want %x", got, payload)
	}
}

// TestReadPacket_TripleMagic1 exercises the pattern 0x94 0x94 0x94 0xC3
// which revealed the syncMagic bug where the third 0x94 was discarded.
func TestReadPacket_TripleMagic1(t *testing.T) {
	payload := []byte{0x01, 0x02}
	// Wire: 0x94 0x94 0x94 <real frame>
	wire := []byte{0x94, 0x94} // two spurious 0x94 before the frame
	wire = append(wire, buildFrame(payload)...)
	r, _ := readerWith(wire)
	got, err := r.ReadPacket()
	if err != nil {
		t.Fatalf("triple-magic1 sequence error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("triple-magic1: got %x, want %x", got, payload)
	}
}

// ---------------------------------------------------------------------------
// ReadPacket — split reads (partial frame arrives in pieces)
// ---------------------------------------------------------------------------

// TestReadPacket_SplitFrame verifies that a frame whose bytes arrive in
// separate writes is still assembled correctly.  We push bytes into the pipe
// from a goroutine with intentional pauses.
func TestReadPacket_SplitFrame(t *testing.T) {
	payload := []byte{0x11, 0x22, 0x33, 0x44}
	frame := buildFrame(payload)

	p := newFakePipe()
	r := &Reader{port: p}

	// Feed the frame in two halves concurrently.
	go func() {
		half := len(frame) / 2
		p.Write(frame[:half])
		// A real runtime.Gosched() is not deterministic; since fakePipe
		// blocks readers on empty buffer, just send second half immediately.
		p.Write(frame[half:])
	}()

	got, err := r.ReadPacket()
	if err != nil {
		t.Fatalf("split-frame ReadPacket error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("split-frame: got %x, want %x", got, payload)
	}
}

func TestReadPacket_SplitHeader(t *testing.T) {
	// Header split: first write delivers only 0x94; rest follows.
	payload := []byte{0xAA}
	frame := buildFrame(payload)

	p := newFakePipe()
	r := &Reader{port: p}

	go func() {
		p.Write(frame[:1])  // just 0x94
		p.Write(frame[1:])  // 0xC3 + len + payload
	}()

	got, err := r.ReadPacket()
	if err != nil {
		t.Fatalf("split-header ReadPacket error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("split-header: got %x, want %x", got, payload)
	}
}

func TestReadPacket_ByteByByte(t *testing.T) {
	// Deliver frame one byte at a time — maximum split stress test.
	payload := []byte{0x01, 0x02, 0x03}
	frame := buildFrame(payload)

	p := newFakePipe()
	r := &Reader{port: p}

	go func() {
		for _, b := range frame {
			p.Write([]byte{b})
		}
	}()

	got, err := r.ReadPacket()
	if err != nil {
		t.Fatalf("byte-by-byte ReadPacket error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("byte-by-byte: got %x, want %x", got, payload)
	}
}

// ---------------------------------------------------------------------------
// ReadPacket — length field edge cases
// ---------------------------------------------------------------------------

func TestReadPacket_ZeroLength(t *testing.T) {
	// Length = 0 must return an error, not panic.
	wire := []byte{magic1, magic2, 0x00, 0x00}
	r, p := readerWith(wire)
	// Close so ReadPacket doesn't block forever waiting for payload.
	p.Close()
	_, err := r.ReadPacket()
	if err == nil {
		t.Fatal("expected error for length=0, got nil")
	}
}

func TestReadPacket_OverMaxLength(t *testing.T) {
	// Length = maxPacket+1 must return an error, not try to allocate huge buf.
	overLen := uint16(maxPacket + 1)
	wire := []byte{magic1, magic2, byte(overLen >> 8), byte(overLen & 0xFF)}
	r, p := readerWith(wire)
	p.Close()
	_, err := r.ReadPacket()
	if err == nil {
		t.Fatal("expected error for over-max length, got nil")
	}
	if !strings.Contains(err.Error(), "invalid packet length") {
		t.Errorf("error should mention 'invalid packet length', got: %v", err)
	}
}

func TestReadPacket_IncompletePayload(t *testing.T) {
	// Length field says 10 bytes but only 3 payload bytes arrive then EOF.
	wire := []byte{magic1, magic2, 0x00, 0x0A, 0x01, 0x02, 0x03}
	r, p := readerWith(wire)
	p.Close() // trigger EOF
	_, err := r.ReadPacket()
	if err == nil {
		t.Fatal("expected error for incomplete payload, got nil")
	}
}

func TestReadPacket_EmptyInput(t *testing.T) {
	// No input at all: ReadPacket should return EOF, not panic.
	p := newFakePipe()
	p.Close()
	r := &Reader{port: p}
	_, err := r.ReadPacket()
	if err == nil {
		t.Fatal("expected error/EOF on empty input, got nil")
	}
	if !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "EOF") {
		t.Logf("non-EOF error on empty input (acceptable): %v", err)
	}
}

// ---------------------------------------------------------------------------
// syncMagic — resynchronization edge cases (white-box tests)
// ---------------------------------------------------------------------------

func TestSyncMagic_ImmediateMatch(t *testing.T) {
	p := newFakePipe()
	p.Write([]byte{magic1, magic2})
	p.Close()
	r := &Reader{port: p}
	if err := r.syncMagic(); err != nil {
		t.Fatalf("syncMagic immediate match error: %v", err)
	}
}

func TestSyncMagic_GarbageFirst(t *testing.T) {
	p := newFakePipe()
	p.Write([]byte{0x00, 0x11, 0xFF, magic1, magic2})
	p.Close()
	r := &Reader{port: p}
	if err := r.syncMagic(); err != nil {
		t.Fatalf("syncMagic with leading garbage error: %v", err)
	}
}

func TestSyncMagic_DoubleMagic1(t *testing.T) {
	// 0x94 0x94 0xC3 — second 0x94 starts the real pair.
	p := newFakePipe()
	p.Write([]byte{magic1, magic1, magic2})
	p.Close()
	r := &Reader{port: p}
	if err := r.syncMagic(); err != nil {
		t.Fatalf("syncMagic 0x94 0x94 0xC3 error: %v", err)
	}
}

// TestSyncMagic_TripleMagic1 is the regression test for the discovered bug:
// the old implementation read 0x94, then 0x94 (inner if), then 0x94 (not
// magic2), then fell through — losing the third 0x94 and never finding 0xC3.
func TestSyncMagic_TripleMagic1(t *testing.T) {
	// 0x94 0x94 0x94 0xC3 — must still sync.
	p := newFakePipe()
	p.Write([]byte{magic1, magic1, magic1, magic2})
	p.Close()
	r := &Reader{port: p}
	if err := r.syncMagic(); err != nil {
		t.Fatalf("syncMagic triple-magic1 error: %v", err)
	}
}

func TestSyncMagic_EOF(t *testing.T) {
	p := newFakePipe()
	p.Close()
	r := &Reader{port: p}
	err := r.syncMagic()
	if err == nil {
		t.Fatal("expected error from syncMagic on closed pipe, got nil")
	}
}

// ---------------------------------------------------------------------------
// WriteFrame + ReadPacket round-trip
// ---------------------------------------------------------------------------

func TestRoundTrip_WriteRead(t *testing.T) {
	// A pipe where the write end feeds the read end directly.
	p := newFakePipe()
	r := &Reader{port: p}

	payloads := [][]byte{
		{0x01},
		{0xAA, 0xBB, 0xCC, 0xDD},
		bytes.Repeat([]byte{0x55}, 100),
	}

	for i, want := range payloads {
		if err := r.WriteFrame(want); err != nil {
			t.Fatalf("frame %d WriteFrame error: %v", i, err)
		}
		got, err := r.ReadPacket()
		if err != nil {
			t.Fatalf("frame %d ReadPacket error: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("frame %d: got %x, want %x", i, got, want)
		}
	}
}
