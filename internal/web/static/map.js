// === Map tab ===
import { state } from './state.js';
import { api } from './api.js';
import { esc, relativeTime, nodeName, parseNodeNum, shortTypeName, getTypeColor, fmtNum, nodeNameByNum, snrQualityColor } from './utils.js';
import { roleBadge, hopStartCellHTML, sumPackets, nodeMatchesFilter } from './nodes.js';

// ---- Map ----
export function initMap() {
    if (state.map) { state.map.invalidateSize(); refreshChUtilLayer(); return; }
    state.map = L.map('mapContainer').setView([44, 11], 6);
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
        attribution: '&copy; OpenStreetMap',
        maxZoom: 18,
    }).addTo(state.map);

    const bounds = [];
    Object.values(state.nodes).forEach(n => {
        if (n.has_pos && n.lat && n.lon) {
            addMarker(n);
            bounds.push([n.lat, n.lon]);
        }
    });
    if (bounds.length > 0) state.map.fitBounds(bounds, { padding: [30, 30] });

    // ChUtil Geo-Monitor legend as a permanent map control.
    addChUtilLegend(state.map);
    // Initial load — background. If the layer checkbox is on it will
    // render, otherwise the cached data sits ready for when it's turned on.
    refreshChUtilLayer();
}

export function addMarker(node) {
    const color = markerColor(node);
    const marker = L.circleMarker([node.lat, node.lon], {
        radius: 8, fillColor: color, color: '#fff', weight: 1, fillOpacity: 0.9,
    }).addTo(state.map);
    marker.bindPopup(nodePopup(node));
    state.markers[node.node_num] = marker;
}

export function updateMapMarker(ev) {
    const num = parseNodeNum(ev.from);
    const node = state.nodes[num];
    if (!node || !node.has_pos) return;
    const existing = state.markers[num];
    if (existing) {
        existing.setLatLng([node.lat, node.lon]);
        existing.setStyle({ fillColor: markerColor(node) });
        existing.setPopupContent(nodePopup(node));
    } else {
        addMarker(node);
    }
    // ChUtil layer repaints via its own periodic refresh; no action here.
}

export function markerColor(node) {
    const age = Math.floor(Date.now() / 1000) - (node.last_heard || 0);
    if (age < 900) return '#22c55e';
    if (age < 3600) return '#eab308';
    return '#555';
}

