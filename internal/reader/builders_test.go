package reader

// White-box tests for admin.go, textmsg.go, and traceroute.go.
//
// Strategy: WriteFrame writes bytes into a fakePipe; we then read those bytes
// back, strip the 4-byte Meshtastic header, and unmarshal the ToRadio proto to
// verify every field is set correctly.

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// captureToRadio drains the fakePipe, strips the Meshtastic frame header, and
// unmarshals the payload as a ToRadio message.
func captureToRadio(t *testing.T, p *fakePipe) *pb.ToRadio {
	t.Helper()
	raw := p.buf.Bytes()
	if len(raw) < 4 {
		t.Fatalf("captured %d bytes, want at least 4 (header)", len(raw))
	}
	if raw[0] != magic1 || raw[1] != magic2 {
		t.Fatalf("magic bytes wrong: %02x %02x", raw[0], raw[1])
	}
	length := int(binary.BigEndian.Uint16(raw[2:4]))
	if len(raw) < 4+length {
		t.Fatalf("frame truncated: have %d bytes, need %d", len(raw), 4+length)
	}
	payload := raw[4 : 4+length]
	var tr pb.ToRadio
	if err := proto.Unmarshal(payload, &tr); err != nil {
		t.Fatalf("proto.Unmarshal ToRadio: %v", err)
	}
	return &tr
}

// newTestReader returns a Reader backed by a fresh fakePipe and returns the
// pipe for inspection.
func newTestReader() (*Reader, *fakePipe) {
	p := newFakePipe()
	return &Reader{port: p}, p
}

// ---------------------------------------------------------------------------
// SetDebugLogApi
// ---------------------------------------------------------------------------

func TestSetDebugLogApi_Enable(t *testing.T) {
	r, p := newTestReader()
	if err := r.SetDebugLogApi(true); err != nil {
		t.Fatalf("SetDebugLogApi(true): %v", err)
	}
	tr := captureToRadio(t, p)

	pkt := tr.GetPacket()
	if pkt == nil {
		t.Fatal("ToRadio.Packet is nil")
	}
	if pkt.To != 0 {
		t.Errorf("To = %d, want 0 (local node)", pkt.To)
	}
	if pkt.WantAck {
		t.Errorf("WantAck = true, want false for local admin message")
	}
	if pkt.Id == 0 {
		t.Error("Id must be non-zero")
	}

	decoded := pkt.GetDecoded()
	if decoded == nil {
		t.Fatal("MeshPacket.Decoded is nil")
	}
	if decoded.Portnum != pb.PortNum_ADMIN_APP {
		t.Errorf("Portnum = %v, want ADMIN_APP", decoded.Portnum)
	}

	// Unmarshal the nested AdminMessage.
	var adminMsg pb.AdminMessage
	if err := proto.Unmarshal(decoded.Payload, &adminMsg); err != nil {
		t.Fatalf("unmarshal AdminMessage: %v", err)
	}
	cfg := adminMsg.GetSetConfig()
	if cfg == nil {
		t.Fatal("AdminMessage.SetConfig is nil")
	}
	sec := cfg.GetSecurity()
	if sec == nil {
		t.Fatal("Config.Security is nil")
	}
	if !sec.DebugLogApiEnabled {
		t.Error("DebugLogApiEnabled = false, want true")
	}
}

func TestSetDebugLogApi_Disable(t *testing.T) {
	r, p := newTestReader()
	if err := r.SetDebugLogApi(false); err != nil {
		t.Fatalf("SetDebugLogApi(false): %v", err)
	}
	tr := captureToRadio(t, p)
	decoded := tr.GetPacket().GetDecoded()
	var adminMsg pb.AdminMessage
	if err := proto.Unmarshal(decoded.Payload, &adminMsg); err != nil {
		t.Fatalf("unmarshal AdminMessage: %v", err)
	}
	sec := adminMsg.GetSetConfig().GetSecurity()
	if sec == nil {
		t.Fatal("Config.Security is nil")
	}
	if sec.DebugLogApiEnabled {
		t.Error("DebugLogApiEnabled = true, want false")
	}
}

// ---------------------------------------------------------------------------
// SendTextMessage
// ---------------------------------------------------------------------------

func TestSendTextMessage_Basic(t *testing.T) {
	r, p := newTestReader()
	dest := uint32(0x12345678)
	text := "hello mesh"
	ch := uint32(0)
	pktID, err := r.SendTextMessage(dest, text, ch, 3)
	if err != nil {
		t.Fatalf("SendTextMessage: %v", err)
	}
	if pktID == 0 {
		t.Error("returned pktID must be non-zero")
	}

	tr := captureToRadio(t, p)
	pkt := tr.GetPacket()
	if pkt == nil {
		t.Fatal("ToRadio.Packet is nil")
	}
	if pkt.To != dest {
		t.Errorf("To = %08x, want %08x", pkt.To, dest)
	}
	if pkt.Channel != ch {
		t.Errorf("Channel = %d, want %d", pkt.Channel, ch)
	}
	if !pkt.WantAck {
		t.Error("WantAck = false, want true")
	}
	if pkt.Id != pktID {
		t.Errorf("Id = %d, want %d", pkt.Id, pktID)
	}
	if pkt.HopLimit != 3 {
		t.Errorf("HopLimit = %d, want 3", pkt.HopLimit)
	}

	decoded := pkt.GetDecoded()
	if decoded == nil {
		t.Fatal("MeshPacket.Decoded is nil")
	}
	if decoded.Portnum != pb.PortNum_TEXT_MESSAGE_APP {
		t.Errorf("Portnum = %v, want TEXT_MESSAGE_APP", decoded.Portnum)
	}
	if string(decoded.Payload) != text {
		t.Errorf("Payload = %q, want %q", string(decoded.Payload), text)
	}
}

