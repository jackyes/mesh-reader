// Package decoder turns raw Meshtastic protobuf bytes into structured Event values.
package decoder

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

// EventType identifies the kind of data contained in an Event.
type EventType string

const (
	EventTextMessage    EventType = "TEXT_MESSAGE"
	EventPosition       EventType = "POSITION"
	EventTelemetry      EventType = "TELEMETRY"
	EventNodeInfo       EventType = "NODE_INFO"
	EventMyInfo         EventType = "MY_INFO"
	EventRouting        EventType = "ROUTING"
	EventTraceroute     EventType = "TRACEROUTE"
	EventLogRecord      EventType = "LOG_RECORD"
	EventNeighborInfo   EventType = "NEIGHBOR_INFO"
	EventStoreForward   EventType = "STORE_FORWARD"
	EventStoreForwardPP EventType = "STORE_FORWARD_PP"
	EventWaypoint       EventType = "WAYPOINT"
	EventDetectionSensor EventType = "DETECT_SENSOR"
	EventAlert          EventType = "ALERT"
	EventKeyVerify      EventType = "KEY_VERIFY"
	EventNodeStatus     EventType = "NODE_STATUS"
	EventRangeTest      EventType = "RANGE_TEST"
	EventMapReport      EventType = "MAP_REPORT"
	EventEncrypted      EventType = "ENCRYPTED"
	EventConfigComplete EventType = "CONFIG_COMPLETE"
	EventMetadata       EventType = "METADATA"
	EventConfigLora     EventType = "CONFIG_LORA"
	EventModuleNeighbor EventType = "MOD_NEIGHBOR"
	EventRaw            EventType = "RAW"
)

// Event is a decoded Meshtastic packet ready for logging or further processing.
type Event struct {
	Time     time.Time
	Type     EventType
	FromNode uint32
	ToNode   uint32
	RSSI     int32
	SNR      float32
	HopLimit uint32
	HopStart uint32
	PacketID uint32
	RelayNode uint32
	ViaMqtt  bool
	Channel  uint32
	// Class is assigned by the store after Add() runs (one of
	// "personal", "broadcast", "from_me", "transit"). Empty for
	// firmware-internal events (NodeInfo via config, MyInfo, Metadata, …).
	Class    string
	Details  map[string]any
	RawBytes []byte
}

// Decoder decodes FromRadio protobuf payloads into Events.
type Decoder struct{}

func New() *Decoder { return &Decoder{} }