// Rich popup for a node on the map. Re-uses the same visual vocabulary
// as the Nodes table (role-badge, pkt-badge, hopstart-pill) so one
// glance tells you the same things in both views.
export function nodePopup(n) {
    const longName  = n.long_name  || '-';
    const shortName = n.short_name ? ` · <span class="np-short">${esc(n.short_name)}</span>` : '';
    const nodeId    = n.id || `!${(n.node_num || 0).toString(16).padStart(8, '0')}`;
    const age       = n.last_heard ? Math.floor(Date.now() / 1000) - n.last_heard : null;
    const statusCls = age === null ? 'offline' : age < 900 ? 'online' : age < 3600 ? 'recent' : 'offline';
    const lastHeard = n.last_heard ? relativeTime(n.last_heard) : '—';

    // Identity / hardware line: ID mono + HW model, role badge optional.
    const roleHtml = n.role ? roleBadge(n.role) : '';

    // Radio line: RSSI / SNR + max-hop pill when present.
    const rssi = (n.rssi || n.rssi === 0) ? `${n.rssi}` : '—';
    const snr  = n.snr ? n.snr.toFixed(1) : '—';
    let hopHtml = '';
    if ((n.hop_start_mode | 0) > 0 || (n.hop_start_max | 0) > 0) {
        hopHtml = `<span class="np-label">max hop</span> ${hopStartCellHTML(n)}`;
    }

    // Telemetry line: only render the pieces we actually have so that
    // nodes that only ever send POSITION don't show a row of dashes.
    const telBits = [];
    if (n.battery_level && n.battery_level > 0) {
        const bat = n.battery_level >= 101 ? 'PWD' : `${n.battery_level}%`;
        telBits.push(`<span class="np-label">bat</span> ${bat}`);
    }
    if (n.voltage && n.voltage > 0) {
        telBits.push(`<span class="np-label">V</span> ${n.voltage.toFixed(2)}`);
    }
    if (n.channel_utilization && n.channel_utilization > 0) {
        telBits.push(`<span class="np-label">ChU</span> ${n.channel_utilization.toFixed(1)}%`);
    }
    if (n.air_util_tx && n.air_util_tx > 0) {
        telBits.push(`<span class="np-label">Air</span> ${n.air_util_tx.toFixed(1)}%`);
    }
    const telRow = telBits.length
        ? `<div class="np-row">${telBits.join(' · ')}</div>`
        : '';

    // Packet breakdown: total + per-type badges in descending order,
    // identical style to the Nodes table so it reads the same way.
    const pbt = n.packets_by_type || {};
    const total = sumPackets(pbt);
    let pktHtml = '';
    if (total > 0) {
        const entries = Object.entries(pbt).sort((a, b) => b[1] - a[1]);
        const badges = entries.map(([type, count]) => {
            const color = getTypeColor(type);
            return `<span class="pkt-badge" style="background:${color}20;color:${color}" title="${esc(type)}: ${count}">${esc(shortTypeName(type))}<span class="pkt-count">${fmtNum(count)}</span></span>`;
        }).join('');
        pktHtml = `
            <div class="np-section">
                <div class="np-row np-row-head">
                    <span class="np-label">packets</span>
                    <strong>${fmtNum(total)}</strong>
                </div>
                <div class="np-pkt-badges">${badges}</div>
            </div>`;
    }

    // Position line (skip altitude when 0).
    const posBits = [];
    if (n.has_pos) {
        posBits.push(`${(n.lat || 0).toFixed(5)}, ${(n.lon || 0).toFixed(5)}`);
        if (n.altitude && n.altitude !== 0) {
            posBits.push(`${n.altitude} m`);
        }
    }
    const posRow = posBits.length
        ? `<div class="np-row"><span class="np-label">pos</span> ${posBits.join(' · ')}</div>`
        : '';

    // Neighbors section: list of nodes this one can hear directly,
    // taken from the latest NEIGHBORINFO_APP packet. SNR is what THIS
    // node measured when receiving from each neighbor (so it tells you
    // how well *this* node hears its peers, not the other way around).
    const neighborsHtml = renderNeighborsSection(n);

    return `
        <div class="node-popup">
            <div class="np-head">
                <span class="node-status ${statusCls}"></span>
                <span class="np-name">${esc(longName)}</span>${shortName}
                ${roleHtml}
            </div>
            <div class="np-sub">${esc(nodeId)} · ${esc(n.hw_model || '?')}</div>
            <div class="np-section">
                <div class="np-row">
                    <span class="np-label">RSSI</span> ${rssi}
                    · <span class="np-label">SNR</span> ${snr}
                    ${hopHtml ? '· ' + hopHtml : ''}
                </div>
                ${telRow}
                ${posRow}
                <div class="np-row"><span class="np-label">last</span> ${lastHeard}</div>
            </div>
            ${pktHtml}
            ${neighborsHtml}
        </div>
    `;
}

// renderNeighborsSection builds the "Neighbors" block used by the map
// popup AND by the Nodes-table modal. Returns '' when the node has not
// sent a NeighborInfo yet (so we don't show an empty section by default).
//
// Each row resolves the neighbor name from state.nodes when known so the
// user sees readable identifiers; unknown ones fall back to the !id hex.
export function renderNeighborsSection(n) {
    const list = n.neighbors;
    if (!list || list.length === 0) return '';
    const sorted = list.slice().sort((a, b) => (b.snr | 0) - (a.snr | 0));
    const rows = sorted.map(nb => {
        const info  = state.nodes[nb.node_num];
        const label = info && (info.long_name || info.short_name)
            ? esc(info.long_name || info.short_name)
            : esc(nb.id || `!${(nb.node_num >>> 0).toString(16).padStart(8, '0')}`);
        const sn = (nb.snr === undefined || nb.snr === null) ? null : nb.snr;
        const snDb = sn === null ? '—' : sn.toFixed(2) + ' dB';
        const color = sn === null ? '#555' : snrQualityColor(sn);
        const short = info && info.short_name ? `<span class="nb-short">${esc(info.short_name)}</span>` : '';
        return `<div class="nb-row">
            <span class="nb-name">${label}</span>${short}
            <span class="nb-snr" style="background:${color}">${snDb}</span>
        </div>`;
    }).join('');
    const ageBit = n.neighbors_at
        ? `<span class="np-sub-tiny" title="Last NeighborInfo packet">${relativeTime(n.neighbors_at)}</span>`
        : '';
    const intvBit = n.neighbor_broadcast_secs
        ? `<span class="np-sub-tiny">every ${Math.round(n.neighbor_broadcast_secs / 60)} min</span>`
        : '';
    return `<div class="np-section nb-section">
        <div class="np-row np-row-head">
            <span class="np-label">neighbors</span>
            <strong>${list.length}</strong>
            ${ageBit}
            ${intvBit}
        </div>
        <div class="nb-list">${rows}</div>
    </div>`;
}

