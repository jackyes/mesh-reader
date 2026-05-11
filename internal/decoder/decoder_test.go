package decoder

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

func wrapMeshPacket(t *testing.T, pkt *pb.MeshPacket) []byte {
	t.Helper()
	fromRadio := &pb.FromRadio{
		PayloadVariant: &pb.FromRadio_Packet{Packet: pkt},
	}
	data, err := proto.Marshal(fromRadio)
	if err != nil {
		t.Fatalf("marshal FromRadio: %v", err)
	}
	return data
}

func makeDataPacket(portnum pb.PortNum, payload []byte) *pb.MeshPacket {
	return &pb.MeshPacket{
		From:     0x12345678,
		To:       0x9ABCDEF0,
		RxRssi:   -42,
		RxSnr:    6.5,
		HopLimit: 2,
		HopStart: 3,
		Id:       100,
		PayloadVariant: &pb.MeshPacket_Decoded{
			Decoded: &pb.Data{
				Portnum: portnum,
				Payload: payload,
			},
		},
	}
}

func TestDecodeTextMessage(t *testing.T) {
	pkt := makeDataPacket(pb.PortNum_TEXT_MESSAGE_APP, []byte("hello mesh"))
	d := New()
	ev, err := d.Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventTextMessage {
		t.Errorf("expected TEXT_MESSAGE, got %s", ev.Type)
	}
	if text, _ := ev.Details["text"].(string); text != "hello mesh" {
		t.Errorf("expected 'hello mesh', got %q", text)
	}
}

func TestDecodePosition(t *testing.T) {
	lat := int32(451234567) // 45.1234567 degrees
	lon := int32(91234567)  // 9.1234567 degrees
	alt := int32(150)
	pos := &pb.Position{
		LatitudeI:  &lat,
		LongitudeI: &lon,
		Altitude:   &alt,
		SatsInView: 8,
	}
	payload, err := proto.Marshal(pos)
	if err != nil {
		t.Fatalf("marshal Position: %v", err)
	}
	pkt := makeDataPacket(pb.PortNum_POSITION_APP, payload)
	d := New()
	ev, err := d.Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventPosition {
		t.Errorf("expected POSITION, got %s", ev.Type)
	}
	if lat, _ := ev.Details["lat"].(float64); lat < 45.12 || lat > 45.13 {
		t.Errorf("lat out of range: %v", lat)
	}
	if sats, _ := ev.Details["sats_in_view"].(uint32); sats != 8 {
		t.Errorf("expected sats=8, got %v", sats)
	}
}

func TestDecodeTelemetryDevice(t *testing.T) {
	batt := uint32(78)
	volt := float32(3.9)
	cu := float32(12.5)
	tel := &pb.Telemetry{
		Variant: &pb.Telemetry_DeviceMetrics{
			DeviceMetrics: &pb.DeviceMetrics{
				BatteryLevel:       &batt,
				Voltage:            &volt,
				ChannelUtilization: &cu,
			},
		},
	}
	payload, _ := proto.Marshal(tel)
	pkt := makeDataPacket(pb.PortNum_TELEMETRY_APP, payload)
	d := New()
	ev, err := d.Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventTelemetry {
		t.Errorf("expected TELEMETRY, got %s", ev.Type)
	}
	if typ, _ := ev.Details["type"].(string); typ != "device" {
		t.Errorf("expected type=device, got %q", typ)
	}
}

func TestDecodeNeighborInfo(t *testing.T) {
	ni := &pb.NeighborInfo{
		NodeId:                    0xAABBCCDD,
		NodeBroadcastIntervalSecs: 14400,
		Neighbors: []*pb.Neighbor{
			{NodeId: 0x11223344, Snr: 3.5},
			{NodeId: 0x55667788, Snr: -2.0},
		},
	}
	payload, _ := proto.Marshal(ni)
	pkt := makeDataPacket(pb.PortNum_NEIGHBORINFO_APP, payload)
	d := New()
	ev, err := d.Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventNeighborInfo {
		t.Errorf("expected NEIGHBORINFO_APP, got %s", ev.Type)
	}
	count, _ := ev.Details["neighbor_count"].(int)
	if count != 2 {
		t.Errorf("expected 2 neighbors, got %d", count)
	}
}

func TestDecodeTraceroute(t *testing.T) {
	rd := &pb.RouteDiscovery{
		Route:      []uint32{0x11111111, 0x22222222, 0x33333333},
		SnrTowards: []int32{40, 20, 10},
		RouteBack:  []uint32{0x33333333, 0x22222222, 0x11111111},
		SnrBack:    []int32{10, 20, 40},
	}
	payload, _ := proto.Marshal(rd)
	pkt := makeDataPacket(pb.PortNum_TRACEROUTE_APP, payload)
	d := New()
	ev, err := d.Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventTraceroute {
		t.Errorf("expected TRACEROUTE, got %s", ev.Type)
	}
	route, _ := ev.Details["route"].([]string)
	if len(route) != 3 {
		t.Errorf("expected 3-hop route, got %d", len(route))
	}
}

func TestDecodeEncrypted(t *testing.T) {
	pkt := &pb.MeshPacket{
		From: 0x12345678,
		To:   0xFFFFFFFF,
		// No Decoded payload — this is an encrypted packet
		PayloadVariant: &pb.MeshPacket_Encrypted{
			Encrypted: []byte{0x01, 0x02, 0x03},
		},
	}
	d := New()
	ev, err := d.Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventEncrypted {
		t.Errorf("expected ENCRYPTED, got %s", ev.Type)
	}
}