// Decode parses a FromRadio protobuf payload and returns an Event.
func (d *Decoder) Decode(data []byte) (*Event, error) {
	fromRadio := &pb.FromRadio{}
	if err := proto.Unmarshal(data, fromRadio); err != nil {
		return nil, fmt.Errorf("unmarshal FromRadio: %w", err)
	}

	event := &Event{
		Time:     time.Now(),
		RawBytes: data,
	}

	switch p := fromRadio.PayloadVariant.(type) {
	case *pb.FromRadio_Packet:
		return d.decodeMeshPacket(event, p.Packet)

	case *pb.FromRadio_MyInfo:
		event.Type = EventMyInfo
		mi := p.MyInfo
		event.FromNode = mi.MyNodeNum
		event.Details = map[string]any{
			"my_node_num":  fmt.Sprintf("!%08x", mi.MyNodeNum),
			"reboot_count": mi.RebootCount,
			"pio_env":      mi.PioEnv,
			"nodedb_count": mi.NodedbCount,
		}

	case *pb.FromRadio_Metadata:
		event.Type = EventMetadata
		m := p.Metadata
		event.Details = map[string]any{
			"firmware_version":     m.FirmwareVersion,
			"hw_model":             m.HwModel.String(),
			"role":                 m.Role.String(),
			"has_wifi":             m.HasWifi,
			"has_bluetooth":        m.HasBluetooth,
			"has_pkc":              m.HasPKC,
			"can_shutdown":         m.CanShutdown,
			"device_state_version": m.DeviceStateVersion,
		}

	case *pb.FromRadio_Config:
		switch cfg := p.Config.GetPayloadVariant().(type) {
		case *pb.Config_Lora:
			event.Type = EventConfigLora
			lc := cfg.Lora
			event.Details = map[string]any{
				"region":        lc.Region.String(),
				"modem_preset":  lc.ModemPreset.String(),
				"use_preset":    lc.UsePreset,
				"hop_limit":     lc.HopLimit,
				"tx_power":      lc.TxPower,
				"tx_enabled":    lc.TxEnabled,
				"bandwidth":     lc.Bandwidth,
				"spread_factor": lc.SpreadFactor,
				"coding_rate":   lc.CodingRate,
				"channel_num":   lc.ChannelNum,
			}
		default:
			return event, nil
		}

	case *pb.FromRadio_ModuleConfig:
		switch m := p.ModuleConfig.GetPayloadVariant().(type) {
		case *pb.ModuleConfig_NeighborInfo:
			event.Type = EventModuleNeighbor
			ni := m.NeighborInfo
			event.Details = map[string]any{
				"enabled":              ni.Enabled,
				"update_interval_sec":  ni.UpdateInterval,
				"transmit_over_lora":   ni.TransmitOverLora,
			}
		default:
			return event, nil
		}

	case *pb.FromRadio_NodeInfo:
		event.Type = EventNodeInfo
		ni := p.NodeInfo
		event.FromNode = ni.Num
		event.Details = decodeNodeInfoDetails(ni)

	case *pb.FromRadio_ConfigCompleteId:
		event.Type = EventConfigComplete
		event.Details = map[string]any{
			"config_id": p.ConfigCompleteId,
		}

	case *pb.FromRadio_LogRecord:
		event.Type = EventLogRecord
		event.Details = map[string]any{
			"level":   p.LogRecord.Level.String(),
			"source":  p.LogRecord.Source,
			"message": p.LogRecord.Message,
		}

	default:
		return nil, nil
	}

	return event, nil
}

// portHandler decodes a Data payload for a specific PortNum.
// Returns (EventType, details, error). The caller applies these to the Event.
type portHandler func(payload []byte) (EventType, map[string]any, error)

// portHandlers maps PortNum to its decoder function. New portnums are added here.
var portHandlers = map[pb.PortNum]portHandler{
	pb.PortNum_TEXT_MESSAGE_APP:      decodeTextMessage,
	pb.PortNum_POSITION_APP:          decodePosition,
	pb.PortNum_TELEMETRY_APP:         decodeTelemetry,
	pb.PortNum_NODEINFO_APP:          decodeNodeInfoApp,
	pb.PortNum_NEIGHBORINFO_APP:      decodeNeighborInfo,
	pb.PortNum_ROUTING_APP:           decodeRouting,
	pb.PortNum_TRACEROUTE_APP:        decodeTraceroute,
	pb.PortNum_STORE_FORWARD_APP:     decodeStoreForward,
	pb.PortNum_STORE_FORWARD_PLUSPLUS_APP: decodeStoreForward,
	pb.PortNum_WAYPOINT_APP:          decodeWaypoint,
	pb.PortNum_DETECTION_SENSOR_APP:  decodeDetectionSensor,
	pb.PortNum_ALERT_APP:             decodeAlert,
	pb.PortNum_KEY_VERIFICATION_APP:  decodeKeyVerify,
	pb.PortNum_NODE_STATUS_APP:       decodeNodeStatus,
	pb.PortNum_RANGE_TEST_APP:        decodeRangeTest,
	pb.PortNum_MAP_REPORT_APP:        decodeMapReport,
}