import { CHUTIL_BANDS } from './state.js';

// ---- Channel utilization heatmap overlay ----
// ── ChUtil Geo-Monitor ────────────────────────────────────────────────
export function chutilColor(v) {
    if (!v || v <= 0) return '#374151'; // no data / zero → neutral grey
    for (const b of CHUTIL_BANDS) {
        if (v < b.max) return b.color;
    }
    return CHUTIL_BANDS[CHUTIL_BANDS.length - 1].color;
}

// Fetches the payload and re-renders both layers + banner.
export async function refreshChUtilLayer() {
    if (!state.map) return;
    const windowHours = parseInt(document.getElementById('chutil-window')?.value || 24);
    let data;
    try {
        data = await api('/api/chutil-zones?window=' + windowHours);
    } catch {
        return;
    }
    state.chutilZones = (data && data.nodes) || [];
    renderChUtilBanner(data);
    renderChUtilCircles();
    renderChUtilBloom();
}

export function renderChUtilBanner(data) {
    const el = document.getElementById('chutil-banner');
    if (!el) return;
    const reporting = (data && data.reporting) || 0;
    if (reporting === 0) {
        el.style.display = 'none';
        return;
    }
    const avg = (data.network_avg || 0).toFixed(1);
    const mx = (data.network_max || 0).toFixed(1);
    let peakName = '';
    if (data.network_peak_node) {
        const n = (state.chutilZones.find(z => z.node_num === data.network_peak_node) || {});
        peakName = n.name ? ` (${esc(n.name)})` : '';
    }
    const peakTime = data.network_peak_time ? relativeTime(data.network_peak_time) : '';
    el.innerHTML = `
        <span class="chutil-banner-num">${reporting}</span> nodi monitorati
        <span class="chutil-banner-sep">·</span>
        media rete <strong>${avg}%</strong>
        <span class="chutil-banner-sep">·</span>
        picco <strong>${mx}%</strong>${peakName}${peakTime ? ' <span class="text-dim">('+peakTime+')</span>' : ''}
    `;
    el.style.display = 'block';
}

export function renderChUtilCircles() {
    if (!state.map) return;
    const on = document.getElementById('chutil-layer')?.checked;
    // Always clear & rebuild — simpler than diffing.
    if (state.chutilLayer) {
        state.map.removeLayer(state.chutilLayer);
        state.chutilLayer = null;
    }
    if (!on) return;
    const metric = document.getElementById('chutil-metric')?.value || 'current';
    const group = L.layerGroup();
    state.chutilZones.forEach(z => {
        const value = pickChUtilMetric(z, metric);
        const stale = (metric === 'current') && (z.current_age_min > 360); // >6h
        const color = value > 0 ? chutilColor(value) : '#374151';
        // radius scales with zoom-indep px; bigger for fresh data
        const radius = stale ? 10 : 16;
        const circle = L.circleMarker([z.lat, z.lon], {
            radius: radius,
            fillColor: color,
            color: stale ? '#9ca3af' : '#0b0d15',
            weight: 2,
            fillOpacity: stale ? 0.35 : 0.85,
            dashArray: stale ? '3,3' : null,
        }).addTo(group);
        circle.bindPopup(chutilPopupHTML(z, metric));
        // Open with live sparkline fetch on demand
        circle.on('popupopen', () => injectChUtilSparkline(z.node_num));
        // Value label above each node
        if (value > 0) {
            const icon = L.divIcon({
                className: 'chutil-label',
                html: `<span>${Math.round(value)}%</span>`,
                iconSize: [36, 14],
                iconAnchor: [18, -14],
            });
            L.marker([z.lat, z.lon], { icon, interactive: false }).addTo(group);
        }
    });
    state.chutilLayer = group.addTo(state.map);
}

export function pickChUtilMetric(z, metric) {
    switch (metric) {
        case 'avg': return z.avg || 0;
        case 'p95': return z.p95 || 0;
        case 'max': return z.max || 0;
        default:    return z.current || 0;
    }
}

