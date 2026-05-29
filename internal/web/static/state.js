// Mesh Reader Dashboard v2 — state and constants

// ---- State ----
export const state = {
    nodes: {},        // nodeNum -> node
    messages: [],
    stats: {},
    traceroutes: [],
    eventSource: null,
    eventSourceReconnect: 0,
    snifferRing: [],
    map: null,
    networkMap: null,
    markers: {},      // nodeNum -> L.marker
    connLines: [],    // L.polyline[]
    charts: {},
    activeTab: 'overview',
    selectedNode: null,
    autoRefreshTimer: null,
    autoRefreshCountdown: 60,
    autoRefreshCountdownTimer: null,
};

// ---- Sort state per table ----
state.sort = {
    nodes:    { key: 'packets',  dir: -1 }, // default: packets desc
    messages: { key: 'time',     dir: -1 }, // default: time desc
};

// ---- Misbehaving ----
state.lastMisbReport = null;

// ---- Channel utilization heatmap overlay ----
state.chutilLayer = null;    // L.LayerGroup of circles + labels
state.chutilBloom = null;    // L.heatLayer
state.chutilZones = [];      // last payload, cached for re-renders
state.chutilLegend = null;   // L.Control
state.heatmapCells = [];     // last heatmap payload, so click handler can reuse data
state.heatmapMax = {volume:0, nodes:0, snrMin:0, snrMax:0};

export const AUTO_REFRESH_INTERVAL = 60; // seconds (fallback reconciliation — SSE handles real-time)

// ---- Temporal heatmap (weekday × hour) ----
export const heatmapDayLabels = ['Mon','Tue','Wed','Thu','Fri','Sat','Sun'];

// Fixed scale. Keep in sync with the legend markup.
export const CHUTIL_BANDS = [
    { max: 10, color: '#16a34a', label: '0–10%   sana' },
    { max: 20, color: '#eab308', label: '10–20%  ok' },
    { max: 30, color: '#f97316', label: '20–30%  alta' },
    { max: 40, color: '#dc2626', label: '30–40%  problematica' },
    { max: Infinity, color: '#9333ea', label: '≥40%   critica' },
];