func (d *Decoder) decodeMeshPacket(event *Event, pkt *pb.MeshPacket) (*Event, error) {
	event.FromNode = pkt.From
	event.ToNode = pkt.To
	event.RSSI = pkt.RxRssi
	event.SNR = pkt.RxSnr
	event.HopLimit = pkt.HopLimit
	event.HopStart = pkt.HopStart
	event.PacketID = pkt.Id
	event.RelayNode = pkt.RelayNode
	event.ViaMqtt = pkt.ViaMqtt
	event.Channel = pkt.Channel

	decoded := pkt.GetDecoded()
	if decoded == nil {
		event.Type = EventEncrypted
		event.Details = map[string]any{
			"note": "packet is encrypted (no shared key)",
			"from": fmt.Sprintf("!%08x", pkt.From),
			"to":   nodeStr(pkt.To),
		}
		return event, nil
	}

	if h, ok := portHandlers[decoded.Portnum]; ok {
		typ, details, err := h(decoded.Payload)
		if err != nil {
			event.Type = EventRaw
			event.Details = map[string]any{
				"portnum": decoded.Portnum.String(),
				"error":   err.Error(),
			}
			return event, nil
		}
		event.Type = typ
		event.Details = details
		return event, nil
	}

	event.Type = EventRaw
	event.Details = map[string]any{
		"portnum": decoded.Portnum.String(),
		"size":    len(decoded.Payload),
	}
	return event, nil
}

// ---- Per-portnum decoders ----

func decodeTextMessage(payload []byte) (EventType, map[string]any, error) {
	return EventTextMessage, map[string]any{"text": string(payload)}, nil
}

func decodePosition(payload []byte) (EventType, map[string]any, error) {
	pos := &pb.Position{}
	if err := proto.Unmarshal(payload, pos); err != nil {
		return "", nil, err
	}
	details := map[string]any{}
	if pos.LatitudeI != nil {
		details["lat"] = float64(*pos.LatitudeI) * 1e-7
	}
	if pos.LongitudeI != nil {
		details["lon"] = float64(*pos.LongitudeI) * 1e-7
	}
	if pos.Altitude != nil {
		details["altitude_m"] = *pos.Altitude
	}
	details["sats_in_view"] = pos.SatsInView
	return EventPosition, details, nil
}

