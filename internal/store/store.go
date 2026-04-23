// Package store provides a thread-safe in-memory event store for Meshtastic data.
package store

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"mesh-reader/internal/decoder"
)

// LocalNodeInfo holds everything we know about the node we are directly
// connected to (the "gateway" node). Fields are populated incrementally from
// the boot-time message sequence: MY_INFO → NODE_INFO (own) → CONFIG_LORA →
// METADATA → CONFIG_COMPLETE. Zero/empty values mean "not yet received".
type LocalNodeInfo struct {
	// Identity
	NodeNum   uint32 `json:"node_num"`
	NodeID    string `json:"node_id"`    // "!xxxxxxxx"
	LongName  string `json:"long_name"`
	ShortName string `json:"short_name"`
	HWModel   string `json:"hw_model"`
	Role      string `json:"role"`

	// Firmware
	FirmwareVersion    string `json:"firmware_version"`
	PioEnv             string `json:"pio_env"`
	RebootCount        uint32 `json:"reboot_count"`
	NodedbCount        uint32 `json:"nodedb_count"`
	DeviceStateVersion uint32 `json:"device_state_version"`

	// Hardware capabilities (from DeviceMetadata)
	HasWifi      bool `json:"has_wifi"`
	HasBluetooth bool `json:"has_bluetooth"`
	HasPKC       bool `json:"has_pkc"`
	CanShutdown  bool `json:"can_shutdown"`

	// LoRa radio config (from Config_Lora)
	Region       string `json:"region"`
	ModemPreset  string `json:"modem_preset"`
	UsePreset    bool   `json:"use_preset"`
	HopLimit     uint32 `json:"hop_limit"`
	TxPower      int32  `json:"tx_power"`
	TxEnabled    bool   `json:"tx_enabled"`
	Bandwidth    uint32 `json:"bandwidth"`
	SpreadFactor uint32 `json:"spread_factor"`
	CodingRate   uint32 `json:"coding_rate"`
	ChannelNum   uint32 `json:"channel_num"`

	// Runtime
	SeenAt        int64 `json:"seen_at"`        // unix ts of last update
	UptimeSeconds int64 `json:"uptime_seconds"` // filled by API at read time
}

// NodeState tracks the latest known state for a single mesh node.
type NodeState struct {
	NodeNum   uint32  `json:"node_num"`
	ID        string  `json:"id"`
	LongName  string  `json:"long_name"`
	ShortName string  `json:"short_name"`
	HWModel   string  `json:"hw_model"`
	// Role is the Meshtastic device role (CLIENT, ROUTER, REPEATER, TRACKER,
	// SENSOR, TAK, CLIENT_HIDDEN, LOST_AND_FOUND, TAK_TRACKER, ROUTER_LATE…).
	// Stored as the protobuf enum string. Empty until the first NODE_INFO
	// packet from that node carries the User.Role field.
	Role      string  `json:"role,omitempty"`
	LastHeard int64   `json:"last_heard"` // unix timestamp
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	HasPos    bool    `json:"has_pos"`
	Altitude  int32   `json:"altitude"`
	// Device telemetry
	BatteryLevel       uint32  `json:"battery_level"`
	Voltage            float32 `json:"voltage"`
	ChannelUtilization float32 `json:"channel_utilization"`
	AirUtilTx          float32 `json:"air_util_tx"`
	// Environment telemetry
	Temperature        float32 `json:"temperature"`
	Humidity           float32 `json:"humidity"`
	BarometricPressure float32 `json:"barometric_pressure"`
	// Signal
	RSSI     int32   `json:"rssi"`
	SNR      float32 `json:"snr"`
	HopLimit uint32  `json:"hop_limit"`
	// Per-node packet counters by event type
	PacketsByType map[string]int `json:"packets_by_type"`
	// Per-node HopStart distribution (TTL set at sender). Key is the
	// numeric string "0".."7"; a node can have multiple entries if it was
	// reconfigured or if different packet types use different TTLs.
	HopStartHist map[string]int `json:"hop_start_hist,omitempty"`
	// HopStartMode is the most frequently observed HopStart for this node
	// (representative of its current configuration).
	HopStartMode uint32 `json:"hop_start_mode,omitempty"`
	// HopStartMax is the largest HopStart ever observed from this node
	// (highlights nodes that occasionally emit with aggressive TTL).
	HopStartMax uint32 `json:"hop_start_max,omitempty"`
}

