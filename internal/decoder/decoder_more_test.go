package decoder

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

// decodePort marshals a payload proto, wraps it in a Data packet on the given
// portnum, and decodes it. Returns the resulting event.
func decodePort(t *testing.T, port pb.PortNum, payloadProto proto.Message) *Event {
	t.Helper()
	var payload []byte
	if payloadProto != nil {
		var err error
		payload, err = proto.Marshal(payloadProto)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
	}
	pkt := makeDataPacket(port, payload)
	ev, err := New().Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev == nil {
		t.Fatal("Decode returned nil event")
	}
	return ev
}

// ---- Telemetry: all variants ----

func TestDecodeTelemetryEnvironmentFull(t *testing.T) {
	tel := &pb.Telemetry{
		Variant: &pb.Telemetry_EnvironmentMetrics{
			EnvironmentMetrics: &pb.EnvironmentMetrics{
				Temperature:        proto.Float32(21.5),
				RelativeHumidity:   proto.Float32(55.0),
				BarometricPressure: proto.Float32(1013.2),
				GasResistance:      proto.Float32(12000),
				Voltage:            proto.Float32(3.7),
				Current:            proto.Float32(120),
				Iaq:                proto.Uint32(42),
				Distance:           proto.Float32(1500),
				Lux:                proto.Float32(300),
				WindDirection:      proto.Uint32(180),
				WindSpeed:          proto.Float32(4.2),
				SoilMoisture:       proto.Uint32(33),
				SoilTemperature:    proto.Float32(18.0),
			},
		},
	}
	ev := decodePort(t, pb.PortNum_TELEMETRY_APP, tel)
	if ev.Type != EventTelemetry {
		t.Fatalf("type = %s", ev.Type)
	}
	if ev.Details["type"] != "environment" {
		t.Errorf("type detail = %v", ev.Details["type"])
	}
	if got, _ := ev.Details["temperature_c"].(float32); got != 21.5 {
		t.Errorf("temperature_c = %v", ev.Details["temperature_c"])
	}
	if got, _ := ev.Details["relative_humidity_%"].(float32); got != 55.0 {
		t.Errorf("humidity = %v", ev.Details["relative_humidity_%"])
	}
	if got, _ := ev.Details["iaq"].(uint32); got != 42 {
		t.Errorf("iaq = %v", ev.Details["iaq"])
	}
	if _, ok := ev.Details["soil_moisture_%"]; !ok {
		t.Error("soil_moisture_% missing")
	}
}

func TestDecodeTelemetryEnvironmentEmpty(t *testing.T) {
	// All optional fields nil -> only "type" present, no panic.
	tel := &pb.Telemetry{Variant: &pb.Telemetry_EnvironmentMetrics{EnvironmentMetrics: &pb.EnvironmentMetrics{}}}
	ev := decodePort(t, pb.PortNum_TELEMETRY_APP, tel)
	if ev.Details["type"] != "environment" {
		t.Fatalf("type = %v", ev.Details["type"])
	}
	if _, ok := ev.Details["temperature_c"]; ok {
		t.Error("temperature_c should be absent when nil")
	}
}

func TestDecodeTelemetryAirQuality(t *testing.T) {
	tel := &pb.Telemetry{
		Variant: &pb.Telemetry_AirQualityMetrics{
			AirQualityMetrics: &pb.AirQualityMetrics{
				Pm10Standard:  proto.Uint32(10),
				Pm25Standard:  proto.Uint32(25),
				Pm100Standard: proto.Uint32(100),
			},
		},
	}
	ev := decodePort(t, pb.PortNum_TELEMETRY_APP, tel)
	if ev.Details["type"] != "air_quality" {
		t.Fatalf("type = %v", ev.Details["type"])
	}
	if got, _ := ev.Details["pm25_standard"].(uint32); got != 25 {
		t.Errorf("pm25 = %v", ev.Details["pm25_standard"])
	}
}