func decodeTelemetry(payload []byte) (EventType, map[string]any, error) {
	tel := &pb.Telemetry{}
	if err := proto.Unmarshal(payload, tel); err != nil {
		return "", nil, err
	}
	details := map[string]any{}
	switch v := tel.Variant.(type) {
	case *pb.Telemetry_DeviceMetrics:
		m := v.DeviceMetrics
		details["type"] = "device"
		if m.BatteryLevel != nil {
			details["battery_level_%"] = *m.BatteryLevel
		}
		if m.Voltage != nil {
			details["voltage_v"] = *m.Voltage
		}
		if m.ChannelUtilization != nil {
			details["channel_utilization_%"] = *m.ChannelUtilization
		}
		if m.AirUtilTx != nil {
			details["air_util_tx_%"] = *m.AirUtilTx
		}
		if m.UptimeSeconds != nil {
			details["uptime_seconds"] = *m.UptimeSeconds
		}
	case *pb.Telemetry_EnvironmentMetrics:
		m := v.EnvironmentMetrics
		details["type"] = "environment"
		if m.Temperature != nil {
			details["temperature_c"] = *m.Temperature
		}
		if m.RelativeHumidity != nil {
			details["relative_humidity_%"] = *m.RelativeHumidity
		}
		if m.BarometricPressure != nil {
			details["barometric_pressure_hpa"] = *m.BarometricPressure
		}
		if m.GasResistance != nil {
			details["gas_resistance"] = *m.GasResistance
		}
		if m.Voltage != nil {
			details["voltage_v"] = *m.Voltage
		}
		if m.Current != nil {
			details["current_ma"] = *m.Current
		}
		if m.Iaq != nil {
			details["iaq"] = *m.Iaq
		}
		if m.Distance != nil {
			details["distance_mm"] = *m.Distance
		}
		if m.Lux != nil {
			details["lux"] = *m.Lux
		}
		if m.WhiteLux != nil {
			details["white_lux"] = *m.WhiteLux
		}
		if m.IrLux != nil {
			details["ir_lux"] = *m.IrLux
		}
		if m.UvLux != nil {
			details["uv_lux"] = *m.UvLux
		}
		if m.WindDirection != nil {
			details["wind_direction_deg"] = *m.WindDirection
		}
		if m.WindSpeed != nil {
			details["wind_speed_ms"] = *m.WindSpeed
		}
		if m.WindGust != nil {
			details["wind_gust_ms"] = *m.WindGust
		}
		if m.WindLull != nil {
			details["wind_lull_ms"] = *m.WindLull
		}
		if m.Weight != nil {
			details["weight_kg"] = *m.Weight
		}
		if m.Radiation != nil {
			details["radiation_urh"] = *m.Radiation
		}
		if m.Rainfall_1H != nil {
			details["rainfall_1h_mm"] = *m.Rainfall_1H
		}
		if m.Rainfall_24H != nil {
			details["rainfall_24h_mm"] = *m.Rainfall_24H
		}
		if m.SoilMoisture != nil {
			details["soil_moisture_%"] = *m.SoilMoisture
		}
		if m.SoilTemperature != nil {
			details["soil_temperature_c"] = *m.SoilTemperature
		}
	case *pb.Telemetry_AirQualityMetrics:
		m := v.AirQualityMetrics
		details["type"] = "air_quality"
		if m.Pm10Standard != nil {
			details["pm10_standard"] = *m.Pm10Standard
		}
		if m.Pm25Standard != nil {
			details["pm25_standard"] = *m.Pm25Standard
		}
		if m.Pm100Standard != nil {
			details["pm100_standard"] = *m.Pm100Standard
		}
	case *pb.Telemetry_PowerMetrics:
		m := v.PowerMetrics
		details["type"] = "power"
		if m.Ch1Voltage != nil {
			details["ch1_voltage_v"] = *m.Ch1Voltage
		}
		if m.Ch1Current != nil {
			details["ch1_current_ma"] = *m.Ch1Current
		}
		if m.Ch2Voltage != nil {
			details["ch2_voltage_v"] = *m.Ch2Voltage
		}
		if m.Ch2Current != nil {
			details["ch2_current_ma"] = *m.Ch2Current
		}
		if m.Ch3Voltage != nil {
			details["ch3_voltage_v"] = *m.Ch3Voltage
		}
		if m.Ch3Current != nil {
			details["ch3_current_ma"] = *m.Ch3Current
		}
	case *pb.Telemetry_LocalStats:
		m := v.LocalStats
		details["type"] = "local_stats"
		details["uptime_seconds"] = m.UptimeSeconds
		details["channel_utilization_%"] = m.ChannelUtilization
		details["air_util_tx_%"] = m.AirUtilTx
		details["num_packets_tx"] = m.NumPacketsTx
		details["num_packets_rx"] = m.NumPacketsRx
		details["num_packets_rx_bad"] = m.NumPacketsRxBad
		details["num_online_nodes"] = m.NumOnlineNodes
		details["num_total_nodes"] = m.NumTotalNodes
		details["num_rx_dupe"] = m.NumRxDupe
		details["num_tx_relay"] = m.NumTxRelay
		details["num_tx_relay_canceled"] = m.NumTxRelayCanceled
		details["heap_total_bytes"] = m.HeapTotalBytes
		details["heap_free_bytes"] = m.HeapFreeBytes
		details["num_tx_dropped"] = m.NumTxDropped
		details["noise_floor_dbm"] = m.NoiseFloor
	case *pb.Telemetry_HealthMetrics:
		m := v.HealthMetrics
		details["type"] = "health"
		if m.HeartBpm != nil {
			details["heart_bpm"] = *m.HeartBpm
		}
		if m.SpO2 != nil {
			details["spo2_%"] = *m.SpO2
		}
		if m.Temperature != nil {
			details["temperature_c"] = *m.Temperature
		}
	}
	return EventTelemetry, details, nil
}