export function renderChUtilBloom() {
    if (!state.map) return;
    const on = document.getElementById('chutil-bloom')?.checked;
    if (state.chutilBloom) {
        state.map.removeLayer(state.chutilBloom);
        state.chutilBloom = null;
    }
    if (!on) return;
    const metric = document.getElementById('chutil-metric')?.value || 'current';
    // Anchor gradient to the same fixed scale (0..40% → 0..1)
    const pts = state.chutilZones
        .map(z => [z.lat, z.lon, Math.min((pickChUtilMetric(z, metric) || 0) / 40, 1)])
        .filter(p => p[2] > 0);
    // Suppress the bloom when too few zones are above zero — Leaflet.heat
    // produces angular "arrow" shapes on sparse data because the radial
    // gradient interpolation degenerates to triangles when neighboring
    // blobs don't overlap enough to smooth out. 6 is the empirical
    // sweet spot at the typical zoom levels we render at.
    if (pts.length < 6) return;
    state.chutilBloom = L.heatLayer(pts, {
        // Smaller radius + lighter blur keeps each blob shaped like a
        // disc rather than bleeding diagonally into a triangle.
        radius: 60,
        blur: 35,
        // Wider zoom range = blobs scale more gracefully when zooming.
        maxZoom: 18,
        // max < 1.0 prevents full saturation: stacked blobs sum without
        // clipping to a hard polygon edge, which is what produced the
        // "arrow" artifact at high node density.
        max: 0.6,
        minOpacity: 0.3,
        gradient: {
            0.00: '#16a34a',  // 0%    green
            0.25: '#eab308',  // 10%   yellow
            0.50: '#f97316',  // 20%   orange
            0.75: '#dc2626',  // 30%   red
            1.00: '#9333ea',  // 40%+  purple (critical)
        },
    }).addTo(state.map);
}

export function chutilPopupHTML(z, metric) {
    const currTxt = z.current ? z.current.toFixed(1) + '%' : '—';
    const ageTxt = z.current_age_min >= 0 ? `(${z.current_age_min} min fa)` : '';
    const selTxt = metric !== 'current'
        ? `<div class="text-dim" style="font-size:11px">metric: ${esc(metric.toUpperCase())}</div>` : '';
    const peakTxt = z.peak_time ? relativeTime(z.peak_time) : '—';

    // Pull full node info from the state so the ChUtil popup carries the
    // same identity/role/packet context as the regular node popup. This
    // avoids the dashboard having to re-request per-node data.
    const n = state.nodes[z.node_num] || {};
    const shortHtml = n.short_name ? ` · <span class="np-short">${esc(n.short_name)}</span>` : '';
    const roleHtml  = n.role ? roleBadge(n.role) : '';
    const nodeId    = n.id || `!${(z.node_num >>> 0).toString(16).padStart(8, '0')}`;
    const hw        = n.hw_model || '';
    const age       = n.last_heard ? Math.floor(Date.now() / 1000) - n.last_heard : null;
    const statusCls = age === null ? 'offline' : age < 900 ? 'online' : age < 3600 ? 'recent' : 'offline';
    const lastHeard = n.last_heard ? relativeTime(n.last_heard) : '—';

    // Max-hop pill (mode + peak) when we have observations for this node.
    let hopHtml = '';
    if ((n.hop_start_mode | 0) > 0 || (n.hop_start_max | 0) > 0) {
        hopHtml = `<span class="np-label">max hop</span> ${hopStartCellHTML(n)}`;
    }

    // Packet breakdown section, same widgets as the node popup.
    const pbt = n.packets_by_type || {};
    const total = sumPackets(pbt);
    let pktHtml = '';
    if (total > 0) {
        const badges = Object.entries(pbt).sort((a, b) => b[1] - a[1]).map(([type, count]) => {
            const color = getTypeColor(type);
            return `<span class="pkt-badge" style="background:${color}20;color:${color}" title="${esc(type)}: ${count}">${esc(shortTypeName(type))}<span class="pkt-count">${fmtNum(count)}</span></span>`;
        }).join('');
        pktHtml = `
            <div class="np-section">
                <div class="np-row np-row-head">
                    <span class="np-label">packets</span>
                    <strong>${fmtNum(total)}</strong>
                </div>
                <div class="np-pkt-badges">${badges}</div>
            </div>`;
    }

    // Node name: keep the existing "APUANIA 36 (jacky) CAMPO CECINA (MS)"
    // title (it's the server-supplied long+area string) but add the
    // short name and role badge on a line below.
    return `
        <div class="chutil-popup">
            <div class="np-head">
                <span class="node-status ${statusCls}"></span>
                <span class="np-name">${esc(z.name || n.long_name || '-')}</span>${shortHtml}
                ${roleHtml}
            </div>
            <div class="np-sub">${esc(nodeId)}${hw ? ' · ' + esc(hw) : ''}</div>
            ${selTxt}
            <div class="chutil-popup-grid">
                <span>Current</span><strong>${currTxt}</strong><span class="text-dim">${ageTxt}</span>
                <span>Avg</span><strong>${(z.avg||0).toFixed(1)}%</strong><span></span>
                <span>P50 / P95</span><strong>${(z.p50||0).toFixed(1)}% / ${(z.p95||0).toFixed(1)}%</strong><span></span>
                <span>Peak</span><strong>${(z.max||0).toFixed(1)}%</strong><span class="text-dim">${peakTxt}</span>
                <span>AirTx avg/peak</span><strong>${(z.air_avg||0).toFixed(1)}% / ${(z.air_max||0).toFixed(1)}%</strong><span></span>
                <span>Samples</span><strong>${z.samples}</strong><span></span>
            </div>
            <div class="chutil-spark" data-node="${z.node_num}">
                <div class="text-dim" style="font-size:10px">loading history…</div>
            </div>
            <div class="np-section">
                <div class="np-row">
                    ${hopHtml ? hopHtml + ' · ' : ''}<span class="np-label">last</span> ${lastHeard}
                </div>
            </div>
            ${pktHtml}
        </div>
    `;
}