// TracerouteRecord is one traceroute observation.
type TracerouteRecord struct {
	Time       int64    `json:"time"` // unix timestamp
	From       uint32   `json:"from"`
	To         uint32   `json:"to"`
	Route      []string `json:"route"`
	RouteBack  []string `json:"route_back,omitempty"`
	SnrTowards []int32  `json:"snr_towards,omitempty"` // quarter-dB per hop (forward)
	SnrBack    []int32  `json:"snr_back,omitempty"`    // quarter-dB per hop (return)
}

// LinkRecord tracks observed signal quality between a pair of nodes.
type LinkRecord struct {
	NodeA    uint32  `json:"node_a"`
	NodeB    uint32  `json:"node_b"`
	RSSI     int32   `json:"rssi"`
	SNR      float32 `json:"snr"`
	Count    int     `json:"count"`
	LastSeen int64   `json:"last_seen"`
	// Neighbor indicates whether this link comes from NEIGHBORINFO_APP
	// (direct neighbor table data — more accurate than inferred from traffic).
	Neighbor bool `json:"neighbor"`
}

// HopStats tracks hop_limit and hop_start statistics for a single event type.
type HopStats struct {
	Count        int     `json:"count"`
	AvgHopLimit  float64 `json:"avg_hop_limit"`
	MinHopLimit  uint32  `json:"min_hop_limit"`
	MaxHopLimit  uint32  `json:"max_hop_limit"`
	AvgHopStart  float64 `json:"avg_hop_start"`
	MinHopStart  uint32  `json:"min_hop_start"`
	MaxHopStart  uint32  `json:"max_hop_start"`
	AvgHopsTraveled float64 `json:"avg_hops_traveled"`
}

// hopAccum accumulates hop data for computing stats.
type hopAccum struct {
	count       int
	sumLimit    uint64
	minLimit    uint32
	maxLimit    uint32
	sumStart    uint64
	minStart    uint32
	maxStart    uint32
	sumTraveled uint64
}

// RelayTypeCount is "this relay forwarded N packets of this type".
// Used inside RelayStat.TopTypes so the dashboard can show which kinds of
// packets each relay repeats the most, not just the total.
type RelayTypeCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// RelayStat holds the relay node identifier and its forwarded packet count,
// plus the top event-type breakdown (what kinds of packets that relay moves
// most of).
type RelayStat struct {
	NodeID     string           `json:"node_id"`                // resolved node ID or "..xx"
	Name       string           `json:"name,omitempty"`          // resolved short name
	Candidates []string         `json:"candidates,omitempty"`    // when ambiguous
	Count      int              `json:"count"`
	TopTypes   []RelayTypeCount `json:"top_types,omitempty"`     // descending by Count
}

// relayAgg is the mutable accumulator behind RelayStat — a total count plus
// a per-type breakdown so we can compute "what this relay mostly forwards".
type relayAgg struct {
	total  int
	byType map[string]int
}

// Stats holds aggregate statistics.
type Stats struct {
	TotalEvents    int                 `json:"total_events"`
	TotalNodes     int                 `json:"total_nodes"`
	ActiveNodes    int                 `json:"active_nodes"`
	MessagesCount  int                 `json:"messages_count"`
	UptimeSeconds  int64               `json:"uptime_seconds"`
	PacketsByType  map[string]int      `json:"packets_by_type"`
	HopStatsByType map[string]HopStats `json:"hop_stats_by_type"`
	RelayStats     []RelayStat         `json:"relay_stats,omitempty"`
}

// Store is the central in-memory data structure.
type Store struct {
	mu        sync.RWMutex
	events    []*decoder.Event
	head      int
	count     int
	maxEvents int

	nodes       map[uint32]*NodeState
	traceroutes []TracerouteRecord
	links       map[uint64]*LinkRecord // key = min(a,b)<<32 | max(a,b)
	myNodeNum   uint32                 // our local node (from MyInfo)
	localNode   LocalNodeInfo          // info about the directly connected node
	msgCount    int
	startTime   time.Time
	lastEventAt time.Time // updated on every Add() call

	packetsByType map[string]int

	// Deduplication: tracks seen (from<<32 | packetID) keys
	seenPackets map[uint64]struct{}

	// Hop statistics per event type
	hopStats map[string]*hopAccum

	// Relay packet counts: relayNodeLastByte -> aggregator (total + per-type).
	// Per-type lets the dashboard show what each relay mostly forwards.
	relayCounts map[uint32]*relayAgg

	// Radio-health metrics from firmware debug log (nil until first datum).
	radio *radioHealthData

	// Node availability tracking (nil until first packet).
	avail *availData

	// Anomaly detection state + ring buffer (nil until first packet).
	anom *anomalyData

	// DX (long-distance reception) leaderboard: per-node best record.
	dx map[uint32]DXRecord

	// WebSocket subscribers
	subMu     sync.RWMutex
	subs      map[uint64]chan *decoder.Event
	nextSubID uint64
}