func decodeNodeInfoApp(payload []byte) (EventType, map[string]any, error) {
	user := &pb.User{}
	if err := proto.Unmarshal(payload, user); err != nil {
		return "", nil, err
	}
	return EventNodeInfo, map[string]any{
		"id":         user.Id,
		"long_name":  user.LongName,
		"short_name": user.ShortName,
		"hw_model":   user.HwModel.String(),
		"role":       user.Role.String(),
	}, nil
}

func decodeNeighborInfo(payload []byte) (EventType, map[string]any, error) {
	ni := &pb.NeighborInfo{}
	if err := proto.Unmarshal(payload, ni); err != nil {
		return "", nil, err
	}
	neighbors := make([]map[string]any, 0, len(ni.Neighbors))
	for _, n := range ni.Neighbors {
		neighbors = append(neighbors, map[string]any{
			"node_id": fmt.Sprintf("!%08x", n.NodeId),
			"snr":     n.Snr,
		})
	}
	return EventNeighborInfo, map[string]any{
		"node_id":           fmt.Sprintf("!%08x", ni.NodeId),
		"node_id_num":       ni.NodeId,
		"neighbors":         neighbors,
		"neighbor_count":    len(ni.Neighbors),
		"broadcast_secs":    ni.NodeBroadcastIntervalSecs,
	}, nil
}

func decodeRouting(payload []byte) (EventType, map[string]any, error) {
	routing := &pb.Routing{}
	if err := proto.Unmarshal(payload, routing); err != nil {
		return EventRouting, map[string]any{"size": len(payload)}, nil
	}
	details := map[string]any{}
	if errReason, ok := routing.Variant.(*pb.Routing_ErrorReason); ok {
		details["error_reason"] = errReason.ErrorReason.String()
	}
	return EventRouting, details, nil
}

func decodeTraceroute(payload []byte) (EventType, map[string]any, error) {
	rd := &pb.RouteDiscovery{}
	if err := proto.Unmarshal(payload, rd); err != nil {
		return "", nil, err
	}
	route := make([]string, len(rd.Route))
	for i, n := range rd.Route {
		route[i] = fmt.Sprintf("!%08x", n)
	}
	details := map[string]any{"route": route}
	if len(rd.SnrTowards) > 0 {
		details["snr_towards"] = rd.SnrTowards
	}
	if len(rd.RouteBack) > 0 {
		routeBack := make([]string, len(rd.RouteBack))
		for i, n := range rd.RouteBack {
			routeBack[i] = fmt.Sprintf("!%08x", n)
		}
		details["route_back"] = routeBack
	}
	if len(rd.SnrBack) > 0 {
		details["snr_back"] = rd.SnrBack
	}
	return EventTraceroute, details, nil
}

func decodeStoreForward(payload []byte) (EventType, map[string]any, error) {
	sf := &pb.StoreAndForward{}
	if err := proto.Unmarshal(payload, sf); err != nil {
		return EventStoreForward, map[string]any{
			"portnum": "STORE_FORWARD_APP",
			"error":   err.Error(),
			"size":    len(payload),
		}, nil
	}
	typ := storeForwardVariant(sf)
	return EventStoreForward, map[string]any{
		"rr":      sf.Rr.String(),
		"variant": typ,
	}, nil
}

func storeForwardVariant(sf *pb.StoreAndForward) string {
	switch sf.Variant.(type) {
	case *pb.StoreAndForward_Stats:
		return "stats"
	case *pb.StoreAndForward_History_:
		return "history"
	case *pb.StoreAndForward_Heartbeat_:
		return "heartbeat"
	case *pb.StoreAndForward_Text:
		return "text"
	}
	return "none"
}