func TestDecodeTelemetryPower(t *testing.T) {
	tel := &pb.Telemetry{
		Variant: &pb.Telemetry_PowerMetrics{
			PowerMetrics: &pb.PowerMetrics{
				Ch1Voltage: proto.Float32(3.3),
				Ch1Current: proto.Float32(50),
				Ch2Voltage: proto.Float32(5.0),
			},
		},
	}
	ev := decodePort(t, pb.PortNum_TELEMETRY_APP, tel)
	if ev.Details["type"] != "power" {
		t.Fatalf("type = %v", ev.Details["type"])
	}
	if got, _ := ev.Details["ch1_voltage_v"].(float32); got != 3.3 {
		t.Errorf("ch1_voltage_v = %v", ev.Details["ch1_voltage_v"])
	}
	if _, ok := ev.Details["ch3_voltage_v"]; ok {
		t.Error("ch3_voltage_v should be absent")
	}
}

func TestDecodeTelemetryLocalStats(t *testing.T) {
	tel := &pb.Telemetry{
		Variant: &pb.Telemetry_LocalStats{
			LocalStats: &pb.LocalStats{
				UptimeSeconds:      3600,
				ChannelUtilization: 12.5,
				AirUtilTx:          3.2,
				NumPacketsTx:       100,
				NumPacketsRx:       200,
				NumPacketsRxBad:    5,
				NumOnlineNodes:     10,
				NumTotalNodes:      25,
				NumRxDupe:          7,
				NumTxRelay:         3,
				NumTxRelayCanceled: 1,
				HeapTotalBytes:     200000,
				HeapFreeBytes:      150000,
				NumTxDropped:       2,
				NoiseFloor:         -95,
			},
		},
	}
	ev := decodePort(t, pb.PortNum_TELEMETRY_APP, tel)
	if ev.Details["type"] != "local_stats" {
		t.Fatalf("type = %v", ev.Details["type"])
	}
	if got, _ := ev.Details["uptime_seconds"].(uint32); got != 3600 {
		t.Errorf("uptime = %v", ev.Details["uptime_seconds"])
	}
	if got, _ := ev.Details["noise_floor_dbm"].(int32); got != -95 {
		t.Errorf("noise_floor_dbm = %v", ev.Details["noise_floor_dbm"])
	}
	if got, _ := ev.Details["num_rx_dupe"].(uint32); got != 7 {
		t.Errorf("num_rx_dupe = %v", ev.Details["num_rx_dupe"])
	}
}

func TestDecodeTelemetryHealth(t *testing.T) {
	tel := &pb.Telemetry{
		Variant: &pb.Telemetry_HealthMetrics{
			HealthMetrics: &pb.HealthMetrics{
				HeartBpm:    proto.Uint32(72),
				SpO2:        proto.Uint32(98),
				Temperature: proto.Float32(36.6),
			},
		},
	}
	ev := decodePort(t, pb.PortNum_TELEMETRY_APP, tel)
	if ev.Details["type"] != "health" {
		t.Fatalf("type = %v", ev.Details["type"])
	}
	if got, _ := ev.Details["heart_bpm"].(uint32); got != 72 {
		t.Errorf("heart_bpm = %v", ev.Details["heart_bpm"])
	}
}

func TestDecodeTelemetryDeviceAllNil(t *testing.T) {
	tel := &pb.Telemetry{Variant: &pb.Telemetry_DeviceMetrics{DeviceMetrics: &pb.DeviceMetrics{}}}
	ev := decodePort(t, pb.PortNum_TELEMETRY_APP, tel)
	if ev.Details["type"] != "device" {
		t.Fatalf("type = %v", ev.Details["type"])
	}
	if _, ok := ev.Details["battery_level_%"]; ok {
		t.Error("battery should be absent when nil")
	}
}