// New creates a Store that keeps at most maxEvents in its ring buffer.
func New(maxEvents int) *Store {
	return &Store{
		events:        make([]*decoder.Event, maxEvents),
		maxEvents:     maxEvents,
		nodes:         make(map[uint32]*NodeState),
		links:         make(map[uint64]*LinkRecord),
		packetsByType: make(map[string]int),
		seenPackets:   make(map[uint64]struct{}),
		hopStats:      make(map[string]*hopAccum),
		relayCounts:   make(map[uint32]*relayAgg),
		subs:          make(map[uint64]chan *decoder.Event),
		startTime:     time.Now(),
	}
}

// AddSilent updates node state from an event without incrementing counters,
// inserting into the ring buffer, or notifying WebSocket subscribers.
// Used for config-phase events (initial NodeInfo dump) that shouldn't appear
// as new traffic.
// availTransitionBuf is a temporary buffer returned to the caller so it can
// persist transitions outside the lock. Avoids holding s.mu while doing DB I/O.
func (s *Store) AddSilent(event *decoder.Event) *AvailTransition {
	s.mu.Lock()
	tr := s.MarkNodeHeard(event.FromNode, event.Time)
	s.updateNode(event)
	s.mu.Unlock()
	return tr
}

// Add ingests a new event into the store and notifies subscribers.
// Returns an AvailTransition if this packet caused a node to go "online".
func (s *Store) Add(event *decoder.Event) *AvailTransition {
	s.mu.Lock()
	// Ring buffer append
	s.events[s.head] = event
	s.head = (s.head + 1) % s.maxEvents
	s.count++
	s.lastEventAt = time.Now()

	s.packetsByType[string(event.Type)]++

	// Availability tracking
	tr := s.MarkNodeHeard(event.FromNode, event.Time)

	// Deduplication tracking
	s.trackDedup(event)

	// Hop statistics
	s.trackHops(event)

	// Relay tracking
	s.trackRelay(event)

	// Update node state and link tracking
	s.updateNode(event)
	s.trackLink(event)
	s.countNodePacket(event)
	s.countNodeHopStart(event)

	// Anomaly detection (GPS teleport, spammer, SNR jump) and DX leaderboard.
	// Both are no-ops until enough state is built up (positions, prior samples).
	s.detectAnomalies(event)
	s.trackDX(event)
	s.mu.Unlock()

	// Notify subscribers (non-blocking)
	s.subMu.RLock()
	for _, ch := range s.subs {
		select {
		case ch <- event:
		default: // drop for slow consumers
		}
	}
	s.subMu.RUnlock()
	return tr
}