// Fetch per-node ChUtil history and render an inline SVG sparkline inside
// the just-opened popup. Data lives outside the payload to keep /zones cheap.
export async function injectChUtilSparkline(nodeNum) {
    const holder = document.querySelector(`.chutil-spark[data-node="${nodeNum}"]`);
    if (!holder) return;
    const hours = parseInt(document.getElementById('chutil-window')?.value || 24);
    // Backend parseNodeID expects hex (same convention as /api/signal/:hex
    // and /api/telemetry/:hex). Decimal would overflow uint32 parse in hex
    // mode and return 400 "invalid id".
    const hex = (nodeNum >>> 0).toString(16).padStart(8, '0');
    let pts;
    try {
        pts = await api(`/api/chutil-history?id=${hex}&hours=${hours}`);
    } catch {
        holder.innerHTML = '<div class="text-dim" style="font-size:10px">history unavailable</div>';
        return;
    }
    if (!Array.isArray(pts) || pts.length < 2) {
        holder.innerHTML = '<div class="text-dim" style="font-size:10px">not enough samples yet</div>';
        return;
    }
    const W = 220, H = 44, pad = 2;
    const tMin = pts[0].time, tMax = pts[pts.length - 1].time;
    const tSpan = Math.max(1, tMax - tMin);
    // Cap y-axis at 40% or observed max (whichever larger) so the shape
    // is comparable across nodes in the same session.
    let yMax = 40;
    pts.forEach(p => { if (p.chan_util > yMax) yMax = p.chan_util; });
    const x = p => pad + ((p.time - tMin) / tSpan) * (W - 2 * pad);
    const y = v => pad + (1 - Math.min(v / yMax, 1)) * (H - 2 * pad);
    let path = '';
    pts.forEach((p, i) => {
        path += (i === 0 ? 'M' : 'L') + x(p).toFixed(1) + ',' + y(p.chan_util).toFixed(1);
    });
    // Threshold bands (as faint horizontal lines)
    const bandsY = [10, 20, 30, 40].filter(v => v <= yMax);
    let bandsSVG = '';
    bandsY.forEach(v => {
        const yy = y(v).toFixed(1);
        bandsSVG += `<line x1="${pad}" y1="${yy}" x2="${W-pad}" y2="${yy}" stroke="${chutilColor(v + 0.01)}" stroke-opacity="0.18" stroke-dasharray="2,2"/>`;
    });
    holder.innerHTML = `
        <div class="text-dim" style="font-size:10px;margin-top:4px">last ${hours}h · y-max ${yMax.toFixed(0)}%</div>
        <svg viewBox="0 0 ${W} ${H}" width="${W}" height="${H}" style="background:rgba(0,0,0,0.25);border-radius:3px;margin-top:2px">
            ${bandsSVG}
            <path d="${path}" fill="none" stroke="#22d3ee" stroke-width="1.4"/>
        </svg>`;
}

// On-map legend, always visible.
export function addChUtilLegend(map) {
    if (state.chutilLegend) return;
    const legend = L.control({ position: 'bottomleft' });
    legend.onAdd = function () {
        const div = L.DomUtil.create('div', 'chutil-legend');
        div.innerHTML = `
            <div class="chutil-legend-title">ChUtil</div>
            ${CHUTIL_BANDS.map(b => `
                <div class="chutil-legend-row">
                    <span class="chutil-legend-sw" style="background:${b.color}"></span>
                    ${esc(b.label)}
                </div>`).join('')}
            <div class="chutil-legend-row" style="margin-top:2px">
                <span class="chutil-legend-sw" style="background:#374151"></span>
                no data / stale
            </div>`;
        return div;
    };
    legend.addTo(map);
    state.chutilLegend = legend;
}