// ---- Telemetry charts ----
// Display groups, in render order.
export const TEL_GROUPS = ['device', 'environment', 'air_quality', 'power', 'local_stats', 'health', 'other'];
export const TEL_GROUP_LABELS = {
    device: 'Device', environment: 'Environment', air_quality: 'Air Quality',
    power: 'Power', local_stats: 'Local Stats', health: 'Health', other: 'Other',
};
// Metadata for known telemetry detail keys. Unknown keys get a generated
// label, the default color and an auto-scaled y-axis (see telMeta).
export const TEL_FIELDS = {
    'battery_level_%':        { label: 'Battery', unit: '%', color: '#22c55e', yMax: 100, group: 'device' },
    'voltage_v':              { label: 'Voltage', unit: 'V', color: '#3b82f6', group: 'device' },
    'channel_utilization_%':  { label: 'Channel Util', unit: '%', color: '#f97316', yMax: 100, group: 'device' },
    'air_util_tx_%':          { label: 'Air Util TX', unit: '%', color: '#eab308', yMax: 100, group: 'device' },
    'uptime_seconds':         { label: 'Uptime', unit: 's', color: '#8b5cf6', group: 'device' },
    'temperature_c':          { label: 'Temperature', unit: '°C', color: '#ef4444', group: 'environment' },
    'relative_humidity_%':    { label: 'Humidity', unit: '%', color: '#06b6d4', yMax: 100, group: 'environment' },
    'barometric_pressure_hpa':{ label: 'Pressure', unit: 'hPa', color: '#0ea5e9', group: 'environment' },
    'gas_resistance':         { label: 'Gas Resistance', unit: 'MΩ', color: '#a3e635', group: 'environment' },
    'current_ma':             { label: 'Current', unit: 'mA', color: '#f59e0b', group: 'environment' },
    'iaq':                    { label: 'IAQ', unit: '', color: '#84cc16', group: 'environment' },
    'distance_mm':            { label: 'Distance', unit: 'mm', color: '#14b8a6', group: 'environment' },
    'lux':                    { label: 'Lux', unit: 'lx', color: '#facc15', group: 'environment' },
    'white_lux':              { label: 'White Lux', unit: 'lx', color: '#fde047', group: 'environment' },
    'ir_lux':                 { label: 'IR Lux', unit: 'lx', color: '#fb7185', group: 'environment' },
    'uv_lux':                 { label: 'UV Lux', unit: 'lx', color: '#a78bfa', group: 'environment' },
    'wind_direction_deg':     { label: 'Wind Direction', unit: '°', color: '#38bdf8', yMax: 360, group: 'environment' },
    'wind_speed_ms':          { label: 'Wind Speed', unit: 'm/s', color: '#22d3ee', group: 'environment' },
    'wind_gust_ms':           { label: 'Wind Gust', unit: 'm/s', color: '#0891b2', group: 'environment' },
    'wind_lull_ms':           { label: 'Wind Lull', unit: 'm/s', color: '#67e8f9', group: 'environment' },
    'weight_kg':              { label: 'Weight', unit: 'kg', color: '#d6d3d1', group: 'environment' },
    'radiation_urh':          { label: 'Radiation', unit: 'µR/h', color: '#fbbf24', group: 'environment' },
    'rainfall_1h_mm':         { label: 'Rainfall (1h)', unit: 'mm', color: '#60a5fa', group: 'environment' },
    'rainfall_24h_mm':        { label: 'Rainfall (24h)', unit: 'mm', color: '#3b82f6', group: 'environment' },
    'soil_moisture_%':        { label: 'Soil Moisture', unit: '%', color: '#a16207', yMax: 100, group: 'environment' },
    'soil_temperature_c':     { label: 'Soil Temp', unit: '°C', color: '#ea580c', group: 'environment' },
    'pm10_standard':          { label: 'PM1.0', unit: 'µg/m³', color: '#94a3b8', group: 'air_quality' },
    'pm25_standard':          { label: 'PM2.5', unit: 'µg/m³', color: '#cbd5e1', group: 'air_quality' },
    'pm100_standard':         { label: 'PM10', unit: 'µg/m³', color: '#e2e8f0', group: 'air_quality' },
    'ch1_voltage_v':          { label: 'Ch1 Voltage', unit: 'V', color: '#3b82f6', group: 'power' },
    'ch1_current_ma':         { label: 'Ch1 Current', unit: 'mA', color: '#60a5fa', group: 'power' },
    'ch2_voltage_v':          { label: 'Ch2 Voltage', unit: 'V', color: '#8b5cf6', group: 'power' },
    'ch2_current_ma':         { label: 'Ch2 Current', unit: 'mA', color: '#a78bfa', group: 'power' },
    'ch3_voltage_v':          { label: 'Ch3 Voltage', unit: 'V', color: '#ec4899', group: 'power' },
    'ch3_current_ma':         { label: 'Ch3 Current', unit: 'mA', color: '#f472b6', group: 'power' },
    'num_packets_tx':         { label: 'Packets TX', unit: '', color: '#22c55e', group: 'local_stats' },
    'num_packets_rx':         { label: 'Packets RX', unit: '', color: '#3b82f6', group: 'local_stats' },
    'num_packets_rx_bad':     { label: 'Packets RX Bad', unit: '', color: '#ef4444', group: 'local_stats' },
    'num_online_nodes':       { label: 'Online Nodes', unit: '', color: '#14b8a6', group: 'local_stats' },
    'num_total_nodes':        { label: 'Total Nodes', unit: '', color: '#0ea5e9', group: 'local_stats' },
    'num_rx_dupe':            { label: 'RX Duplicates', unit: '', color: '#f97316', group: 'local_stats' },
    'num_tx_relay':           { label: 'TX Relayed', unit: '', color: '#a3e635', group: 'local_stats' },
    'num_tx_relay_canceled':  { label: 'TX Relay Cancel', unit: '', color: '#eab308', group: 'local_stats' },
    'num_tx_dropped':         { label: 'TX Dropped', unit: '', color: '#dc2626', group: 'local_stats' },
    'heap_total_bytes':       { label: 'Heap Total', unit: 'B', color: '#64748b', group: 'local_stats' },
    'heap_free_bytes':        { label: 'Heap Free', unit: 'B', color: '#94a3b8', group: 'local_stats' },
    'noise_floor_dbm':        { label: 'Noise Floor', unit: 'dBm', color: '#f59e0b', group: 'local_stats' },
    'heart_bpm':              { label: 'Heart Rate', unit: 'bpm', color: '#ef4444', group: 'health' },
    'spo2_%':                 { label: 'SpO₂', unit: '%', color: '#06b6d4', yMax: 100, group: 'health' },
};
export const DEFAULT_TEL_COLOR = '#7c8499';

// ---- Misbehaving metrics ----
export const MISB_METRICS = [
    { ui: 'node_info', enabled: 'node_info_enabled', count: 'node_info_count', win: 'node_info_window_sec' },
    { ui: 'telemetry', enabled: 'telemetry_enabled', count: 'telemetry_count', win: 'telemetry_window_sec' },
    { ui: 'position',  enabled: 'position_enabled',  count: 'position_count',  win: 'position_window_sec'  },
    { ui: 'max_hop',   enabled: 'max_hop_enabled',   count: 'max_hop_value',   win: 'max_hop_window_sec'   },
];

// ---- Network / Traceroute map ----
state.networkLayers = { nodes: null, traces: null, links: null };
state.networkLinksData = [];

// ---- Misbehaving notify status timer ----
export let _notifyStatusTimer = null;

// ---- Sniffer (overheard packets) ----
export const sniffer = {
    timer:   null,
    latestId: 0,   // not used (DB query is by filter, not by id)
    rendered: false,
};