func (s *Store) updateNode(event *decoder.Event) {
	d := event.Details

	// Global firmware-level events (no FromNode, no MeshPacket) — these are
	// sent by the directly-connected device itself in response to our
	// WantConfig handshake. They carry information about the local node
	// only, so we handle them here before the FromNode==0 early return.
	switch event.Type {
	case decoder.EventMetadata:
		s.localNode.SeenAt = event.Time.Unix()
		if v, ok := d["firmware_version"].(string); ok && v != "" {
			s.localNode.FirmwareVersion = v
		}
		if v, ok := d["hw_model"].(string); ok && v != "" {
			s.localNode.HWModel = v
		}
		if v, ok := d["role"].(string); ok && v != "" {
			s.localNode.Role = v
		}
		if v, ok := d["has_wifi"].(bool); ok {
			s.localNode.HasWifi = v
		}
		if v, ok := d["has_bluetooth"].(bool); ok {
			s.localNode.HasBluetooth = v
		}
		if v, ok := d["has_pkc"].(bool); ok {
			s.localNode.HasPKC = v
		}
		if v, ok := d["can_shutdown"].(bool); ok {
			s.localNode.CanShutdown = v
		}
		if v, ok := d["device_state_version"].(uint32); ok {
			s.localNode.DeviceStateVersion = v
		}
		return
	case decoder.EventConfigLora:
		s.localNode.SeenAt = event.Time.Unix()
		if v, ok := d["region"].(string); ok && v != "" {
			s.localNode.Region = v
		}
		if v, ok := d["modem_preset"].(string); ok && v != "" {
			s.localNode.ModemPreset = v
		}
		if v, ok := d["use_preset"].(bool); ok {
			s.localNode.UsePreset = v
		}
		if v, ok := d["hop_limit"].(uint32); ok {
			s.localNode.HopLimit = v
		}
		if v, ok := d["tx_power"].(int32); ok {
			s.localNode.TxPower = v
		}
		if v, ok := d["tx_enabled"].(bool); ok {
			s.localNode.TxEnabled = v
		}
		if v, ok := d["bandwidth"].(uint32); ok {
			s.localNode.Bandwidth = v
		}
		if v, ok := d["spread_factor"].(uint32); ok {
			s.localNode.SpreadFactor = v
		}
		if v, ok := d["coding_rate"].(uint32); ok {
			s.localNode.CodingRate = v
		}
		if v, ok := d["channel_num"].(uint32); ok {
			s.localNode.ChannelNum = v
		}
		return
	}

	if event.FromNode == 0 {
		return
	}
	node, ok := s.nodes[event.FromNode]
	if !ok {
		node = &NodeState{
			NodeNum:       event.FromNode,
			PacketsByType: make(map[string]int),
		}
		s.nodes[event.FromNode] = node
	}
	node.LastHeard = event.Time.Unix()
	if event.RSSI != 0 {
		node.RSSI = event.RSSI
	}
	if event.SNR != 0 {
		node.SNR = event.SNR
	}
	if event.HopLimit != 0 {
		node.HopLimit = event.HopLimit
	}

	switch event.Type {
	case decoder.EventNodeInfo:
		if v, ok := d["id"].(string); ok {
			node.ID = v
		}
		if v, ok := d["long_name"].(string); ok {
			node.LongName = v
		}
		if v, ok := d["short_name"].(string); ok {
			node.ShortName = v
		}
		if v, ok := d["hw_model"].(string); ok {
			node.HWModel = v
		}
		// Role is an enum string ("CLIENT", "ROUTER", …). We keep the raw
		// protobuf string so the UI can map it to badges without guessing.
		if v, ok := d["role"].(string); ok && v != "" {
			node.Role = v
		}
		// When the NodeInfo belongs to the local node, mirror identity fields
		// into LocalNodeInfo (long/short name are not in MyNodeInfo).
		if event.FromNode != 0 && event.FromNode == s.myNodeNum {
			if v, ok := d["long_name"].(string); ok && v != "" {
				s.localNode.LongName = v
			}
			if v, ok := d["short_name"].(string); ok && v != "" {
				s.localNode.ShortName = v
			}
			if v, ok := d["hw_model"].(string); ok && s.localNode.HWModel == "" {
				s.localNode.HWModel = v // DeviceMetadata is authoritative if present
			}
			if v, ok := d["role"].(string); ok && s.localNode.Role == "" {
				s.localNode.Role = v
			}
		}
		if v, ok := d["lat"].(float64); ok {
			node.Lat = v
			node.HasPos = true
		}
		if v, ok := d["lon"].(float64); ok {
			node.Lon = v
		}
		if v, ok := d["altitude"].(int32); ok {
			node.Altitude = v
		}

	case decoder.EventPosition:
		if v, ok := d["lat"].(float64); ok {
			node.Lat = v
			node.HasPos = true
		}
		if v, ok := d["lon"].(float64); ok {
			node.Lon = v
		}
		if v, ok := d["altitude_m"].(int32); ok {
			node.Altitude = v
		}

	case decoder.EventTelemetry:
		telType, _ := d["type"].(string)
		switch telType {
		case "device":
			if v, ok := d["battery_level_%"].(uint32); ok {
				node.BatteryLevel = v
			}
			if v, ok := d["voltage_v"].(float32); ok {
				node.Voltage = v
			}
			if v, ok := d["channel_utilization_%"].(float32); ok {
				node.ChannelUtilization = v
			}
			if v, ok := d["air_util_tx_%"].(float32); ok {
				node.AirUtilTx = v
			}
		case "environment":
			if v, ok := d["temperature_c"].(float32); ok {
				node.Temperature = v
			}
			if v, ok := d["relative_humidity_%"].(float32); ok {
				node.Humidity = v
			}
			if v, ok := d["barometric_pressure_hpa"].(float32); ok {
				node.BarometricPressure = v
			}
		}

	case decoder.EventTextMessage:
		s.msgCount++

	case decoder.EventTraceroute:
		rec := TracerouteRecord{
			Time: event.Time.Unix(),
			From: event.FromNode,
			To:   event.ToNode,
		}
		if v, ok := d["route"].([]string); ok {
			rec.Route = v
		}
		if v, ok := d["route_back"].([]string); ok {
			rec.RouteBack = v
		}
		if v, ok := d["snr_towards"].([]int32); ok {
			rec.SnrTowards = v
		}
		if v, ok := d["snr_back"].([]int32); ok {
			rec.SnrBack = v
		}
		s.traceroutes = append(s.traceroutes, rec)

	case decoder.EventNeighborInfo:
		s.processNeighborInfo(event)

	case decoder.EventMyInfo:
		s.myNodeNum = event.FromNode
		if v, ok := d["my_node_num"].(string); ok {
			node.ID = v
		}
		// Populate LocalNodeInfo fields available in MyInfo.
		s.localNode.NodeNum = event.FromNode
		s.localNode.NodeID = fmt.Sprintf("!%08x", event.FromNode)
		s.localNode.SeenAt = event.Time.Unix()
		if v, ok := d["reboot_count"].(uint32); ok {
			s.localNode.RebootCount = v
		}
		if v, ok := d["pio_env"].(string); ok && v != "" {
			s.localNode.PioEnv = v
		}
		if v, ok := d["nodedb_count"].(uint32); ok {
			s.localNode.NodedbCount = v
		}
	}
}