func decodeWaypoint(payload []byte) (EventType, map[string]any, error) {
	w := &pb.Waypoint{}
	if err := proto.Unmarshal(payload, w); err != nil {
		return "", nil, err
	}
	details := map[string]any{
		"id":          w.Id,
		"name":        w.Name,
		"description": w.Description,
		"icon":        w.Icon,
		"expire":      w.Expire,
		"locked_to":   w.LockedTo,
	}
	if w.LatitudeI != nil {
		details["lat"] = float64(*w.LatitudeI) * 1e-7
	}
	if w.LongitudeI != nil {
		details["lon"] = float64(*w.LongitudeI) * 1e-7
	}
	return EventWaypoint, details, nil
}

func decodeDetectionSensor(payload []byte) (EventType, map[string]any, error) {
	return EventDetectionSensor, map[string]any{
		"text": string(payload),
		"size": len(payload),
	}, nil
}

func decodeAlert(payload []byte) (EventType, map[string]any, error) {
	return EventAlert, map[string]any{
		"text": string(payload),
		"size": len(payload),
	}, nil
}

func decodeKeyVerify(payload []byte) (EventType, map[string]any, error) {
	kv := &pb.KeyVerification{}
	if err := proto.Unmarshal(payload, kv); err != nil {
		return "", nil, err
	}
	phase := "init"
	if len(kv.Hash1) > 0 {
		phase = "final"
	} else if len(kv.Hash2) > 0 {
		phase = "response"
	}
	return EventKeyVerify, map[string]any{
		"nonce": fmt.Sprintf("%016x", kv.Nonce),
		"phase": phase,
	}, nil
}

func decodeNodeStatus(payload []byte) (EventType, map[string]any, error) {
	return EventNodeStatus, map[string]any{"size": len(payload)}, nil
}

func decodeRangeTest(payload []byte) (EventType, map[string]any, error) {
	return EventRangeTest, map[string]any{
		"text": string(payload),
		"size": len(payload),
	}, nil
}

func decodeMapReport(payload []byte) (EventType, map[string]any, error) {
	mr := &pb.MapReport{}
	if err := proto.Unmarshal(payload, mr); err != nil {
		return "", nil, err
	}
	details := map[string]any{
		"long_name":              mr.LongName,
		"short_name":             mr.ShortName,
		"role":                   mr.Role.String(),
		"hw_model":               mr.HwModel.String(),
		"firmware_version":       mr.FirmwareVersion,
		"region":                 mr.Region.String(),
		"modem_preset":           mr.ModemPreset.String(),
		"has_default_channel":    mr.HasDefaultChannel,
		"position_precision":     mr.PositionPrecision,
		"num_online_local_nodes": mr.NumOnlineLocalNodes,
	}
	if mr.LatitudeI != 0 || mr.LongitudeI != 0 {
		details["lat"] = float64(mr.LatitudeI) * 1e-7
		details["lon"] = float64(mr.LongitudeI) * 1e-7
	}
	if mr.Altitude != 0 {
		details["altitude"] = mr.Altitude
	}
	return EventMapReport, details, nil
}

func decodeNodeInfoDetails(ni *pb.NodeInfo) map[string]any {
	details := map[string]any{
		"num": fmt.Sprintf("!%08x", ni.Num),
		"snr": ni.Snr,
	}
	if ni.User != nil {
		details["id"] = ni.User.Id
		details["long_name"] = ni.User.LongName
		details["short_name"] = ni.User.ShortName
		details["hw_model"] = ni.User.HwModel.String()
		details["role"] = ni.User.Role.String()
	}
	if ni.Position != nil {
		if ni.Position.LatitudeI != nil {
			details["lat"] = float64(*ni.Position.LatitudeI) * 1e-7
		}
		if ni.Position.LongitudeI != nil {
			details["lon"] = float64(*ni.Position.LongitudeI) * 1e-7
		}
		if ni.Position.Altitude != nil {
			details["altitude"] = *ni.Position.Altitude
		}
	}
	return details
}

func nodeStr(n uint32) string {
	if n == 0xFFFFFFFF {
		return "^all"
	}
	return fmt.Sprintf("!%08x", n)
}
