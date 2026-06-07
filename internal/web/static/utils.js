// Mesh Reader Dashboard v2 — utility functions

import { state } from './state.js';

// ---- Helpers ----

export function esc(s) {
    if (!s) return '';
    const div = document.createElement('div');
    div.textContent = s;
    return div.innerHTML;
}

export function fmtTime(isoStr) {
    if (!isoStr) return '-';
    const d = new Date(isoStr);
    return d.toLocaleTimeString('it-IT', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

export function fmtNum(n) {
    if (n === undefined || n === null) return '0';
    if (n >= 10000) return (n / 1000).toFixed(1) + 'k';
    return n.toLocaleString('it-IT');
}

export function relativeTime(unixTs) {
    const secs = Math.floor(Date.now() / 1000) - unixTs;
    if (secs < 60) return `${secs}s ago`;
    if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
    if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
    return `${Math.floor(secs / 86400)}d ago`;
}

export function nodeName(id) {
    if (!id || id === '' || id === '^all') return id || '-';
    const num = parseNodeNum(id);
    const node = num ? state.nodes[num] : null;
    return node && node.short_name ? `${node.short_name}` : (id || '-');
}

export function nodeNameByNum(num) {
    const n = state.nodes[num];
    return n ? (n.short_name || n.long_name || n.id || `!${num.toString(16).padStart(8,'0')}`) : `!${(num||0).toString(16).padStart(8,'0')}`;
}

export function parseNodeNum(id) {
    if (!id || typeof id === 'number') return id || 0;
    if (typeof id !== 'string') return 0;
    if (id.startsWith('!')) return parseInt(id.slice(1), 16) || 0;
    return parseInt(id, 16) || 0;
}

export function shortTypeName(type) {
    const map = {
        TEXT_MESSAGE: 'MSG',
        POSITION: 'POS',
        TELEMETRY: 'TEL',
        NODE_INFO: 'NODE',
        ROUTING: 'ROUTE',
        TRACEROUTE: 'TRACE',
        NEIGHBOR_INFO: 'NEIGH',
        STORE_FORWARD: 'S&F',
        STORE_FORWARD_PP: 'S&F++',
        WAYPOINT: 'WPT',
        DETECT_SENSOR: 'SENS',
        ALERT: 'ALERT',
        KEY_VERIFY: 'PKC',
        NODE_STATUS: 'NSTAT',
        RANGE_TEST: 'RNGT',
        MAP_REPORT: 'MAP',
        ENCRYPTED: 'ENC',
        LOG_RECORD: 'LOG',
        MY_INFO: 'MY',
        RAW: 'RAW',
    };
    return map[type] || type;
}

export function eventInfo(ev) {
    const d = ev.details || {};
    switch (ev.type) {
        case 'TEXT_MESSAGE': return `<span class="msg-text">${esc(d.text || '')}</span>`;
        case 'POSITION': return `${(d.lat || 0).toFixed(4)}, ${(d.lon || 0).toFixed(4)}`;
        case 'TELEMETRY':
            if (d.type === 'device') return `bat ${d['battery_level_%'] || '?'}% &middot; ${(d['voltage_v'] || 0).toFixed(2)}V`;
            if (d.type === 'environment') return `${(d['temperature_c'] || 0).toFixed(1)}&deg;C`;
            return d.type || '';
        case 'NODE_INFO': return esc(d.long_name || d.id || '');
        case 'ROUTING': return d.error_reason || '';
        case 'TRACEROUTE': return `${(d.route || []).length} hops`;
        case 'NEIGHBOR_INFO': return `${d.neighbor_count || 0} neighbors`;
        case 'STORE_FORWARD':
        case 'STORE_FORWARD_PP': {
            // Surface the sub-type (heartbeat / history / stats / text) and
            // the RR enum so a router doing S&F is recognizable at a glance.
            const v = d.variant && d.variant !== 'none' ? d.variant : '';
            const rr = d.rr || '';
            return [v, rr].filter(Boolean).join(' · ');
        }
        case 'WAYPOINT': {
            const name = d.name ? esc(d.name) : '';
            const desc = d.description ? esc(d.description) : '';
            const coord = (d.lat !== undefined && d.lon !== undefined)
                ? `${(+d.lat).toFixed(4)}, ${(+d.lon).toFixed(4)}` : '';
            return [name, desc, coord].filter(Boolean).join(' · ');
        }
        case 'DETECT_SENSOR': return `<span class="msg-text">${esc(d.text || '')}</span>`;
        case 'ALERT':         return `<span class="msg-text" style="color:var(--red);font-weight:700">${esc(d.text || '')}</span>`;
        case 'KEY_VERIFY':    return d.phase || '';
        case 'NODE_STATUS':   return `${d.size || '?'} bytes`;
        case 'RANGE_TEST':    return `<span class="msg-text">${esc(d.text || '')}</span>`;
        case 'MAP_REPORT': {
            // Big proto, keep the live-feed line short: identity + role.
            const id = d.short_name ? esc(d.short_name) : (d.long_name ? esc(d.long_name) : '');
            const role = d.role && d.role !== 'CLIENT' ? esc(d.role) : '';
            return [id, role].filter(Boolean).join(' · ');
        }
        case 'RAW': return d.portnum || d.variant || `${d.size || '?'} bytes`;
        default: return '';
    }
}

export function getTypeColor(type) {
    const colors = {
        TEXT_MESSAGE: '#3b82f6', POSITION: '#22c55e', TELEMETRY: '#eab308',
        NODE_INFO: '#a855f7', ROUTING: '#64748b', TRACEROUTE: '#f97316',
        NEIGHBOR_INFO: '#14b8a6', STORE_FORWARD: '#d946ef',
        STORE_FORWARD_PP: '#c026d3',
        WAYPOINT: '#06b6d4',       // cyan-ish, fits the map metaphor
        DETECT_SENSOR: '#84cc16',  // lime — sensor "trigger"
        ALERT: '#dc2626',          // strong red — high-priority
        KEY_VERIFY: '#0ea5e9',     // sky blue — auth/security
        NODE_STATUS: '#94a3b8',    // slate — periodic heartbeat
        RANGE_TEST: '#f59e0b',     // amber — debugging tool
        MAP_REPORT: '#8b5cf6',     // violet — map registry report
        ENCRYPTED: '#78716c', LOG_RECORD: '#475569',
        RAW: '#444', MY_INFO: '#6366f1',
    };
    return colors[type] || '#666';
}

export function makeHopPill(ev) {
    if (!ev.hop_start && !ev.hop_limit) return '';
    const start = ev.hop_start || 0;
    const remaining = ev.hop_limit || 0;
    const used = start >= remaining ? start - remaining : 0;
    if (start === 0 && remaining === 0) return '';
    // Format: "2↑ 1↓ /3"  — used↑  remaining↓  /max
    return ` <span class="hop-pill"><span class="hop-used">${used}</span>↑ <span class="hop-remaining">${remaining}</span>↓ /${start}</span>`;
}

export function makeRelayTag(ev) {
    let parts = [];
    if (ev.via_mqtt) parts.push('<span class="relay-tag mqtt">MQTT</span>');
    if (ev.relay_candidates && ev.relay_candidates.length > 1) {
        const names = ev.relay_candidates.map(id => nodeName(id)).join(', ');
        parts.push(`<span class="relay-tag ambiguous" title="${esc(names)}">relay: ${esc(ev.relay_node)} (${ev.relay_candidates.length}?)</span>`);
    } else if (ev.relay_node) {
        parts.push(`<span class="relay-tag">relay: ${nodeName(ev.relay_node)}</span>`);
    }
    return parts.length ? ' ' + parts.join(' ') : '';
}

// Format a number of seconds as "Xd Yh Zm" or "Yh Zm" or "Zm Ss".
export function fmtUptime(sec) {
    sec = Math.max(0, sec | 0);
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const s = sec % 60;
    if (d > 0) return `${d}d ${h}h ${m}m`;
    if (h > 0) return `${h}h ${m}m`;
    if (m > 0) return `${m}m ${s}s`;
    return `${s}s`;
}

// fmtBytes humanizes a byte count (binary units). Returns '' for 0/undefined
// so kvRows renders a muted dash instead of "0 B".
export function fmtBytes(n) {
    const v0 = Number(n);
    if (!v0 || v0 < 0) return '';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    let v = v0, i = 0;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    const s = (i > 0 && v < 10) ? v.toFixed(2) : (i > 0 && v < 100) ? v.toFixed(1) : Math.round(v).toString();
    return `${s} ${units[i]}`;
}

// sparkline(values, opts?) renders a tiny inline SVG area+line chart for a
// numeric series (used in the My Node cards next to the current value).
// Returns '' for fewer than 2 finite points. Sizing comes from the .ln-spark
// CSS box; the SVG stretches to fill it (preserveAspectRatio="none").
export function sparkline(values, opts) {
    const o = opts || {};
    const vals = (values || []).filter(v => typeof v === 'number' && isFinite(v));
    if (vals.length < 2) return '';
    const W = 100, H = 24, pad = 2;
    const color = o.color || '#7c8499';
    let min = Math.min(...vals), max = Math.max(...vals);
    if (min === max) { min -= 1; max += 1; } // flat series → horizontal centre line
    const n = vals.length;
    const px = i => (i / (n - 1)) * W;
    const py = v => H - pad - ((v - min) / (max - min)) * (H - 2 * pad);
    let line = '', area = `M 0 ${H}`;
    vals.forEach((v, i) => {
        const x = px(i).toFixed(2), y = py(v).toFixed(2);
        line += (i === 0 ? 'M' : ' L') + ` ${x} ${y}`;
        area += ` L ${x} ${y}`;
    });
    area += ` L ${W} ${H} Z`;
    return `<svg class="ln-spark" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" aria-hidden="true">`
        + `<path d="${area}" fill="${color}" fill-opacity="0.13"/>`
        + `<path d="${line}" fill="none" stroke="${color}" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>`
        + `</svg>`;
}

// boolBadge — yes/no pill with color. Undefined/null renders a muted dash.
export function boolBadge(v) {
    if (v === undefined || v === null) return '<span class="ln-dash">—</span>';
    return v
        ? '<span class="ln-bool ln-bool-yes">yes</span>'
        : '<span class="ln-bool ln-bool-no">no</span>';
}

// kvRows renders a flat array of [label, value, opts?] tuples as a
// two-column grid. Empty values render a muted dash so the grid stays
// aligned. When opts.raw is true or a per-row opts.raw override is set,
// the value is inserted as HTML (for badges/spans).
export function kvRows(rows, globalOpts) {
    const gRaw = !!(globalOpts && globalOpts.raw);
    return rows.map(row => {
        const [label, value, opts] = row;
        const rowRaw = gRaw || !!(opts && opts.raw);
        const v = (value === undefined || value === null || value === '') ? '<span class="ln-dash">—</span>' : (rowRaw ? value : esc(String(value)));
        return `<div class="ln-row"><span class="ln-key">${esc(label)}</span><span class="ln-val">${v}</span></div>`;
    }).join('');
}

// ---- Signal-quality coloring ----

export function rssiColor(rssi) {
    if (!rssi || rssi === 0) return '#555';
    if (rssi >= -70)  return '#22c55e';
    if (rssi >= -90)  return '#84cc16';
    if (rssi >= -100) return '#eab308';
    if (rssi >= -110) return '#f97316';
    return '#ef4444';
}

export function snrQualityColor(snrDb) {
    if (snrDb === null || snrDb === undefined) return '#555';
    if (snrDb >= 10) return '#22c55e';
    if (snrDb >= 5)  return '#84cc16';
    if (snrDb >= 0)  return '#eab308';
    if (snrDb >= -5) return '#f97316';
    return '#ef4444';
}

// ---- Geometry helpers ----

export function bearingDeg(lat1, lon1, lat2, lon2) {
    const toRad = Math.PI / 180;
    const dLon = (lon2 - lon1) * toRad;
    const y = Math.sin(dLon) * Math.cos(lat2 * toRad);
    const x = Math.cos(lat1 * toRad) * Math.sin(lat2 * toRad)
            - Math.sin(lat1 * toRad) * Math.cos(lat2 * toRad) * Math.cos(dLon);
    return (Math.atan2(y, x) * 180 / Math.PI + 360) % 360;
}

export function perpOffset(lat1, lon1, lat2, lon2, meters) {
    const bear = bearingDeg(lat1, lon1, lat2, lon2);
    const perpRad = (bear + 90) * Math.PI / 180;
    const R = 6371000;
    return {
        dlat: (meters * Math.cos(perpRad) / R) * (180 / Math.PI),
        dlon: (meters * Math.sin(perpRad) / (R * Math.cos(lat1 * Math.PI / 180))) * (180 / Math.PI),
    };
}
