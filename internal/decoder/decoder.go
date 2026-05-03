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
	EventTextMessage EventType = "TEXT_MESSAGE"
	EventPosition    EventType = "POSITION"
	EventTelemetry   EventType = "TELEMETRY"
	EventNodeInfo    EventType = "NODE_INFO"
	EventMyInfo      EventType = "MY_INFO"
	EventRouting     EventType = "ROUTING"
	EventTraceroute  EventType = "TRACEROUTE"
	EventLogRecord      EventType = "LOG_RECORD"
	EventNeighborInfo   EventType = "NEIGHBOR_INFO"
	EventStoreForward   EventType = "STORE_FORWARD" // Store & Forward router/client packets
	EventStoreForwardPP EventType = "STORE_FORWARD_PP" // Store-and-Forward++ (variant)
	EventWaypoint       EventType = "WAYPOINT"      // Shared waypoints (lat/lon + name)
	EventDetectionSensor EventType = "DETECT_SENSOR" // PIR/door/proximity sensor events
	EventAlert          EventType = "ALERT"         // Mesh-wide alert
	EventKeyVerify      EventType = "KEY_VERIFY"    // PKC verification handshake
	EventNodeStatus     EventType = "NODE_STATUS"   // Periodic node status
	EventRangeTest      EventType = "RANGE_TEST"    // Range-test sequence (debug coverage)
	EventMapReport      EventType = "MAP_REPORT"    // Periodic node info report (used by map.meshtastic.org)
	EventEncrypted      EventType = "ENCRYPTED"
	EventConfigComplete EventType = "CONFIG_COMPLETE"
	EventMetadata       EventType = "METADATA"      // DeviceMetadata (firmware version, caps)
	EventConfigLora     EventType = "CONFIG_LORA"   // LoRa radio config
	EventModuleNeighbor EventType = "MOD_NEIGHBOR"  // ModuleConfig.NeighborInfo
	EventRaw            EventType = "RAW"
)