func TestDecodeTelemetryNoVariant(t *testing.T) {
	// Telemetry with no variant set: should not panic, no "type" key.
	tel := &pb.Telemetry{}
	ev := decodePort(t, pb.PortNum_TELEMETRY_APP, tel)
	if ev.Type != EventTelemetry {
		t.Fatalf("type = %s", ev.Type)
	}
	if _, ok := ev.Details["type"]; ok {
		t.Error("no variant => no type key expected")
	}
}

// ---- Position edge cases ----

func TestDecodePositionNegativeCoords(t *testing.T) {
	pos := &pb.Position{
		LatitudeI:  proto.Int32(-451234567), // southern hemisphere
		LongitudeI: proto.Int32(-1221234567),
	}
	ev := decodePort(t, pb.PortNum_POSITION_APP, pos)
	lat, _ := ev.Details["lat"].(float64)
	if lat > -45.12 || lat < -45.13 {
		t.Errorf("lat = %v, want ~-45.123", lat)
	}
	lon, _ := ev.Details["lon"].(float64)
	if lon > -122.12 || lon < -122.13 {
		t.Errorf("lon = %v, want ~-122.123", lon)
	}
}

func TestDecodePositionEmpty(t *testing.T) {
	// No lat/lon/alt; sats_in_view must STILL be present (always set).
	pos := &pb.Position{}
	ev := decodePort(t, pb.PortNum_POSITION_APP, pos)
	if _, ok := ev.Details["lat"]; ok {
		t.Error("lat should be absent")
	}
	if _, ok := ev.Details["sats_in_view"]; !ok {
		t.Error("sats_in_view must always be present")
	}
}

// ---- NodeInfo (FromRadio) ----

func TestDecodeNodeInfoFull(t *testing.T) {
	ni := &pb.NodeInfo{
		Num: 0xAABBCCDD,
		Snr: 5.5,
		User: &pb.User{
			Id:        "!aabbccdd",
			LongName:  "Test Node",
			ShortName: "TN",
		},
		Position: &pb.Position{
			LatitudeI:  proto.Int32(451234567),
			LongitudeI: proto.Int32(91234567),
			Altitude:   proto.Int32(200),
		},
	}
	fr := &pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: ni}}
	data, _ := proto.Marshal(fr)
	ev, err := New().Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventNodeInfo {
		t.Fatalf("type = %s", ev.Type)
	}
	if ev.FromNode != 0xAABBCCDD {
		t.Errorf("FromNode = %08x", ev.FromNode)
	}
	if ev.Details["long_name"] != "Test Node" {
		t.Errorf("long_name = %v", ev.Details["long_name"])
	}
	if _, ok := ev.Details["lat"]; !ok {
		t.Error("lat missing")
	}
	if got, _ := ev.Details["altitude"].(int32); got != 200 {
		t.Errorf("altitude = %v", ev.Details["altitude"])
	}
}

func TestDecodeNodeInfoMinimal(t *testing.T) {
	// No User, no Position -> only num + snr.
	ni := &pb.NodeInfo{Num: 0x11223344, Snr: -1.5}
	fr := &pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: ni}}
	data, _ := proto.Marshal(fr)
	ev, _ := New().Decode(data)
	if ev.Details["num"] != "!11223344" {
		t.Errorf("num = %v", ev.Details["num"])
	}
	if _, ok := ev.Details["long_name"]; ok {
		t.Error("long_name should be absent without User")
	}
}

// ---- NeighborInfo empty ----

func TestDecodeNeighborInfoEmpty(t *testing.T) {
	ni := &pb.NeighborInfo{NodeId: 0x12345678}
	ev := decodePort(t, pb.PortNum_NEIGHBORINFO_APP, ni)
	if c, _ := ev.Details["neighbor_count"].(int); c != 0 {
		t.Errorf("neighbor_count = %v, want 0", ev.Details["neighbor_count"])
	}
	neighbors, ok := ev.Details["neighbors"].([]map[string]any)
	if !ok || len(neighbors) != 0 {
		t.Errorf("neighbors = %v, want empty slice", ev.Details["neighbors"])
	}
}