// trackLink creates or updates an observed link between two nodes.
// For broadcast events the "receiver" is our local node (from MyInfo).
func (s *Store) trackLink(event *decoder.Event) {
	from := event.FromNode
	if from == 0 || (event.RSSI == 0 && event.SNR == 0) {
		return
	}

	to := event.ToNode
	// For broadcasts or unknown destinations, attribute to our local node.
	if to == 0 || to == 0xFFFFFFFF {
		if s.myNodeNum == 0 || s.myNodeNum == from {
			return
		}
		to = s.myNodeNum
	}
	if from == to {
		return
	}

	a, b := from, to
	if a > b {
		a, b = b, a
	}
	key := uint64(a)<<32 | uint64(b)

	link, ok := s.links[key]
	if !ok {
		link = &LinkRecord{NodeA: a, NodeB: b}
		s.links[key] = link
	}
	link.RSSI = event.RSSI
	link.SNR = event.SNR
	link.Count++
	link.LastSeen = event.Time.Unix()
}

// countNodePacket increments the per-node packet-by-type counter.
// Must be called with s.mu held.
func (s *Store) countNodePacket(event *decoder.Event) {
	if event.FromNode == 0 {
		return
	}
	node, ok := s.nodes[event.FromNode]
	if !ok {
		return
	}
	if node.PacketsByType == nil {
		node.PacketsByType = make(map[string]int)
	}
	node.PacketsByType[string(event.Type)]++
}

// countNodeHopStart records the HopStart (TTL set at sender) seen in a packet
// into the originating node's distribution, and updates Mode/Max derived fields.
// HopStart is a 3-bit field in the Meshtastic PacketHeader so values >7 are
// treated as invalid and skipped. Called from Add and from backfill/reload.
// Must be called with s.mu held.
func (s *Store) countNodeHopStart(event *decoder.Event) {
	if event.FromNode == 0 || event.HopStart > 7 {
		return
	}
	// Skip when we have no HopStart data at all (some packet paths deliver
	// only HopLimit with HopStart=0 genuinely — those still count as "0").
	// We include HopStart=0 here because it is a legitimate observation.
	node, ok := s.nodes[event.FromNode]
	if !ok {
		return
	}
	if node.HopStartHist == nil {
		node.HopStartHist = make(map[string]int, 4)
	}
	key := strconv.FormatUint(uint64(event.HopStart), 10)
	node.HopStartHist[key]++
	if event.HopStart > node.HopStartMax {
		node.HopStartMax = event.HopStart
	}
	// Recompute mode (hist has at most 8 keys — O(8) is trivial).
	var bestKey string
	var bestCnt int
	for k, c := range node.HopStartHist {
		if c > bestCnt || (c == bestCnt && k > bestKey) {
			bestCnt = c
			bestKey = k
		}
	}
	if v, err := strconv.ParseUint(bestKey, 10, 32); err == nil {
		node.HopStartMode = uint32(v)
	}
}