func TestSendTextMessage_BroadcastAddress(t *testing.T) {
	r, p := newTestReader()
	dest := uint32(0xFFFFFFFF) // broadcast
	_, err := r.SendTextMessage(dest, "broadcast", 0, 0)
	if err != nil {
		t.Fatalf("SendTextMessage to broadcast: %v", err)
	}
	tr := captureToRadio(t, p)
	if tr.GetPacket().To != dest {
		t.Errorf("To = %08x, want %08x", tr.GetPacket().To, dest)
	}
}

func TestSendTextMessage_ZeroDestError(t *testing.T) {
	r, _ := newTestReader()
	_, err := r.SendTextMessage(0, "hi", 0, 0)
	if err == nil {
		t.Fatal("expected error for dest=0, got nil")
	}
}

func TestSendTextMessage_EmptyTextError(t *testing.T) {
	r, _ := newTestReader()
	_, err := r.SendTextMessage(0x1234, "", 0, 0)
	if err == nil {
		t.Fatal("expected error for empty text, got nil")
	}
}

func TestSendTextMessage_TextTruncatedAt200(t *testing.T) {
	r, p := newTestReader()
	longText := strings.Repeat("A", 300)
	_, err := r.SendTextMessage(0x1234, longText, 0, 3)
	if err != nil {
		t.Fatalf("SendTextMessage with long text: %v", err)
	}
	tr := captureToRadio(t, p)
	got := string(tr.GetPacket().GetDecoded().Payload)
	if len(got) != 200 {
		t.Errorf("payload length = %d, want 200 (truncated)", len(got))
	}
}

func TestSendTextMessage_HopLimitDefault(t *testing.T) {
	// hopLimit = 0 → should default to 3
	r, p := newTestReader()
	_, err := r.SendTextMessage(0x1234, "test", 0, 0)
	if err != nil {
		t.Fatalf("SendTextMessage: %v", err)
	}
	tr := captureToRadio(t, p)
	if tr.GetPacket().HopLimit != 3 {
		t.Errorf("HopLimit = %d, want 3 (default for hopLimit=0)", tr.GetPacket().HopLimit)
	}
}

func TestSendTextMessage_HopLimitOver7Clamp(t *testing.T) {
	// hopLimit = 8 → should clamp to 3 (per the > 7 guard)
	r, p := newTestReader()
	_, err := r.SendTextMessage(0x1234, "test", 0, 8)
	if err != nil {
		t.Fatalf("SendTextMessage: %v", err)
	}
	tr := captureToRadio(t, p)
	if tr.GetPacket().HopLimit != 3 {
		t.Errorf("HopLimit = %d, want 3 (clamped for hopLimit>7)", tr.GetPacket().HopLimit)
	}
}

func TestSendTextMessage_MaxHopLimit(t *testing.T) {
	// hopLimit = 7 is the maximum accepted value without clamping
	r, p := newTestReader()
	_, err := r.SendTextMessage(0x1234, "test", 0, 7)
	if err != nil {
		t.Fatalf("SendTextMessage: %v", err)
	}
	tr := captureToRadio(t, p)
	if tr.GetPacket().HopLimit != 7 {
		t.Errorf("HopLimit = %d, want 7", tr.GetPacket().HopLimit)
	}
}

func TestSendTextMessage_NonZeroChannel(t *testing.T) {
	r, p := newTestReader()
	_, err := r.SendTextMessage(0x1234, "test", 2, 3)
	if err != nil {
		t.Fatalf("SendTextMessage: %v", err)
	}
	tr := captureToRadio(t, p)
	if tr.GetPacket().Channel != 2 {
		t.Errorf("Channel = %d, want 2", tr.GetPacket().Channel)
	}
}

func TestSendTextMessage_PacketIdNonZero(t *testing.T) {
	// The code always ORs pktID with 1 so it can never be 0.
	r, _ := newTestReader()
	for i := 0; i < 10; i++ {
		id, err := r.SendTextMessage(0x1234, "t", 0, 3)
		if err != nil {
			t.Fatalf("SendTextMessage iteration %d: %v", i, err)
		}
		if id == 0 {
			t.Errorf("iteration %d: pktID is 0", i)
		}
	}
}

// ---------------------------------------------------------------------------
// SendTraceroute
// ---------------------------------------------------------------------------