// ---- Routing ----

func TestDecodeRoutingMalformed(t *testing.T) {
	// Malformed routing payload: decoder swallows the error and returns
	// EventRouting with a size hint (graceful degradation).
	pkt := makeDataPacket(pb.PortNum_ROUTING_APP, []byte{0xFF, 0xFF, 0xFF})
	ev, err := New().Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventRouting {
		t.Fatalf("type = %s", ev.Type)
	}
}

// ---- Store & Forward ----

func TestDecodeStoreForwardVariants(t *testing.T) {
	cases := []struct {
		name string
		sf   *pb.StoreAndForward
		want string
	}{
		{"heartbeat", &pb.StoreAndForward{Variant: &pb.StoreAndForward_Heartbeat_{Heartbeat: &pb.StoreAndForward_Heartbeat{}}}, "heartbeat"},
		{"stats", &pb.StoreAndForward{Variant: &pb.StoreAndForward_Stats{Stats: &pb.StoreAndForward_Statistics{}}}, "stats"},
		{"text", &pb.StoreAndForward{Variant: &pb.StoreAndForward_Text{Text: []byte("hi")}}, "text"},
		{"none", &pb.StoreAndForward{}, "none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := decodePort(t, pb.PortNum_STORE_FORWARD_APP, c.sf)
			if ev.Type != EventStoreForward {
				t.Fatalf("type = %s", ev.Type)
			}
			if ev.Details["variant"] != c.want {
				t.Errorf("variant = %v, want %s", ev.Details["variant"], c.want)
			}
		})
	}
}

func TestStoreForwardVariantHistory(t *testing.T) {
	sf := &pb.StoreAndForward{Variant: &pb.StoreAndForward_History_{History: &pb.StoreAndForward_History{}}}
	if got := storeForwardVariant(sf); got != "history" {
		t.Errorf("history variant = %q", got)
	}
}

// ---- KeyVerify phase logic ----

func TestDecodeKeyVerifyPhases(t *testing.T) {
	cases := []struct {
		name string
		kv   *pb.KeyVerification
		want string
	}{
		{"init", &pb.KeyVerification{Nonce: 1}, "init"},
		{"response", &pb.KeyVerification{Nonce: 2, Hash2: []byte{0x01}}, "response"},
		{"final", &pb.KeyVerification{Nonce: 3, Hash1: []byte{0x02}}, "final"},
		{"final_takes_precedence", &pb.KeyVerification{Hash1: []byte{0x01}, Hash2: []byte{0x02}}, "final"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := decodePort(t, pb.PortNum_KEY_VERIFICATION_APP, c.kv)
			if ev.Type != EventKeyVerify {
				t.Fatalf("type = %s", ev.Type)
			}
			if ev.Details["phase"] != c.want {
				t.Errorf("phase = %v, want %s", ev.Details["phase"], c.want)
			}
		})
	}
}

// ---- Simple text-based ports ----

func TestDecodeTextLikePorts(t *testing.T) {
	cases := []struct {
		port pb.PortNum
		typ  EventType
	}{
		{pb.PortNum_DETECTION_SENSOR_APP, EventDetectionSensor},
		{pb.PortNum_ALERT_APP, EventAlert},
		{pb.PortNum_RANGE_TEST_APP, EventRangeTest},
	}
	for _, c := range cases {
		t.Run(string(c.typ), func(t *testing.T) {
			pkt := makeDataPacket(c.port, []byte("payload-text"))
			ev, _ := New().Decode(wrapMeshPacket(t, pkt))
			if ev.Type != c.typ {
				t.Fatalf("type = %s, want %s", ev.Type, c.typ)
			}
			if ev.Details["text"] != "payload-text" {
				t.Errorf("text = %v", ev.Details["text"])
			}
		})
	}
}