// processNeighborInfo extracts direct neighbor links from a NEIGHBOR_INFO event.
// This data is more authoritative than traffic-inferred links because it comes
// directly from each node's neighbor table with measured SNR values.
func (s *Store) processNeighborInfo(event *decoder.Event) {
	d := event.Details
	reportingNode, _ := d["node_id_num"].(uint32)
	if reportingNode == 0 {
		reportingNode = event.FromNode
	}
	if reportingNode == 0 {
		return
	}

	neighbors, ok := d["neighbors"].([]map[string]any)
	if !ok {
		return
	}

	now := event.Time.Unix()
	for _, nb := range neighbors {
		var neighborNum uint32
		if v, ok := nb["node_id"].(string); ok && len(v) > 1 && v[0] == '!' {
			if parsed, err := strconv.ParseUint(v[1:], 16, 32); err == nil {
				neighborNum = uint32(parsed)
			}
		}
		if neighborNum == 0 {
			continue
		}

		snr, _ := nb["snr"].(float32)

		a, b := reportingNode, neighborNum
		if a > b {
			a, b = b, a
		}
		key := uint64(a)<<32 | uint64(b)

		link, exists := s.links[key]
		if !exists {
			link = &LinkRecord{NodeA: a, NodeB: b}
			s.links[key] = link
		}
		link.SNR = snr
		link.Count++
		link.LastSeen = now
		link.Neighbor = true
	}
}

// trackDedup checks if a packet (from+id) has been seen before.
// Must be called with s.mu held.
func (s *Store) trackDedup(event *decoder.Event) {
	if event.PacketID == 0 || event.FromNode == 0 {
		return
	}
	key := uint64(event.FromNode)<<32 | uint64(event.PacketID)
	if _, exists := s.seenPackets[key]; exists {
		return
	}
	s.seenPackets[key] = struct{}{}
	// Evict old entries when the map grows too large (keep 20k)
	if len(s.seenPackets) > 20000 {
		i := 0
		for k := range s.seenPackets {
			delete(s.seenPackets, k)
			i++
			if i >= 5000 {
				break
			}
		}
	}
}

// trackHops accumulates hop statistics per event type.
// Must be called with s.mu held.
func (s *Store) trackHops(event *decoder.Event) {
	if event.HopStart == 0 && event.HopLimit == 0 {
		return
	}
	typ := string(event.Type)
	acc, ok := s.hopStats[typ]
	if !ok {
		acc = &hopAccum{
			minLimit: ^uint32(0),
			minStart: ^uint32(0),
		}
		s.hopStats[typ] = acc
	}
	acc.count++
	acc.sumLimit += uint64(event.HopLimit)
	if event.HopLimit < acc.minLimit {
		acc.minLimit = event.HopLimit
	}
	if event.HopLimit > acc.maxLimit {
		acc.maxLimit = event.HopLimit
	}
	acc.sumStart += uint64(event.HopStart)
	if event.HopStart > 0 {
		if event.HopStart < acc.minStart {
			acc.minStart = event.HopStart
		}
		if event.HopStart > acc.maxStart {
			acc.maxStart = event.HopStart
		}
	}
	if event.HopStart >= event.HopLimit {
		acc.sumTraveled += uint64(event.HopStart - event.HopLimit)
	}
}

// trackRelay increments the relay counter for the packet's relay node and
// the per-type sub-counter. Must be called with s.mu held.
func (s *Store) trackRelay(event *decoder.Event) {
	if event.RelayNode == 0 {
		return
	}
	a := s.relayCounts[event.RelayNode]
	if a == nil {
		a = &relayAgg{byType: make(map[string]int)}
		s.relayCounts[event.RelayNode] = a
	}
	a.total++
	a.byType[string(event.Type)]++
}

// Links returns a snapshot of all observed node-pair links.
func (s *Store) Links() []LinkRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]LinkRecord, 0, len(s.links))
	for _, l := range s.links {
		out = append(out, *l)
	}
	return out
}

// LoadNodes pre-populates the node index from persisted data (called at startup).
func (s *Store) LoadNodes(nodes []NodeState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range nodes {
		n := nodes[i]
		if n.PacketsByType == nil {
			n.PacketsByType = make(map[string]int)
		}
		s.nodes[n.NodeNum] = &n
	}
}

// LoadTraceroutes pre-populates traceroute records from persisted data.
func (s *Store) LoadTraceroutes(records []TracerouteRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traceroutes = append(s.traceroutes, records...)
}

// LoadEvents replays persisted events into the ring buffer and packet counters.
// Events must be in chronological order (oldest first).
// Also rebuilds link map from the loaded events.
func (s *Store) LoadEvents(events []*decoder.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range events {
		s.events[s.head] = ev
		s.head = (s.head + 1) % s.maxEvents
		s.count++
		s.packetsByType[string(ev.Type)]++
		if ev.Type == decoder.EventTextMessage {
			s.msgCount++
		}
		// Detect our local node from MyInfo events (works for old DB events too)
		if ev.Type == decoder.EventMyInfo {
			if ev.FromNode != 0 {
				s.myNodeNum = ev.FromNode
			} else if v, ok := ev.Details["my_node_num"].(string); ok && len(v) > 1 && v[0] == '!' {
				if num, err := strconv.ParseUint(v[1:], 16, 32); err == nil {
					s.myNodeNum = uint32(num)
				}
			}
		}
		s.trackDedup(ev)
		s.trackHops(ev)
		s.trackRelay(ev)
		s.trackLink(ev)
		s.countNodePacket(ev)
		s.countNodeHopStart(ev)
		// Backfill DX from persisted events. Anomalies are intentionally NOT
		// re-flagged on reload — they reflect live behavior, not historical.
		s.trackDX(ev)
	}
}