func TestSendTraceroute_Basic(t *testing.T) {
	r, p := newTestReader()
	dest := uint32(0xABCD1234)
	if err := r.SendTraceroute(dest, 5); err != nil {
		t.Fatalf("SendTraceroute: %v", err)
	}
	tr := captureToRadio(t, p)
	pkt := tr.GetPacket()
	if pkt == nil {
		t.Fatal("ToRadio.Packet is nil")
	}
	if pkt.To != dest {
		t.Errorf("To = %08x, want %08x", pkt.To, dest)
	}
	if !pkt.WantAck {
		t.Error("WantAck = false, want true")
	}
	if pkt.HopLimit != 5 {
		t.Errorf("HopLimit = %d, want 5", pkt.HopLimit)
	}

	decoded := pkt.GetDecoded()
	if decoded == nil {
		t.Fatal("MeshPacket.Decoded is nil")
	}
	if decoded.Portnum != pb.PortNum_TRACEROUTE_APP {
		t.Errorf("Portnum = %v, want TRACEROUTE_APP", decoded.Portnum)
	}
	if !decoded.WantResponse {
		t.Error("WantResponse = false, want true")
	}

	// Payload should unmarshal as a valid (possibly empty) RouteDiscovery.
	var rd pb.RouteDiscovery
	if err := proto.Unmarshal(decoded.Payload, &rd); err != nil {
		t.Fatalf("unmarshal RouteDiscovery: %v", err)
	}
}

func TestSendTraceroute_ZeroDestError(t *testing.T) {
	r, _ := newTestReader()
	err := r.SendTraceroute(0, 5)
	if err == nil {
		t.Fatal("expected error for dest=0, got nil")
	}
}

func TestSendTraceroute_HopLimitDefault(t *testing.T) {
	// hopLimit = 0 → should default to 7
	r, p := newTestReader()
	if err := r.SendTraceroute(0x1234, 0); err != nil {
		t.Fatalf("SendTraceroute: %v", err)
	}
	tr := captureToRadio(t, p)
	if tr.GetPacket().HopLimit != 7 {
		t.Errorf("HopLimit = %d, want 7 (default for hopLimit=0)", tr.GetPacket().HopLimit)
	}
}

func TestSendTraceroute_HopLimitOver7Clamp(t *testing.T) {
	// hopLimit = 8 → clamped to 7 (unlike SendTextMessage which defaults to 3)
	r, p := newTestReader()
	if err := r.SendTraceroute(0x1234, 8); err != nil {
		t.Fatalf("SendTraceroute: %v", err)
	}
	tr := captureToRadio(t, p)
	if tr.GetPacket().HopLimit != 7 {
		t.Errorf("HopLimit = %d, want 7 (clamped)", tr.GetPacket().HopLimit)
	}
}

func TestSendTraceroute_MaxHopLimit(t *testing.T) {
	r, p := newTestReader()
	if err := r.SendTraceroute(0x1234, 7); err != nil {
		t.Fatalf("SendTraceroute: %v", err)
	}
	tr := captureToRadio(t, p)
	if tr.GetPacket().HopLimit != 7 {
		t.Errorf("HopLimit = %d, want 7", tr.GetPacket().HopLimit)
	}
}

func TestSendTraceroute_PacketIdNonZero(t *testing.T) {
	r, p := newTestReader()
	if err := r.SendTraceroute(0x1234, 5); err != nil {
		t.Fatalf("SendTraceroute: %v", err)
	}
	tr := captureToRadio(t, p)
	if tr.GetPacket().Id == 0 {
		t.Error("packet Id is 0, want non-zero")
	}
}

func TestSendTraceroute_RouteDiscoveryEmptyPayload(t *testing.T) {
	// An empty RouteDiscovery marshals to zero bytes; verify we handle this.
	r, p := newTestReader()
	if err := r.SendTraceroute(0x1234, 3); err != nil {
		t.Fatalf("SendTraceroute: %v", err)
	}
	tr := captureToRadio(t, p)
	payload := tr.GetPacket().GetDecoded().Payload
	// Empty proto marshals to nil or zero-length slice; both are valid.
	var rd pb.RouteDiscovery
	if err := proto.Unmarshal(payload, &rd); err != nil {
		t.Fatalf("unmarshal empty RouteDiscovery: %v", err)
	}
	// rd is intentionally empty — no fields to assert for an empty request.
}

// ---------------------------------------------------------------------------
// WriteFrame size guard — shared by all builders
// ---------------------------------------------------------------------------

func TestWriteFrame_MaxExactly512(t *testing.T) {
	p := newFakePipe()
	r := &Reader{port: p}
	if err := r.WriteFrame(bytes.Repeat([]byte{0x00}, maxPacket)); err != nil {
		t.Fatalf("WriteFrame(512) unexpectedly failed: %v", err)
	}
}

func TestWriteFrame_513Fails(t *testing.T) {
	p := newFakePipe()
	r := &Reader{port: p}
	err := r.WriteFrame(bytes.Repeat([]byte{0x00}, maxPacket+1))
	if err == nil {
		t.Fatal("WriteFrame(513) should fail, got nil")
	}
}