func TestDecodeNodeStatus(t *testing.T) {
	pkt := makeDataPacket(pb.PortNum_NODE_STATUS_APP, []byte{1, 2, 3, 4})
	ev, _ := New().Decode(wrapMeshPacket(t, pkt))
	if ev.Type != EventNodeStatus {
		t.Fatalf("type = %s", ev.Type)
	}
	if sz, _ := ev.Details["size"].(int); sz != 4 {
		t.Errorf("size = %v, want 4", ev.Details["size"])
	}
}

func TestDecodeNodeInfoApp(t *testing.T) {
	user := &pb.User{Id: "!deadbeef", LongName: "Long", ShortName: "L"}
	ev := decodePort(t, pb.PortNum_NODEINFO_APP, user)
	if ev.Type != EventNodeInfo {
		t.Fatalf("type = %s", ev.Type)
	}
	if ev.Details["long_name"] != "Long" {
		t.Errorf("long_name = %v", ev.Details["long_name"])
	}
}

// ---- MapReport: lat/lon only when nonzero ----

func TestDecodeMapReportWithPosition(t *testing.T) {
	mr := &pb.MapReport{
		LongName:   "Mapper",
		ShortName:  "MP",
		LatitudeI:  451234567,
		LongitudeI: 91234567,
		Altitude:   100,
	}
	ev := decodePort(t, pb.PortNum_MAP_REPORT_APP, mr)
	if ev.Type != EventMapReport {
		t.Fatalf("type = %s", ev.Type)
	}
	if _, ok := ev.Details["lat"]; !ok {
		t.Error("lat should be present")
	}
	if got, _ := ev.Details["altitude"].(int32); got != 100 {
		t.Errorf("altitude = %v", ev.Details["altitude"])
	}
}

func TestDecodeMapReportNoPosition(t *testing.T) {
	mr := &pb.MapReport{LongName: "NoPos", ShortName: "NP"}
	ev := decodePort(t, pb.PortNum_MAP_REPORT_APP, mr)
	if _, ok := ev.Details["lat"]; ok {
		t.Error("lat should be absent when LatitudeI==0 && LongitudeI==0")
	}
	if _, ok := ev.Details["altitude"]; ok {
		t.Error("altitude should be absent when 0")
	}
}

// ---- Waypoint ----

func TestDecodeWaypointNoPosition(t *testing.T) {
	w := &pb.Waypoint{Id: 7, Name: "WP"}
	ev := decodePort(t, pb.PortNum_WAYPOINT_APP, w)
	if ev.Type != EventWaypoint {
		t.Fatalf("type = %s", ev.Type)
	}
	if _, ok := ev.Details["lat"]; ok {
		t.Error("lat should be absent when LatitudeI nil")
	}
	if ev.Details["name"] != "WP" {
		t.Errorf("name = %v", ev.Details["name"])
	}
}

// ---- Port handler error path -> EventRaw ----

func TestDecodeHandlerErrorBecomesRaw(t *testing.T) {
	// A telemetry port with bytes that fail proto.Unmarshal -> EventRaw with
	// portnum + error keys (decodeMeshPacket error branch).
	pkt := makeDataPacket(pb.PortNum_TELEMETRY_APP, []byte{0x08}) // varint tag, no value -> EOF
	ev, err := New().Decode(wrapMeshPacket(t, pkt))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventRaw {
		t.Fatalf("type = %s, want RAW", ev.Type)
	}
	if _, ok := ev.Details["error"]; !ok {
		t.Error("expected error key in details")
	}
	if _, ok := ev.Details["portnum"]; !ok {
		t.Error("expected portnum key in details")
	}
}

// ---- FromRadio variants ----