// SetCounts lets the caller set exact persisted totals (from the DB).
func (s *Store) SetCounts(totalEvents, totalMessages int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count = totalEvents
	s.msgCount = totalMessages
}

// NodeByNum returns a pointer to a node (for DB persistence after updates).
func (s *Store) NodeByNum(num uint32) (NodeState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[num]
	if !ok {
		return NodeState{}, false
	}
	return *n, true
}

// ResolveRelayNodes matches the last byte of a relay node number against known
// nodes and returns all matching node numbers.
func (s *Store) ResolveRelayNodes(lastByte uint32) []uint32 {
	if lastByte == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matches []uint32
	for num := range s.nodes {
		if num&0xFF == lastByte&0xFF {
			matches = append(matches, num)
		}
	}
	return matches
}

// Nodes returns a snapshot of all known nodes.
func (s *Store) Nodes() []NodeState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]NodeState, 0, len(s.nodes))
	for _, n := range s.nodes {
		out = append(out, *n)
	}
	return out
}

// RecentEvents returns the most recent n events, optionally filtered by type.
func (s *Store) RecentEvents(n int, filterType decoder.EventType) []*decoder.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := s.count
	if total > s.maxEvents {
		total = s.maxEvents
	}

	out := make([]*decoder.Event, 0, n)
	for i := 0; i < total && len(out) < n; i++ {
		idx := (s.head - 1 - i + s.maxEvents) % s.maxEvents
		ev := s.events[idx]
		if ev == nil {
			break
		}
		if filterType != "" && ev.Type != filterType {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// Messages returns the most recent n text messages.
func (s *Store) Messages(n int) []*decoder.Event {
	return s.RecentEvents(n, decoder.EventTextMessage)
}

// TelemetryHistory returns the last n telemetry events for a specific node.
func (s *Store) TelemetryHistory(nodeNum uint32, n int) []*decoder.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := s.count
	if total > s.maxEvents {
		total = s.maxEvents
	}

	out := make([]*decoder.Event, 0, n)
	for i := 0; i < total && len(out) < n; i++ {
		idx := (s.head - 1 - i + s.maxEvents) % s.maxEvents
		ev := s.events[idx]
		if ev == nil {
			break
		}
		if ev.Type == decoder.EventTelemetry && ev.FromNode == nodeNum {
			out = append(out, ev)
		}
	}
	return out
}

// Traceroutes returns all recorded traceroute observations.
func (s *Store) Traceroutes() []TracerouteRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TracerouteRecord, len(s.traceroutes))
	copy(out, s.traceroutes)
	return out
}

// Stats returns aggregate statistics.
// MyNodeNum returns the node number of our locally-connected Meshtastic radio
// (resolved from MyInfo), or 0 if not yet known.
func (s *Store) MyNodeNum() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.myNodeNum
}

// LocalNode returns a snapshot of all known information about the directly
// connected (gateway) node: identity, firmware, capabilities and LoRa config.
// Fields are populated incrementally from the boot-time message sequence and
// may be empty/zero until the corresponding packet has been received.
func (s *Store) LocalNode() LocalNodeInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.localNode
	out.UptimeSeconds = int64(time.Since(s.startTime).Seconds())
	return out
}

// LastEventAt returns the time of the most recent Add() call (zero if none yet).
func (s *Store) LastEventAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastEventAt
}

// EventsPerMinute returns the count of events per 1-minute bucket for the
// last windowMinutes minutes. Oldest bucket first. Uses the ring buffer so
// the window is bounded by how many events fit in it.
func (s *Store) EventsPerMinute(windowMinutes int) []int {
	if windowMinutes <= 0 {
		windowMinutes = 60
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]int, windowMinutes)
	if s.maxEvents <= 0 {
		return out
	}
	now := time.Now()
	cutoff := now.Add(-time.Duration(windowMinutes) * time.Minute)
	total := s.count
	if total > s.maxEvents {
		total = s.maxEvents
	}
	for i := 0; i < total; i++ {
		idx := (s.head - 1 - i + s.maxEvents) % s.maxEvents
		if idx < 0 || idx >= len(s.events) {
			continue
		}
		ev := s.events[idx]
		if ev == nil || ev.Time.Before(cutoff) {
			continue
		}
		// bucket = (now - ev.time) in minutes, from newest (0) to oldest
		diff := int(now.Sub(ev.Time).Minutes())
		if diff < 0 || diff >= windowMinutes {
			continue
		}
		// out[0] = oldest, out[windowMinutes-1] = newest
		out[windowMinutes-1-diff]++
	}
	return out
}