func TestDecodeUnknownPortnum(t *testing.T) {
	unknownPort := pb.PortNum(9999)
	pkt := makeDataPacket(unknownPort, []byte{0xAA, 0xBB})
	d := New()
	ev, err := d.Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventRaw {
		t.Errorf("expected RAW for unknown portnum, got %s", ev.Type)
	}
}

func TestDecodeWaypoint(t *testing.T) {
	lat := int32(451234567)
	lon := int32(91234567)
	w := &pb.Waypoint{
		Id:          42,
		Name:        "Meeting Point",
		Description: "Main entrance",
		Icon:        0x1F4CD,
		Expire:      3600,
		LatitudeI:   &lat,
		LongitudeI:  &lon,
	}
	payload, _ := proto.Marshal(w)
	pkt := makeDataPacket(pb.PortNum_WAYPOINT_APP, payload)
	d := New()
	ev, err := d.Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventWaypoint {
		t.Errorf("expected WAYPOINT, got %s", ev.Type)
	}
	name, _ := ev.Details["name"].(string)
	if name != "Meeting Point" {
		t.Errorf("expected 'Meeting Point', got %q", name)
	}
}

func TestDecodeMyInfo(t *testing.T) {
	mi := &pb.FromRadio{
		PayloadVariant: &pb.FromRadio_MyInfo{
			MyInfo: &pb.MyNodeInfo{
				MyNodeNum:   0xABCD1234,
				RebootCount: 3,
				PioEnv:      "tbeam",
				NodedbCount: 15,
			},
		},
	}
	data, _ := proto.Marshal(mi)
	d := New()
	ev, err := d.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventMyInfo {
		t.Errorf("expected MY_INFO, got %s", ev.Type)
	}
	if ev.FromNode != 0xABCD1234 {
		t.Errorf("expected from=0xABCD1234, got 0x%08X", ev.FromNode)
	}
}

func TestDecodeConfigComplete(t *testing.T) {
	fr := &pb.FromRadio{
		PayloadVariant: &pb.FromRadio_ConfigCompleteId{
			ConfigCompleteId: 42,
		},
	}
	data, _ := proto.Marshal(fr)
	d := New()
	ev, err := d.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventConfigComplete {
		t.Errorf("expected CONFIG_COMPLETE, got %s", ev.Type)
	}
}

func TestDecodeRoutingError(t *testing.T) {
	routing := &pb.Routing{
		Variant: &pb.Routing_ErrorReason{
			ErrorReason: pb.Routing_NONE,
		},
	}
	payload, _ := proto.Marshal(routing)
	pkt := makeDataPacket(pb.PortNum_ROUTING_APP, payload)
	d := New()
	ev, err := d.Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventRouting {
		t.Errorf("expected ROUTING, got %s", ev.Type)
	}
}

func TestDecodeMalformedProtobuf(t *testing.T) {
	// Garbage bytes that won't parse as protobuf
	framed := make([]byte, 6)
	framed[0] = 0x94
	framed[1] = 0xC3
	framed[2] = 0x00
	framed[3] = 0x02
	framed[4] = 0xFF
	framed[5] = 0xFF
	d := New()
	_, err := d.Decode(framed)
	if err == nil {
		t.Error("expected error for malformed protobuf")
	}
}

func TestPacketMetadata(t *testing.T) {
	pkt := &pb.MeshPacket{
		From:      0xAABBCCDD,
		To:        0x11223344,
		RxRssi:    -85,
		RxSnr:     2.5,
		HopLimit:  1,
		HopStart:  5,
		Id:        999,
		RelayNode: 0xEE,
		ViaMqtt:   true,
		PayloadVariant: &pb.MeshPacket_Decoded{
			Decoded: &pb.Data{
				Portnum: pb.PortNum_TEXT_MESSAGE_APP,
				Payload: []byte("test"),
			},
		},
	}
	d := New()
	ev, err := d.Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.FromNode != 0xAABBCCDD {
		t.Errorf("FromNode: expected 0xAABBCCDD, got 0x%08X", ev.FromNode)
	}
	if ev.ToNode != 0x11223344 {
		t.Errorf("ToNode: expected 0x11223344, got 0x%08X", ev.ToNode)
	}
	if ev.RSSI != -85 {
		t.Errorf("RSSI: expected -85, got %d", ev.RSSI)
	}
	if ev.SNR != 2.5 {
		t.Errorf("SNR: expected 2.5, got %f", ev.SNR)
	}
	if ev.HopStart != 5 {
		t.Errorf("HopStart: expected 5, got %d", ev.HopStart)
	}
	if ev.HopLimit != 1 {
		t.Errorf("HopLimit: expected 1, got %d", ev.HopLimit)
	}
	if ev.PacketID != 999 {
		t.Errorf("PacketID: expected 999, got %d", ev.PacketID)
	}
	if ev.RelayNode != 0xEE {
		t.Errorf("RelayNode: expected 0xEE, got 0x%02X", ev.RelayNode)
	}
	if !ev.ViaMqtt {
		t.Error("ViaMqtt: expected true")
	}
}

func TestNodeStr(t *testing.T) {
	if s := nodeStr(0xFFFFFFFF); s != "^all" {
		t.Errorf("expected ^all, got %q", s)
	}
	if s := nodeStr(0x12345678); s != "!12345678" {
		t.Errorf("expected !12345678, got %q", s)
	}
}