func TestDecodeMetadata(t *testing.T) {
	fr := &pb.FromRadio{
		PayloadVariant: &pb.FromRadio_Metadata{
			Metadata: &pb.DeviceMetadata{
				FirmwareVersion: "2.5.0",
				HasWifi:         true,
				HasBluetooth:    true,
			},
		},
	}
	data, _ := proto.Marshal(fr)
	ev, err := New().Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventMetadata {
		t.Fatalf("type = %s", ev.Type)
	}
	if ev.Details["firmware_version"] != "2.5.0" {
		t.Errorf("fw = %v", ev.Details["firmware_version"])
	}
	if ev.Details["has_wifi"] != true {
		t.Errorf("has_wifi = %v", ev.Details["has_wifi"])
	}
}

func TestDecodeConfigLora(t *testing.T) {
	fr := &pb.FromRadio{
		PayloadVariant: &pb.FromRadio_Config{
			Config: &pb.Config{
				PayloadVariant: &pb.Config_Lora{
					Lora: &pb.Config_LoRaConfig{
						Region:     pb.Config_LoRaConfig_EU_868,
						HopLimit:   3,
						TxPower:    20,
						TxEnabled:  true,
						UsePreset:  true,
						ChannelNum: 20,
					},
				},
			},
		},
	}
	data, _ := proto.Marshal(fr)
	ev, err := New().Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventConfigLora {
		t.Fatalf("type = %s", ev.Type)
	}
	if got, _ := ev.Details["hop_limit"].(uint32); got != 3 {
		t.Errorf("hop_limit = %v", ev.Details["hop_limit"])
	}
}

func TestDecodeConfigNonLoraReturnsEmptyEvent(t *testing.T) {
	// A Config payload that is not LoRa returns a non-nil event with empty Type.
	fr := &pb.FromRadio{
		PayloadVariant: &pb.FromRadio_Config{
			Config: &pb.Config{
				PayloadVariant: &pb.Config_Device{Device: &pb.Config_DeviceConfig{}},
			},
		},
	}
	data, _ := proto.Marshal(fr)
	ev, err := New().Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.Type != "" {
		t.Errorf("expected empty Type, got %s", ev.Type)
	}
}

func TestDecodeModuleConfigNeighborInfo(t *testing.T) {
	fr := &pb.FromRadio{
		PayloadVariant: &pb.FromRadio_ModuleConfig{
			ModuleConfig: &pb.ModuleConfig{
				PayloadVariant: &pb.ModuleConfig_NeighborInfo{
					NeighborInfo: &pb.ModuleConfig_NeighborInfoConfig{
						Enabled:        true,
						UpdateInterval: 14400,
					},
				},
			},
		},
	}
	data, _ := proto.Marshal(fr)
	ev, err := New().Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventModuleNeighbor {
		t.Fatalf("type = %s", ev.Type)
	}
	if ev.Details["enabled"] != true {
		t.Errorf("enabled = %v", ev.Details["enabled"])
	}
}

func TestDecodeLogRecord(t *testing.T) {
	fr := &pb.FromRadio{
		PayloadVariant: &pb.FromRadio_LogRecord{
			LogRecord: &pb.LogRecord{
				Level:   pb.LogRecord_WARNING,
				Source:  "RadioIf",
				Message: "Lora RX something",
			},
		},
	}
	data, _ := proto.Marshal(fr)
	ev, err := New().Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if ev.Type != EventLogRecord {
		t.Fatalf("type = %s", ev.Type)
	}
	if ev.Details["message"] != "Lora RX something" {
		t.Errorf("message = %v", ev.Details["message"])
	}
}

// ---- Decode: empty / unknown FromRadio -> (nil, nil) ----

func TestDecodeEmptyInput(t *testing.T) {
	ev, err := New().Decode([]byte{})
	if err != nil {
		t.Fatalf("Decode empty: %v", err)
	}
	if ev != nil {
		t.Errorf("expected nil event for empty FromRadio, got %+v", ev)
	}
}

// ---- nodeStr / storeForwardVariant unexported helpers ----

func TestStoreForwardVariantNil(t *testing.T) {
	if got := storeForwardVariant(&pb.StoreAndForward{}); got != "none" {
		t.Errorf("nil variant = %q, want none", got)
	}
}