func (s *Store) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	active := 0
	for _, n := range s.nodes {
		if now.Unix()-n.LastHeard < 1800 { // 30 minutes
			active++
		}
	}

	pbt := make(map[string]int, len(s.packetsByType))
	for k, v := range s.packetsByType {
		pbt[k] = v
	}

	hs := make(map[string]HopStats, len(s.hopStats))
	for typ, acc := range s.hopStats {
		if acc.count == 0 {
			continue
		}
		h := HopStats{
			Count:       acc.count,
			AvgHopLimit: float64(acc.sumLimit) / float64(acc.count),
			MinHopLimit: acc.minLimit,
			MaxHopLimit: acc.maxLimit,
			AvgHopStart: float64(acc.sumStart) / float64(acc.count),
			MinHopStart: acc.minStart,
			MaxHopStart: acc.maxStart,
			AvgHopsTraveled: float64(acc.sumTraveled) / float64(acc.count),
		}
		// Sanitize min values when no valid data was recorded
		if acc.minLimit == ^uint32(0) {
			h.MinHopLimit = 0
		}
		if acc.minStart == ^uint32(0) {
			h.MinHopStart = 0
		}
		hs[typ] = h
	}

	// Build relay stats sorted by count descending. For each relay also
	// compute the TopTypes breakdown so the dashboard can show what kinds
	// of packets that relay moves the most.
	relayStats := make([]RelayStat, 0, len(s.relayCounts))
	for lastByte, agg := range s.relayCounts {
		rs := RelayStat{
			NodeID: fmt.Sprintf("..%02x", lastByte&0xFF),
			Count:  agg.total,
		}
		// Resolve last byte to known nodes
		var matches []uint32
		for num := range s.nodes {
			if num&0xFF == lastByte&0xFF {
				matches = append(matches, num)
			}
		}
		if len(matches) == 1 {
			rs.NodeID = fmt.Sprintf("!%08x", matches[0])
			if n, ok := s.nodes[matches[0]]; ok {
				if n.ShortName != "" {
					rs.Name = n.ShortName
				} else if n.LongName != "" {
					rs.Name = n.LongName
				}
			}
		} else if len(matches) > 1 {
			cands := make([]string, len(matches))
			for i, m := range matches {
				cands[i] = fmt.Sprintf("!%08x", m)
			}
			rs.Candidates = cands
		}
		// Per-type breakdown, sorted by count desc, capped at top 5 so the
		// UI stays readable even when a relay has touched every event type.
		if len(agg.byType) > 0 {
			tops := make([]RelayTypeCount, 0, len(agg.byType))
			for t, c := range agg.byType {
				tops = append(tops, RelayTypeCount{Type: t, Count: c})
			}
			sort.Slice(tops, func(i, j int) bool {
				if tops[i].Count != tops[j].Count {
					return tops[i].Count > tops[j].Count
				}
				return tops[i].Type < tops[j].Type
			})
			if len(tops) > 5 {
				tops = tops[:5]
			}
			rs.TopTypes = tops
		}
		relayStats = append(relayStats, rs)
	}
	sort.Slice(relayStats, func(i, j int) bool {
		return relayStats[i].Count > relayStats[j].Count
	})

	return Stats{
		TotalEvents:    s.count,
		TotalNodes:     len(s.nodes),
		ActiveNodes:    active,
		MessagesCount:  s.msgCount,
		UptimeSeconds:  int64(time.Since(s.startTime).Seconds()),
		PacketsByType:  pbt,
		HopStatsByType: hs,
		RelayStats:     relayStats,
	}
}

// Subscribe returns a channel that receives new events in real time.
func (s *Store) Subscribe() (uint64, <-chan *decoder.Event) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	id := s.nextSubID
	s.nextSubID++
	ch := make(chan *decoder.Event, 64)
	s.subs[id] = ch
	return id, ch
}

// Unsubscribe removes a subscriber.
func (s *Store) Unsubscribe(id uint64) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if ch, ok := s.subs[id]; ok {
		close(ch)
		delete(s.subs, id)
	}
}