// Event is a decoded Meshtastic packet ready for logging or further processing.
type Event struct {
	Time     time.Time
	Type     EventType
	FromNode uint32
	ToNode   uint32
	// RSSI / SNR from the radio (0 if not available)
	RSSI int32
	SNR  float32
	// HopLimit remaining hops
	HopLimit uint32
	// HopStart is the original hop limit when the packet was first sent.
	// The difference (HopStart - HopLimit) gives how many hops it traveled.
	HopStart uint32
	// PacketID is the unique packet ID (per sender). Used for deduplication.
	PacketID uint32
	// RelayNode is the last byte of the relaying node's number (0 = direct).
	RelayNode uint32
	// ViaMqtt indicates the packet passed through MQTT at some point.
	ViaMqtt bool
	Details map[string]any
	// RawBytes holds the original protobuf bytes for debugging / future use.
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
			return event, nil // skip other config types (Device, Network, …)
		}

	case *pb.FromRadio_ModuleConfig:
		// ModuleConfig comes in as one of several variants. We currently only
		// surface the NeighborInfo flavor — when this module is DISABLED on
		// the connected node, the firmware silently drops every NeighborInfo
		// packet it receives over the air, so the dashboard would never see
		// a single one regardless of how chatty the mesh is. Surfacing the
		// flag on the My Node page makes the cause obvious to the user.
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
			return event, nil // skip MQTT, Serial, ExternalNotification, …
		}

	case *pb.FromRadio_NodeInfo:
		event.Type = EventNodeInfo
		ni := p.NodeInfo
		event.FromNode = ni.Num
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
		event.Details = details

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
		// All other FromRadio variants are internal radio config messages
		// (Channel, Config, ModuleConfig, Metadata, QueueStatus, etc.)
		// — not mesh network traffic. Discard silently.
		return nil, nil
	}

	return event, nil
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

	switch decoded.Portnum {

	case pb.PortNum_TEXT_MESSAGE_APP:
		event.Type = EventTextMessage
		event.Details = map[string]any{
			"text": string(decoded.Payload),
		}

	case pb.PortNum_POSITION_APP:
		pos := &pb.Position{}
		if err := proto.Unmarshal(decoded.Payload, pos); err != nil {
			event.Type = EventRaw
			event.Details = map[string]any{"portnum": "POSITION_APP", "error": err.Error()}
			return event, nil
		}
		event.Type = EventPosition
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
		event.Details = details

	case pb.PortNum_TELEMETRY_APP:
		tel := &pb.Telemetry{}
		if err := proto.Unmarshal(decoded.Payload, tel); err != nil {
			event.Type = EventRaw
			event.Details = map[string]any{"portnum": "TELEMETRY_APP", "error": err.Error()}
			return event, nil
		}
		event.Type = EventTelemetry
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
		}
		event.Details = details

	case pb.PortNum_NODEINFO_APP:
		user := &pb.User{}
		if err := proto.Unmarshal(decoded.Payload, user); err != nil {
			event.Type = EventRaw
			event.Details = map[string]any{"portnum": "NODEINFO_APP", "error": err.Error()}
			return event, nil
		}
		event.Type = EventNodeInfo
		event.Details = map[string]any{
			"id":         user.Id,
			"long_name":  user.LongName,
			"short_name": user.ShortName,
			"hw_model":   user.HwModel.String(),
			"role":       user.Role.String(),
		}

	case pb.PortNum_NEIGHBORINFO_APP:
		ni := &pb.NeighborInfo{}
		if err := proto.Unmarshal(decoded.Payload, ni); err != nil {
			event.Type = EventRaw
			event.Details = map[string]any{"portnum": "NEIGHBORINFO_APP", "error": err.Error()}
			return event, nil
		}
		event.Type = EventNeighborInfo
		neighbors := make([]map[string]any, 0, len(ni.Neighbors))
		for _, n := range ni.Neighbors {
			neighbors = append(neighbors, map[string]any{
				"node_id": fmt.Sprintf("!%08x", n.NodeId),
				"snr":     n.Snr,
			})
		}
		event.Details = map[string]any{
			"node_id":           fmt.Sprintf("!%08x", ni.NodeId),
			"node_id_num":       ni.NodeId,
			"neighbors":         neighbors,
			"neighbor_count":    len(ni.Neighbors),
			"broadcast_secs":    ni.NodeBroadcastIntervalSecs,
		}

	case pb.PortNum_ROUTING_APP:
		routing := &pb.Routing{}
		if err := proto.Unmarshal(decoded.Payload, routing); err != nil {
			event.Type = EventRouting
			event.Details = map[string]any{"size": len(decoded.Payload)}
			return event, nil
		}
		event.Type = EventRouting
		details := map[string]any{}
		if errReason, ok := routing.Variant.(*pb.Routing_ErrorReason); ok {
			details["error_reason"] = errReason.ErrorReason.String()
		}
		event.Details = details

	case pb.PortNum_TRACEROUTE_APP:
		rd := &pb.RouteDiscovery{}
		if err := proto.Unmarshal(decoded.Payload, rd); err != nil {
			event.Type = EventRaw
			event.Details = map[string]any{"portnum": "TRACEROUTE_APP", "error": err.Error()}
			return event, nil
		}
		event.Type = EventTraceroute
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
		event.Details = details

	case pb.PortNum_STORE_FORWARD_APP:
		// Store-and-Forward router/client packets: Heartbeats, RouterRecord
		// announcements, history requests, replayed text, stats, etc. We
		// decode the wrapper to surface the sub-type so a router doing S&F
		// shows up distinctly in per-node breakdowns instead of as generic RAW.
		sf := &pb.StoreAndForward{}
		if err := proto.Unmarshal(decoded.Payload, sf); err != nil {
			event.Type = EventStoreForward
			event.Details = map[string]any{
				"portnum": "STORE_FORWARD_APP",
				"error":   err.Error(),
				"size":    len(decoded.Payload),
			}
			return event, nil
		}
		event.Type = EventStoreForward
		variant := "none"
		switch sf.Variant.(type) {
		case *pb.StoreAndForward_Stats:
			variant = "stats"
		case *pb.StoreAndForward_History_:
			variant = "history"
		case *pb.StoreAndForward_Heartbeat_:
			variant = "heartbeat"
		case *pb.StoreAndForward_Text:
			variant = "text"
		}
		event.Details = map[string]any{
			"rr":      sf.Rr.String(), // ROUTER_*, CLIENT_*, ROUTER_HEARTBEAT, …
			"variant": variant,
		}

	case pb.PortNum_STORE_FORWARD_PLUSPLUS_APP:
		// S&F++ uses the same StoreAndForward proto as the original.
		sf := &pb.StoreAndForward{}
		if err := proto.Unmarshal(decoded.Payload, sf); err != nil {
			event.Type = EventStoreForwardPP
			event.Details = map[string]any{
				"portnum": "STORE_FORWARD_PLUSPLUS_APP",
				"error":   err.Error(),
				"size":    len(decoded.Payload),
			}
			return event, nil
		}
		event.Type = EventStoreForwardPP
		variant := "none"
		switch sf.Variant.(type) {
		case *pb.StoreAndForward_Stats:
			variant = "stats"
		case *pb.StoreAndForward_History_:
			variant = "history"
		case *pb.StoreAndForward_Heartbeat_:
			variant = "heartbeat"
		case *pb.StoreAndForward_Text:
			variant = "text"
		}
		event.Details = map[string]any{
			"rr":      sf.Rr.String(),
			"variant": variant,
		}

	case pb.PortNum_WAYPOINT_APP:
		// Shared waypoint: lat/lon + name + description + emoji icon.
		// Stored on every node so anyone can see/edit (unless locked_to).
		w := &pb.Waypoint{}
		if err := proto.Unmarshal(decoded.Payload, w); err != nil {
			event.Type = EventWaypoint
			event.Details = map[string]any{"portnum": "WAYPOINT_APP", "error": err.Error()}
			return event, nil
		}
		event.Type = EventWaypoint
		details := map[string]any{
			"id":          w.Id,
			"name":        w.Name,
			"description": w.Description,
			"icon":        w.Icon,    // unicode codepoint of the emoji
			"expire":      w.Expire,
			"locked_to":   w.LockedTo,
		}
		if w.LatitudeI != nil {
			details["lat"] = float64(*w.LatitudeI) * 1e-7
		}
		if w.LongitudeI != nil {
			details["lon"] = float64(*w.LongitudeI) * 1e-7
		}
		event.Details = details

	case pb.PortNum_DETECTION_SENSOR_APP:
		// Detection sensor module sends a plain text status string when
		// the configured GPIO triggers (PIR motion, door, etc).
		event.Type = EventDetectionSensor
		event.Details = map[string]any{
			"text": string(decoded.Payload),
			"size": len(decoded.Payload),
		}

	case pb.PortNum_ALERT_APP:
		// Mesh-wide alert (text payload, like a high-priority TextMessage).
		event.Type = EventAlert
		event.Details = map[string]any{
			"text": string(decoded.Payload),
			"size": len(decoded.Payload),
		}

	case pb.PortNum_KEY_VERIFICATION_APP:
		// PKC handshake: nonce + intermediate hash2 + final hash1.
		// We surface the phase (init / response / final) by which hash is
		// present so an operator can debug a verification flow.
		kv := &pb.KeyVerification{}
		if err := proto.Unmarshal(decoded.Payload, kv); err != nil {
			event.Type = EventKeyVerify
			event.Details = map[string]any{"portnum": "KEY_VERIFICATION_APP", "error": err.Error()}
			return event, nil
		}
		event.Type = EventKeyVerify
		phase := "init"
		if len(kv.Hash1) > 0 {
			phase = "final"
		} else if len(kv.Hash2) > 0 {
			phase = "response"
		}
		event.Details = map[string]any{
			"nonce": fmt.Sprintf("%016x", kv.Nonce),
			"phase": phase,
		}

	case pb.PortNum_NODE_STATUS_APP:
		// Periodic node status. The payload format isn't a documented
		// proto message — we capture the size for now so it shows up as
		// a distinct type in breakdowns.
		event.Type = EventNodeStatus
		event.Details = map[string]any{
			"size": len(decoded.Payload),
		}

	case pb.PortNum_RANGE_TEST_APP:
		// Range-test module sends sequential text frames so the receiver
		// can plot where coverage drops. Payload is the sequence/text
		// itself.
		event.Type = EventRangeTest
		event.Details = map[string]any{
			"text": string(decoded.Payload),
			"size": len(decoded.Payload),
		}

	case pb.PortNum_MAP_REPORT_APP:
		// Periodic self-report sent to map.meshtastic.org via MQTT (or
		// just to the mesh when MQTT is disabled). Useful even off-MQTT
		// because it carries firmware/region/preset for nodes we never
		// got a NodeInfo from.
		mr := &pb.MapReport{}
		if err := proto.Unmarshal(decoded.Payload, mr); err != nil {
			event.Type = EventMapReport
			event.Details = map[string]any{"portnum": "MAP_REPORT_APP", "error": err.Error()}
			return event, nil
		}
		event.Type = EventMapReport
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
		// lat/lon/altitude — guard precision-zero (means "not shared")
		if mr.LatitudeI != 0 || mr.LongitudeI != 0 {
			details["lat"] = float64(mr.LatitudeI) * 1e-7
			details["lon"] = float64(mr.LongitudeI) * 1e-7
		}
		if mr.Altitude != 0 {
			details["altitude"] = mr.Altitude
		}
		event.Details = details

	default:
		event.Type = EventRaw
		event.Details = map[string]any{
			"portnum": decoded.Portnum.String(),
			"size":    len(decoded.Payload),
		}
	}

	return event, nil
}

func nodeStr(n uint32) string {
	if n == 0xFFFFFFFF {
		return "^all"
	}
	return fmt.Sprintf("!%08x", n)
}
