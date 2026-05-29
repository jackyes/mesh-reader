// === Overview tab ===
import { state } from './state.js';
import { api } from './api.js';
import { esc, fmtTime, fmtNum, relativeTime, nodeName, parseNodeNum, shortTypeName, getTypeColor, eventInfo, makeHopPill, makeRelayTag, nodeNameByNum } from './utils.js';
import { heatmapCells, heatmapMax, heatmapDayLabels } from './state.js';

// Re-export so other modules (sse.js) can import these from overview.js
export { heatmapCells, heatmapMax };

// ---- Overview ----
export function renderOverview(events) {
    updateStatsCards();
    renderTypesChart();
    const tbody = document.querySelector('#recent-events tbody');
    tbody.innerHTML = '';
    (events || []).forEach(ev => tbody.appendChild(makeEventRow(ev)));
}

export function updateStatsCards() {
    const s = state.stats;
    const total = s.total_events || 0;
    const nodes = s.total_nodes || 0;
    const active = s.active_nodes || countActive();

    document.getElementById('stat-nodes').textContent = fmtNum(nodes);
    document.getElementById('stat-active').textContent = fmtNum(active);
    document.getElementById('stat-messages').textContent = fmtNum(s.messages_count || 0);
    document.getElementById('stat-events').textContent = fmtNum(total);
    // Sub-labels
    const activePct = document.getElementById('stat-active-pct');
    if (activePct) activePct.textContent = nodes > 0 ? `${Math.round(active / nodes * 100)}% of nodes` : '';

    renderClassStrip();
    renderHopStats();
    renderRelayStats();
    renderRadioHealth();
    renderChannelUtil();
    renderAvailEvents();
    renderEventsSparkline();
    renderIsolatedNodes();
    renderSignalTrends();
    renderAnomalies();
    renderHeatmapTemporal();
    renderDXLeaderboard();
}

// Populate the per-class strip from state.stats.class_counts (cumulative
// since startup). transit = packets between two other nodes we overheard.
export function renderClassStrip() {
    const cc = (state.stats && state.stats.class_counts) || {};
    const personal  = cc.personal  || 0;
    const broadcast = cc.broadcast || 0;
    const transit   = cc.transit   || 0;
    const fromme    = cc.from_me   || 0;
    const total = personal + broadcast + transit + fromme;
    const pct = v => total > 0 ? (v / total * 100).toFixed(1) + '%' : '—';
    const set = (id, v) => { const el = document.getElementById(id); if (el) el.textContent = v; };
    set('class-personal',      fmtNum(personal));
    set('class-broadcast',     fmtNum(broadcast));
    set('class-transit',       fmtNum(transit));
    set('class-fromme',        fmtNum(fromme));
    set('class-personal-pct',  pct(personal));
    set('class-broadcast-pct', pct(broadcast));
    set('class-transit-pct',   pct(transit));
    set('class-fromme-pct',    pct(fromme));
}

// ---- Anomalies (GPS teleport / spammer / SNR jump) ----
export async function renderAnomalies() {
    const cont = document.getElementById('anomalies-container');
    if (!cont) return;
    let rows = [];
    try { rows = await api('/api/anomalies?limit=30'); } catch { return; }
    const pill = document.getElementById('anomaly-count');
    if (!Array.isArray(rows) || rows.length === 0) {
        if (pill) pill.style.display = 'none';
        cont.innerHTML = '<div class="text-dim" style="padding:0.5rem 0">✅ No anomalies detected.</div>';
        return;
    }
    if (pill) {
        pill.style.display = '';
        pill.textContent = `${rows.length}`;
    }
    const sevClass = { critical: 'risk-weak', warning: 'risk-spof', info: 'risk-direct' };
    const typeLbl = {
        gps_teleport: 'GPS',
        spammer:      'SPAM',
        snr_jump:     'SNR',
    };
    let html = '<div class="iso-rows">';
    rows.slice(0, 12).forEach(a => {
        const when = relativeTime(a.time);
        const badge = `<span class="risk-badge ${sevClass[a.severity] || 'risk-direct'}">${typeLbl[a.type] || a.type.toUpperCase()}</span>`;
        html += `<div class="iso-row anomaly-row">
            ${badge}
            <div class="iso-name" title="${esc(a.message)}">${esc(a.node_name || ('!'+(a.node_num||0).toString(16).padStart(8,'0')))}</div>
            <div class="iso-meta">${esc(a.message)}</div>
            <div class="iso-relays">${when}</div>
        </div>`;
    });
    html += '</div>';
    cont.innerHTML = html;
}

// ---- Temporal heatmap (weekday × hour) ----
export async function renderHeatmapTemporal() {
    const cont = document.getElementById('heatmap-temporal-container');
    if (!cont) return;
    const daysSel = document.getElementById('heatmap-days');
    const modeSel = document.getElementById('heatmap-mode');
    const days = daysSel ? parseInt(daysSel.value) : 30;
    const mode = modeSel ? modeSel.value : 'volume';
    let data;
    try { data = await api('/api/heatmap-temporal?days=' + days); } catch { return; }
    const cells = (data && data.cells) || [];
    heatmapCells = cells;

    // Build a 7x24 lookup grid of raw cell objects (reorder so row 0 = Monday).
    const grid = Array.from({length: 7}, () => new Array(24).fill(null));
    let vMax = 0, nMax = 0, sMin = Infinity, sMax = -Infinity, anySNR = false;
    cells.forEach(c => {
        const row = (c.weekday + 6) % 7;
        grid[row][c.hour] = c;
        if (c.count > vMax) vMax = c.count;
        if (c.unique_nodes > nMax) nMax = c.unique_nodes;
        if (c.avg_snr !== 0) {
            anySNR = true;
            if (c.avg_snr < sMin) sMin = c.avg_snr;
            if (c.avg_snr > sMax) sMax = c.avg_snr;
        }
    });
    heatmapMax = {volume:vMax, nodes:nMax, snrMin:sMin, snrMax:sMax, snrAny:anySNR};

    if (vMax === 0) {
        cont.innerHTML = `<div class="text-dim" style="padding:0.5rem 0">No data in last ${days} days.</div>`;
        return;
    }

    const cellW = 22, cellH = 18, padL = 36, padT = 14;
    const W = padL + 24 * cellW + 8;
    const H = padT + 7 * cellH + 22;
    let html = `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="xMinYMin meet" style="width:100%;height:auto;display:block" class="heatmap-svg">`;
    // Hour ticks (every 3h to avoid clutter)
    for (let h = 0; h < 24; h += 3) {
        const x = padL + h * cellW + cellW/2;
        html += `<text x="${x}" y="${padT-3}" text-anchor="middle" fill="#6b7394" font-size="9">${h}</text>`;
    }

    // Current (weekday, hour) marker: highlight the cell for "now"
    const now = new Date();
    const nowRow = (now.getDay() + 6) % 7;
    const nowHour = now.getHours();

    // Cells
    for (let d = 0; d < 7; d++) {
        html += `<text x="${padL-6}" y="${padT + d*cellH + cellH*0.7}" text-anchor="end" fill="#6b7394" font-size="10">${heatmapDayLabels[d]}</text>`;
        for (let h = 0; h < 24; h++) {
            const c = grid[d][h];
            const color = heatmapCellColor(c, mode);
            const x = padL + h * cellW;
            const y = padT + d * cellH;
            const isNow = (d === nowRow && h === nowHour);
            const stroke = isNow ? 'stroke="#facc15" stroke-width="1.5"' : '';
            // weekday back to sqlite convention (0=Sun) so click handler
            // can pass it straight to /api/heatmap-cell-detail
            const sqlWeekday = (d + 1) % 7;
            html += `<rect class="heatmap-cell" data-weekday="${sqlWeekday}" data-hour="${h}" data-row="${d}" x="${x+1}" y="${y+1}" width="${cellW-2}" height="${cellH-2}" rx="2" fill="${color}" ${stroke}></rect>`;
        }
    }
    // Legend — different per mode
    const legY = H - 8;
    const legend = heatmapLegend(mode);
    html += `<text x="${padL}" y="${legY}" fill="#6b7394" font-size="9">${esc(legend.left)}</text>`;
    for (let i = 0; i < 6; i++) {
        const t = i / 5;
        html += `<rect x="${padL + 48 + i*16}" y="${legY-9}" width="14" height="10" rx="2" fill="${heatmapRampColor(t, mode)}"/>`;
    }
    html += `<text x="${padL + 48 + 6*16 + 4}" y="${legY}" fill="#6b7394" font-size="9">${esc(legend.right)}</text>`;
    html += '</svg>';
    cont.innerHTML = html;

    // Wire up hover + click on every cell.
    cont.querySelectorAll('rect.heatmap-cell').forEach(rect => {
        rect.addEventListener('mouseenter', onHeatmapCellHover);
        rect.addEventListener('mousemove', onHeatmapCellMove);
        rect.addEventListener('mouseleave', onHeatmapCellLeave);
        rect.addEventListener('click', onHeatmapCellClick);
    });
}

// --- mode → scalar value for a cell ---
export function heatmapCellValue(c, mode) {
    if (!c) return 0;
    if (mode === 'nodes') return c.unique_nodes || 0;
    if (mode === 'snr')   return c.avg_snr || 0;
    return c.count || 0;
}
// --- mode → color for a raw cell ---
export function heatmapCellColor(c, mode) {
    if (!c) return 'rgba(255,255,255,0.04)';
    if (mode === 'snr') {
        if (!heatmapMax.snrAny || c.avg_snr === 0) return 'rgba(255,255,255,0.04)';
        const span = (heatmapMax.snrMax - heatmapMax.snrMin) || 1;
        const t = (c.avg_snr - heatmapMax.snrMin) / span;
        return heatmapRampColor(t, 'snr');
    }
    if (mode === 'nodes') {
        if (!heatmapMax.nodes) return 'rgba(255,255,255,0.04)';
        return heatmapRampColor((c.unique_nodes || 0) / heatmapMax.nodes, 'nodes');
    }
    if (!heatmapMax.volume) return 'rgba(255,255,255,0.04)';
    return heatmapRampColor((c.count || 0) / heatmapMax.volume, 'volume');
}
// --- ramp by mode (t in 0..1) ---
export function heatmapRampColor(t, mode) {
    if (t <= 0) return 'rgba(255,255,255,0.04)';
    if (t > 1) t = 1;
    if (mode === 'snr') {
        // divergent red → amber → green (worst → best SNR)
        if (t < 0.15) return '#7f1d1d';
        if (t < 0.35) return '#b91c1c';
        if (t < 0.55) return '#d97706';
        if (t < 0.75) return '#84cc16';
        return '#16a34a';
    }
    if (mode === 'nodes') {
        if (t < 0.15) return '#1f2937';
        if (t < 0.35) return '#065f46';
        if (t < 0.55) return '#10b981';
        if (t < 0.75) return '#7c3aed';
        return '#c026d3';
    }
    // volume
    if (t < 0.15) return '#1e293b';
    if (t < 0.35) return '#155e75';
    if (t < 0.55) return '#0891b2';
    if (t < 0.75) return '#a855f7';
    return '#ef4444';
}
export function heatmapLegend(mode) {
    if (mode === 'nodes') return {left:'1 node', right:`${heatmapMax.nodes} nodes`};
    if (mode === 'snr')   return {
        left: heatmapMax.snrAny ? `${heatmapMax.snrMin.toFixed(1)} dB (worst)` : 'no SNR',
        right: heatmapMax.snrAny ? `${heatmapMax.snrMax.toFixed(1)} dB (best)` : ''
    };
    return {left:'less', right:`max ${heatmapMax.volume}`};
}

// --- rich HTML tooltip ---
export function onHeatmapCellHover(e) {
    const rect = e.currentTarget;
    const weekday = parseInt(rect.dataset.weekday);
    const hour = parseInt(rect.dataset.hour);
    const row = parseInt(rect.dataset.row);
    const c = heatmapCells.find(x => x.weekday === weekday && x.hour === hour);
    const tip = document.getElementById('heatmap-tooltip');
    if (!tip) return;
    if (!c || c.count === 0) {
        tip.innerHTML = `<div><strong>${heatmapDayLabels[row]} ${String(hour).padStart(2,'0')}:00</strong></div><div class="text-dim">no events</div>`;
    } else {
        const snrTxt = c.avg_snr !== 0 ? `${c.avg_snr.toFixed(1)} dB` : '—';
        const rssiTxt = c.avg_rssi !== 0 ? `${Math.round(c.avg_rssi)} dBm` : '—';
        const topT = c.top_type ? `${esc(c.top_type)} <span class="text-dim">(${c.top_type_count})</span>` : '—';
        tip.innerHTML = `
            <div><strong>${heatmapDayLabels[row]} ${String(hour).padStart(2,'0')}:00</strong></div>
            <div>${c.count} events · ${c.unique_nodes} nodes</div>
            <div class="text-dim" style="margin-top:2px">Top: ${topT}</div>
            <div class="text-dim">SNR avg ${snrTxt} · RSSI ${rssiTxt}</div>
            <div class="text-dim" style="margin-top:4px;font-size:10px">click for details</div>`;
    }
    tip.style.display = 'block';
    onHeatmapCellMove(e);
}
export function onHeatmapCellMove(e) {
    const tip = document.getElementById('heatmap-tooltip');
    if (!tip || tip.style.display === 'none') return;
    // Position relative to the viewport, then adjust so it stays in-card.
    const cont = document.getElementById('heatmap-temporal-container');
    if (!cont) return;
    const card = cont.parentElement;
    const cardRect = card.getBoundingClientRect();
    let x = e.clientX - cardRect.left + 12;
    let y = e.clientY - cardRect.top + 12;
    const tipRect = tip.getBoundingClientRect();
    if (x + tipRect.width > cardRect.width - 8) x = cardRect.width - tipRect.width - 8;
    if (y + tipRect.height > cardRect.height - 8) y = e.clientY - cardRect.top - tipRect.height - 8;
    tip.style.left = x + 'px';
    tip.style.top = y + 'px';
}
export function onHeatmapCellLeave() {
    const tip = document.getElementById('heatmap-tooltip');
    if (tip) tip.style.display = 'none';
}

// --- drill-down modal ---
export async function onHeatmapCellClick(e) {
    const rect = e.currentTarget;
    const weekday = parseInt(rect.dataset.weekday);
    const hour = parseInt(rect.dataset.hour);
    const row = parseInt(rect.dataset.row);
    const days = parseInt(document.getElementById('heatmap-days')?.value || 30);
    const modal = document.getElementById('heatmap-modal');
    const body = document.getElementById('heatmap-modal-body');
    const title = document.getElementById('heatmap-modal-title');
    if (!modal || !body) return;
    title.textContent = `${heatmapDayLabels[row]} ${String(hour).padStart(2,'0')}:00 — last ${days} days`;
    body.innerHTML = '<div class="text-dim">Loading…</div>';
    modal.style.display = 'flex';
    let det;
    try {
        det = await api(`/api/heatmap-cell-detail?weekday=${weekday}&hour=${hour}&days=${days}`);
    } catch (err) {
        body.innerHTML = `<div class="text-dim">Error loading details.</div>`;
        return;
    }
    if (!det || det.total === 0) {
        body.innerHTML = `<div class="text-dim">No events in this slot.</div>`;
        return;
    }
    const snrLine = det.avg_snr !== 0
        ? `SNR avg <strong>${det.avg_snr.toFixed(1)} dB</strong> · min ${det.min_snr.toFixed(1)} · max ${det.max_snr.toFixed(1)}`
        : 'SNR: no samples';
    const rssiLine = det.avg_rssi !== 0
        ? `RSSI avg <strong>${Math.round(det.avg_rssi)} dBm</strong>`
        : 'RSSI: no samples';
    let html = `
        <div class="heatmap-detail-summary">
            <div><strong>${det.total}</strong> events from <strong>${det.unique_nodes}</strong> unique nodes</div>
            <div class="text-dim">${snrLine}</div>
            <div class="text-dim">${rssiLine}</div>
        </div>`;

    // Type breakdown — horizontal bars
    if (det.types && det.types.length) {
        const tMax = det.types.reduce((m, t) => Math.max(m, t.count), 1);
        html += `<h4 class="heatmap-detail-h">Event types</h4><div class="heatmap-type-bars">`;
        det.types.forEach(t => {
            const pct = (t.count / tMax) * 100;
            html += `<div class="heatmap-type-row">
                <span class="heatmap-type-name">${esc(t.type)}</span>
                <div class="heatmap-type-bar"><div style="width:${pct.toFixed(1)}%"></div></div>
                <span class="heatmap-type-count">${t.count}</span>
            </div>`;
        });
        html += `</div>`;
    }

    // Top nodes
    if (det.top_nodes && det.top_nodes.length) {
        html += `<h4 class="heatmap-detail-h">Top 10 nodes</h4>
            <table class="path-table"><thead><tr><th>#</th><th>Node</th><th>Events</th></tr></thead><tbody>`;
        det.top_nodes.forEach((n, i) => {
            html += `<tr>
                <td class="text-dim">${i+1}</td>
                <td>${esc(n.node_label)}</td>
                <td>${n.count}</td>
            </tr>`;
        });
        html += `</tbody></table>`;
    }

    // Recent samples
    if (det.samples && det.samples.length) {
        html += `<h4 class="heatmap-detail-h">Recent samples</h4>
            <table class="path-table"><thead><tr><th>Time</th><th>Type</th><th>From</th><th>RSSI</th><th>SNR</th></tr></thead><tbody>`;
        det.samples.forEach(s => {
            const t = new Date(s.time);
            const lbl = isNaN(t.getTime()) ? s.time : t.toLocaleString();
            html += `<tr>
                <td class="text-dim" style="white-space:nowrap">${esc(lbl)}</td>
                <td>${esc(s.type)}</td>
                <td>${s.from_node ? '!' + s.from_node.toString(16) : '—'}</td>
                <td>${s.rssi || '—'}</td>
                <td>${s.snr ? s.snr.toFixed(1) : '—'}</td>
            </tr>`;
        });
        html += `</tbody></table>`;
    }
    body.innerHTML = html;
}

// ---- DX leaderboard ----
export async function renderDXLeaderboard() {
    const cont = document.getElementById('dx-leaderboard-container');
    if (!cont) return;
    const onlyDirect = document.getElementById('dx-direct-only')?.checked;
    let rows = [];
    try {
        rows = await api(`/api/dx-records?limit=15&direct_only=${onlyDirect ? 'true' : 'false'}`);
    } catch { return; }
    if (!Array.isArray(rows) || rows.length === 0) {
        cont.innerHTML = `<div class="text-dim" style="padding:0.5rem 0">No DX data yet ${onlyDirect ? '(no direct receptions with both positions known)' : ''}.</div>`;
        return;
    }
    // Bars scale by SNR (primary sort key). SNR can be negative, so map
    // [minSNR, maxSNR] → [10%, 100%] so the weakest still shows a stub.
    const snrs = rows.map(r => r.snr);
    const minSNR = Math.min(...snrs);
    const maxSNR = Math.max(...snrs);
    const spanSNR = (maxSNR - minSNR) || 1;
    let html = '<table class="dx-table"><thead><tr><th>#</th><th>Node</th><th>SNR</th><th>Distance</th><th>RSSI</th><th>Hops</th><th>When</th></tr></thead><tbody>';
    rows.forEach((r, i) => {
        const pct = 10 + ((r.snr - minSNR) / spanSNR) * 90;
        const directBadge = r.direct ? '<span class="dx-direct">direct</span>' : `<span class="dx-relayed">${r.hops_used}h</span>`;
        html += `<tr>
            <td class="dx-rank">${i+1}</td>
            <td class="dx-name">${esc(r.node_name)}</td>
            <td class="dx-dist">
                <div class="dx-bar"><div class="dx-bar-fill" style="width:${pct.toFixed(1)}%"></div></div>
                <span>${r.snr.toFixed(1)} dB</span>
            </td>
            <td>${r.distance_km.toFixed(2)} km</td>
            <td>${r.rssi} dBm</td>
            <td>${directBadge}</td>
            <td class="text-dim">${relativeTime(r.time)}</td>
        </tr>`;
    });
    html += '</tbody></table>';
    cont.innerHTML = html;
}

// ---- Path tracing modal ----
export async function openPacketPath(fromHexOrNum, packetID) {
    const modal = document.getElementById('path-modal');
    const body = document.getElementById('path-modal-body');
    if (!modal || !body) return;
    body.innerHTML = '<div class="text-dim">Loading…</div>';
    modal.style.display = 'flex';
    const fromParam = typeof fromHexOrNum === 'number'
        ? fromHexOrNum.toString(16).padStart(8, '0')
        : String(fromHexOrNum).replace(/^!/, '');
    let data;
    try {
        data = await api(`/api/packet-path?from=${fromParam}${packetID ? '&id=' + packetID : ''}`);
    } catch (e) {
        body.innerHTML = `<div style="color:var(--red)">Error: ${esc(String(e))}</div>`;
        return;
    }
    const fromName = data.from_name || ('!' + (data.from || 0).toString(16).padStart(8,'0'));
    let html = `<div class="path-from"><b>${esc(fromName)}</b> → <b>us</b>`;
    if (data.packet_id) html += `<span class="text-dim"> · packet id ${data.packet_id}</span>`;
    html += `</div>`;
    if (data.receptions && data.receptions.length) {
        html += '<div class="path-section-title">Reception(s)</div>';
        html += '<table class="path-table"><thead><tr><th>Time</th><th>Hops</th><th>Relay hint</th><th>RSSI</th><th>SNR</th></tr></thead><tbody>';
        data.receptions.forEach(ev => {
            // Hops: prefer hop_start as ground truth (it's the original budget).
            // hop_limit can legitimately be 0 (packet ran out of hops on its way to us).
            let hops = '?';
            if (ev.hop_start && ev.hop_start > 0) {
                const remaining = ev.hop_limit || 0;
                const used = ev.hop_start >= remaining ? ev.hop_start - remaining : 0;
                hops = `${used}/${ev.hop_start}`;
            } else if (ev.hop_limit) {
                hops = `?/${ev.hop_limit}+`;
            }
            // Relay column: build HTML in pieces, escape each text part separately
            // so the <span> markup we add ourselves stays as HTML.
            let relayHtml = esc(ev.relay_node || '—');
            if (ev.relay_candidates && ev.relay_candidates.length) {
                const cands = ev.relay_candidates.map(esc).join(', ');
                relayHtml += ` <span class="text-dim">(${ev.relay_candidates.length} candidates: ${cands})</span>`;
            }
            html += `<tr>
                <td>${esc(ev.time.replace('T',' ').slice(0,19))}</td>
                <td>${hops}</td>
                <td>${relayHtml}</td>
                <td>${ev.rssi || '-'}</td>
                <td>${ev.snr ? ev.snr.toFixed(1) : '-'}</td>
            </tr>`;
        });
        html += '</tbody></table>';
    } else {
        html += '<div class="text-dim" style="margin:0.5rem 0">No matching reception in ring buffer (event may have been evicted).</div>';
    }
    if (data.traceroutes && data.traceroutes.length) {
        html += '<div class="path-section-title">Recent traceroutes involving this node (route hints)</div>';
        html += '<div class="path-routes">';
        data.traceroutes.forEach(tr => {
            const from = '!' + tr.from.toString(16).padStart(8,'0');
            const to = '!' + tr.to.toString(16).padStart(8,'0');
            const route = (tr.route || []).join(' → ') || '(empty)';
            html += `<div class="path-route">
                <div class="text-dim" style="font-size:0.7rem">${new Date(tr.time*1000).toLocaleString()}</div>
                <div><b>${esc(from)}</b> → ${esc(route)} → <b>${esc(to)}</b></div>
            </div>`;
        });
        html += '</div>';
    } else {
        html += '<div class="text-dim" style="margin-top:0.6rem">No traceroute data for this node — try the <b>TR</b> button on the Nodes tab to discover its path.</div>';
    }
    body.innerHTML = html;
}

// ---- Node detail modal (Nodes table → click on the name) ----
// Reuses nodePopup() so the modal layout stays consistent with the map
// popup (and any future enrichment shows up in both places at once).
export function openNodeModal(nodeNum) {
    const n = state.nodes[nodeNum];
    if (!n) return;
    const title = document.getElementById('node-modal-title');
    const body  = document.getElementById('node-modal-body');
    const modal = document.getElementById('node-modal');
    if (!title || !body || !modal) return;
    const idStr = n.id || `!${(nodeNum >>> 0).toString(16).padStart(8, '0')}`;
    title.innerHTML = `${esc(n.long_name || '-')}
        ${n.short_name ? `<span class="np-short">${esc(n.short_name)}</span>` : ''}
        <span class="modal-id">${esc(idStr)}</span>`;
    // nodePopup is imported from map.js
    import('./map.js').then(mod => {
        body.innerHTML = mod.nodePopup(n);
    });
    modal.style.display = '';
}

// ---- On-demand traceroute (button on Nodes table) ----
export async function sendTracerouteToNode(nodeNum, btn) {
    if (!nodeNum) return;
    const orig = btn.textContent;
    btn.disabled = true;
    btn.textContent = '…';
    try {
        const hex = nodeNum.toString(16).padStart(8,'0');
        const r = await fetch(`/api/traceroute/${hex}?hops=7`, { method: 'POST' });
        if (!r.ok) {
            const txt = await r.text();
            btn.textContent = 'ERR';
            btn.title = txt;
            setTimeout(() => { btn.textContent = orig; btn.title = 'Send traceroute to this node'; btn.disabled = false; }, 3000);
            return;
        }
        btn.textContent = '✓';
        btn.title = 'Traceroute sent — response will arrive asynchronously';
        setTimeout(() => { btn.textContent = orig; btn.disabled = false; }, 4000);
    } catch (e) {
        btn.textContent = 'ERR';
        btn.title = String(e);
        setTimeout(() => { btn.textContent = orig; btn.disabled = false; }, 3000);
    }
}

// ---- Signal degradation trends ----
export async function renderSignalTrends() {
    const cont = document.getElementById('signal-trends-container');
    if (!cont) return;
    const winSel = document.getElementById('trend-window');
    const hours = winSel ? parseInt(winSel.value) : 24;
    let rows = [];
    try {
        rows = await api(`/api/signal-trends?window_hours=${hours}&min_samples=5&only_bad=true`);
    } catch { return; }
    if (!Array.isArray(rows) || rows.length === 0) {
        cont.innerHTML = `<div class="text-dim" style="padding:0.5rem 0">✅ No nodes are degrading. Window: last ${hours}h vs previous ${hours}h (min 5 samples each).</div>`;
        return;
    }
    const sevBadge = {
        severe:      '<span class="risk-badge risk-weak">SEVERE</span>',
        significant: '<span class="risk-badge risk-direct">SIGNIFICANT</span>',
        minor:       '<span class="risk-badge risk-spof">MINOR</span>',
    };
    let html = `<div class="iso-summary">${rows.length} node(s) with SNR degradation · window ${hours}h</div>`;
    html += '<div class="iso-rows">';
    rows.slice(0, 15).forEach(r => {
        const name = r.long_name || r.short_name || r.id || `!${(r.node_num||0).toString(16).padStart(8,'0')}`;
        const deltaStr = `${r.delta_snr >= 0 ? '+' : ''}${r.delta_snr.toFixed(1)} dB`;
        const sparkHTML = buildTrendSparkline(r);
        html += `<div class="iso-row trend-row">
            ${sevBadge[r.severity] || ''}
            <div class="iso-name" title="SNR ${r.older_mean_snr.toFixed(1)}→${r.recent_mean_snr.toFixed(1)} · RSSI ${r.older_mean_rssi.toFixed(0)}→${r.recent_mean_rssi.toFixed(0)} dBm">${esc(name)}</div>
            <div class="iso-meta">
                <span class="trend-delta ${r.delta_snr < 0 ? 'bad' : 'good'}">${deltaStr}</span>
                <span style="margin-left:0.5rem">${r.older_count}→${r.recent_count} pkt</span>
            </div>
            <div class="trend-spark">${sparkHTML}</div>
        </div>`;
    });
    html += '</div>';
    cont.innerHTML = html;
}

// buildTrendSparkline renders a mini 2-bar chart: older SNR vs recent SNR.
export function buildTrendSparkline(r) {
    const W = 80, H = 22;
    // Normalize SNR to 0..1 over range [-20, +10]
    const normY = v => {
        const n = (v - (-20)) / (10 - (-20));
        return Math.max(0, Math.min(1, n));
    };
    const y1 = H - normY(r.older_mean_snr)  * H;
    const y2 = H - normY(r.recent_mean_snr) * H;
    const color = r.delta_snr <= -4 ? '#ef4444' : r.delta_snr <= -2 ? '#f97316' : '#eab308';
    return `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" width="${W}" height="${H}">
        <line x1="6" y1="${y1.toFixed(1)}" x2="${W-6}" y2="${y2.toFixed(1)}"
              stroke="${color}" stroke-width="2" stroke-linecap="round"/>
        <circle cx="6" cy="${y1.toFixed(1)}" r="2.5" fill="#6b7394"/>
        <circle cx="${W-6}" cy="${y2.toFixed(1)}" r="2.5" fill="${color}"/>
    </svg>`;
}

// ---- Fragile / isolated nodes ----
export async function renderIsolatedNodes() {
    const cont = document.getElementById('isolated-nodes-container');
    if (!cont) return;
    let rows = [];
    try { rows = await api('/api/isolated-nodes?min_packets=3'); } catch { return; }
    if (!Array.isArray(rows) || rows.length === 0) {
        cont.innerHTML = '<div class="text-dim" style="padding:0.5rem 0">No data yet.</div>';
        return;
    }
    // Keep only non-healthy and show top 15
    const risky = rows.filter(r => r.risk !== 'healthy').slice(0, 15);
    const healthyCount = rows.filter(r => r.risk === 'healthy').length;
    if (risky.length === 0) {
        cont.innerHTML = `<div class="text-dim" style="padding:0.5rem 0">✅ All ${rows.length} observed nodes reach us via multiple paths.</div>`;
        return;
    }
    const riskBadge = {
        'weak':        '<span class="risk-badge risk-weak">WEAK</span>',
        'direct-only': '<span class="risk-badge risk-direct">DIRECT-ONLY</span>',
        'spof':        '<span class="risk-badge risk-spof">SPOF</span>',
    };
    let html = `<div class="iso-summary">${risky.length} fragile · ${healthyCount} healthy · threshold ≥ 3 pkts</div>`;
    html += '<div class="iso-rows">';
    risky.forEach(r => {
        const name = r.long_name || r.short_name || r.id || `!${(r.node_num||0).toString(16).padStart(8,'0')}`;
        const sig = r.best_rssi ? `${r.best_rssi} dBm · ${r.best_snr.toFixed(1)} dB` : '—';
        const relays = r.relay_count > 0 ? r.relays.join(' ') : 'none';
        html += `<div class="iso-row">
            ${riskBadge[r.risk] || ''}
            <div class="iso-name" title="${esc(r.risk_reason || '')}">${esc(name)}</div>
            <div class="iso-meta">${r.packets_seen} pkt · ${r.relay_count} relay${r.relay_count===1?'':'s'} · ${sig}</div>
            <div class="iso-relays">${esc(relays)}</div>
        </div>`;
    });
    html += '</div>';
    cont.innerHTML = html;
}

// ---- SNR vs distance scatter (Network tab) ----
export async function renderSNRDistanceChart() {
    const svg = document.getElementById('snr-distance-chart');
    if (!svg) return;
    let points = [];
    try { points = await api('/api/snr-distance'); } catch { return; }
    if (!Array.isArray(points) || points.length === 0) {
        svg.innerHTML = '<text x="400" y="130" text-anchor="middle" fill="#6b7394" font-size="14">Nessun nodo con posizione + segnale disponibile</text>';
        return;
    }
    const W = 800, H = 260, padL = 50, padR = 20, padT = 18, padB = 40;
    const plotW = W - padL - padR, plotH = H - padT - padB;
    const maxDist = Math.max(1, ...points.map(p => p.distance_km)) * 1.1;
    // SNR range: -20 .. +10
    const minSNR = -20, maxSNR = 10;
    const xScale = d => padL + (d / maxDist) * plotW;
    const yScale = s => padT + ((maxSNR - s) / (maxSNR - minSNR)) * plotH;
    const colorFor = s => s >= -5 ? '#22c55e' : s >= -10 ? '#eab308' : s >= -15 ? '#f97316' : '#ef4444';

    let html = '';
    // Grid lines (horizontal, every 5 dB)
    for (let s = minSNR; s <= maxSNR; s += 5) {
        const y = yScale(s);
        html += `<line x1="${padL}" y1="${y}" x2="${padL+plotW}" y2="${y}" stroke="rgba(255,255,255,0.05)"/>`;
        html += `<text x="${padL-6}" y="${y+3}" text-anchor="end" fill="#6b7394" font-size="10">${s}</text>`;
    }
    // X ticks (every ~maxDist/5 km)
    const tickStep = niceStep(maxDist / 5);
    for (let d = 0; d <= maxDist; d += tickStep) {
        const x = xScale(d);
        html += `<line x1="${x}" y1="${padT}" x2="${x}" y2="${padT+plotH}" stroke="rgba(255,255,255,0.04)"/>`;
        html += `<text x="${x}" y="${padT+plotH+14}" text-anchor="middle" fill="#6b7394" font-size="10">${d.toFixed(d<10?1:0)}</text>`;
    }
    // Axis labels
    html += `<text x="${padL-38}" y="${padT+plotH/2}" text-anchor="middle" fill="#6b7394" font-size="11" transform="rotate(-90 ${padL-38} ${padT+plotH/2})">SNR (dB)</text>`;
    html += `<text x="${padL+plotW/2}" y="${H-8}" text-anchor="middle" fill="#6b7394" font-size="11">Distance (km)</text>`;
    // Free-space path loss reference: SNR drops ~6dB per doubling of distance.
    // We draw a subtle guide line from (1km, 0dB) as a visual "expected" trend.
    // This isn't physically accurate for LoRa but gives a useful eyeball baseline.
    // Points
    points.forEach(p => {
        const cx = xScale(p.distance_km);
        const cy = yScale(Math.max(minSNR, Math.min(maxSNR, p.snr)));
        const color = colorFor(p.snr);
        const tooltip = `${p.name}\n${p.distance_km.toFixed(2)} km · ${p.snr.toFixed(1)} dB · ${p.rssi} dBm`;
        html += `<circle cx="${cx.toFixed(1)}" cy="${cy.toFixed(1)}" r="5" fill="${color}" fill-opacity="0.75" stroke="#0c0e14" stroke-width="1"><title>${esc(tooltip)}</title></circle>`;
    });
    svg.innerHTML = html;
}

// niceStep picks a round axis step (1, 2, 5, 10, 20, 50, ...).
export function niceStep(raw) {
    if (raw <= 0) return 1;
    const exp = Math.floor(Math.log10(raw));
    const base = raw / Math.pow(10, exp);
    let nice;
    if (base < 1.5) nice = 1;
    else if (base < 3) nice = 2;
    else if (base < 7) nice = 5;
    else nice = 10;
    return nice * Math.pow(10, exp);
}

// ---- Events/minute sparkline (on Total Packets stat card) ----
export async function renderEventsSparkline() {
    const svg = document.getElementById('stat-spark');
    if (!svg) return;
    try {
        const data = await api('/api/events-per-minute?window=60');
        const buckets = data.buckets || [];
        if (!buckets.length) return;
        const max = Math.max(1, ...buckets);
        const W = 100, H = 24;
        const step = W / buckets.length;
        // Area path
        let path = `M 0 ${H}`;
        buckets.forEach((v, i) => {
            const x = i * step;
            const y = H - (v / max) * (H - 2);
            path += ` L ${x.toFixed(2)} ${y.toFixed(2)}`;
        });
        path += ` L ${W} ${H} Z`;
        // Line path (same points, no close)
        let line = '';
        buckets.forEach((v, i) => {
            const x = i * step;
            const y = H - (v / max) * (H - 2);
            line += (i === 0 ? 'M' : ' L') + ` ${x.toFixed(2)} ${y.toFixed(2)}`;
        });
        const total = buckets.reduce((a,b)=>a+b, 0);
        const avg = (total / buckets.length).toFixed(1);
        svg.innerHTML = `
            <defs><linearGradient id="sparkGrad" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%"   stop-color="var(--purple)" stop-opacity="0.55"/>
                <stop offset="100%" stop-color="var(--purple)" stop-opacity="0.0"/>
            </linearGradient></defs>
            <path d="${path}" fill="url(#sparkGrad)" />
            <path d="${line}" fill="none" stroke="var(--purple)" stroke-width="1.2" />`;
        svg.setAttribute('title', `~${avg} evt/min over last 60 min (total ${total})`);
    } catch (e) {
        // silently ignore
    }
}

export function renderRadioHealth() {
    const card = document.getElementById('radio-health-card');
    if (!card) return;
    const rh = state.radio;
    if (!rh || !rh.enabled) {
        card.style.display = 'none';
        return;
    }
    card.style.display = '';

    // ---- Duplicate-rate alert ----
    const alertBadge = document.getElementById('radio-dup-alert');
    if (alertBadge) {
        if (rh.dup_rate_5min > 0.6 && rh.rx_last_5min >= 10) {
            alertBadge.style.display = '';
            alertBadge.textContent = `DUP RATE ${(rh.dup_rate_5min * 100).toFixed(0)}% (5min)`;
        } else {
            alertBadge.style.display = 'none';
        }
    }

    // ---- Summary strip ----
    const dupPct = (rh.dup_rate * 100).toFixed(1);
    const dup5Pct = (rh.dup_rate_5min * 100).toFixed(1);
    const winMin = Math.max(1, Math.round((rh.window_secs || 0) / 60));
    const rate = rh.raw_rx_total > 0 ? (rh.raw_rx_total / Math.max(winMin, 1)).toFixed(1) : '0';
    const summary = document.getElementById('radio-health-summary');
    summary.innerHTML = `
        <div class="radio-stat"><span class="rs-val">${fmtNum(rh.raw_rx_total)}</span><span class="rs-lbl">raw RX<br><span class="rs-sub">${rate}/min avg</span></span></div>
        <div class="radio-stat"><span class="rs-val">${fmtNum(rh.raw_dup_total)}</span><span class="rs-lbl">dupes<br><span class="rs-sub">${dupPct}% of RX</span></span></div>
        <div class="radio-stat"><span class="rs-val">${dup5Pct}%</span><span class="rs-lbl">dup rate<br><span class="rs-sub">last 5 min (${rh.rx_last_5min} RX)</span></span></div>
        <div class="radio-stat"><span class="rs-val">${fmtNum(rh.raw_mqtt_total)}</span><span class="rs-lbl">via MQTT<br><span class="rs-sub">from internet</span></span></div>
        <div class="radio-stat"><span class="rs-val">${fmtNum(winMin)}m</span><span class="rs-lbl">window<br><span class="rs-sub">since enable</span></span></div>
    `;

    // ---- Per-sender best-relay ----
    const sendersDiv = document.getElementById('radio-senders');
    if (!rh.senders || rh.senders.length === 0) {
        sendersDiv.innerHTML = '<div class="radio-empty">No data</div>';
    } else {
        const max = rh.senders[0].count;
        let h = '';
        for (const s of rh.senders) {
            const label = s.name || nodeName(s.node_id) || s.node_id;
            const pct = max > 0 ? (s.count / max * 100) : 0;
            const bestRelay = resolveRelayDisplay(s.best_relay);
            // tiny SNR color: >-6 green, >-10 yellow, else red
            const snrCol = s.best_snr > -6 ? 'var(--green)' : (s.best_snr > -10 ? 'var(--yellow)' : 'var(--red,#d75f5f)');
            let viaHtml = '';
            if (s.via_relays && s.via_relays.length > 1) {
                viaHtml = '<div class="sender-via">via ' +
                    s.via_relays.map(v => `<span class="via-relay">${esc(resolveRelayDisplay(v.relay))}<span class="via-snr">${v.best_snr.toFixed(1)}</span></span>`).join(' ') +
                    '</div>';
            }
            h += `<div class="sender-row">
                <div class="sender-head">
                    <span class="sender-name">${esc(label)}</span>
                    <span class="sender-count">${s.count}</span>
                </div>
                <div class="sender-bar-track"><div class="sender-bar-fill" style="width:${pct}%"></div></div>
                <div class="sender-meta">
                    best via <b>${esc(bestRelay)}</b> ·
                    <span style="color:${snrCol}">SNR ${s.best_snr.toFixed(1)}</span> ·
                    RSSI ${s.best_rssi}
                </div>
                ${viaHtml}
            </div>`;
        }
        sendersDiv.innerHTML = h;
    }

    // ---- Raw relay ranking ----
    const rawDiv = document.getElementById('radio-raw-relays');
    if (!rh.raw_relays || rh.raw_relays.length === 0) {
        rawDiv.innerHTML = '<div class="radio-empty">No data</div>';
    } else {
        const max = rh.raw_relays[0].count;
        let h = '';
        for (const r of rh.raw_relays) {
            const pct = max > 0 ? (r.count / max * 100) : 0;
            const name = r.name || r.node_id;
            h += `<div class="relay-row">
                <div class="relay-name">${esc(name)}</div>
                <div class="relay-bar-track"><div class="relay-bar-fill" style="width:${pct}%"></div></div>
                <div class="relay-count">${r.count}</div>
            </div>`;
        }
        rawDiv.innerHTML = h;
    }

    // ---- Hops used histogram ----
    const hopsDiv = document.getElementById('radio-hops');
    const hops = rh.hop_used || {};
    const hopKeys = Object.keys(hops).sort((a, b) => {
        if (a === '?') return 1;
        if (b === '?') return -1;
        return parseInt(a) - parseInt(b);
    });
    if (hopKeys.length === 0) {
        hopsDiv.innerHTML = '<div class="radio-empty">No data</div>';
    } else {
        const maxH = Math.max(...Object.values(hops));
        hopsDiv.innerHTML = hopKeys.map(k => {
            const pct = maxH > 0 ? (hops[k] / maxH * 100) : 0;
            return `<div class="histo-row">
                <div class="histo-label">${k} hop${k === '1' ? '' : 's'}</div>
                <div class="histo-bar-track"><div class="histo-bar-fill" style="width:${pct}%"></div></div>
                <div class="histo-count">${hops[k]}</div>
            </div>`;
        }).join('');
    }

    // ---- Max-hop histogram (HopStart = TTL set at sender) ----
    const mhDiv = document.getElementById('radio-max-hop');
    if (mhDiv) {
        const mh = rh.max_hop || {};
        const mhKeys = Object.keys(mh).sort((a, b) => {
            if (a === '?') return 1;
            if (b === '?') return -1;
            return parseInt(a) - parseInt(b);
        });
        if (mhKeys.length === 0) {
            mhDiv.innerHTML = '<div class="radio-empty">No data</div>';
        } else {
            const maxM = Math.max(...Object.values(mh));
            const total = Object.values(mh).reduce((a, b) => a + b, 0);
            mhDiv.innerHTML = mhKeys.map(k => {
                const pct = maxM > 0 ? (mh[k] / maxM * 100) : 0;
                const share = total > 0 ? (mh[k] / total * 100).toFixed(1) : '0';
                return `<div class="histo-row" title="${mh[k]} packets with hop_start=${k} (${share}%)">
                    <div class="histo-label">max ${k}</div>
                    <div class="histo-bar-track"><div class="histo-bar-fill" style="width:${pct}%"></div></div>
                    <div class="histo-count">${mh[k]}</div>
                </div>`;
            }).join('');
        }
    }

    // ---- Channels heard ----
    const chDiv = document.getElementById('radio-channels');
    const channels = rh.channel_hashes || {};
    const chKeys = Object.keys(channels).sort((a, b) => channels[b] - channels[a]);
    if (chKeys.length === 0) {
        chDiv.innerHTML = '<div class="radio-empty">No data</div>';
    } else {
        const maxC = channels[chKeys[0]];
        chDiv.innerHTML = chKeys.map(k => {
            const pct = maxC > 0 ? (channels[k] / maxC * 100) : 0;
            return `<div class="histo-row">
                <div class="histo-label">${k}</div>
                <div class="histo-bar-track"><div class="histo-bar-fill" style="width:${pct}%"></div></div>
                <div class="histo-count">${channels[k]}</div>
            </div>`;
        }).join('');
    }

    // ---- Router candidates ----
    const rcDiv = document.getElementById('radio-router-candidates');
    const rcs = rh.router_candidates || [];
    if (rcs.length === 0) {
        rcDiv.innerHTML = '<div class="radio-empty">No data</div>';
    } else {
        const maxBest = Math.max(...rcs.map(r => r.best_for_n));
        rcDiv.innerHTML = rcs.map(r => {
            const pct = maxBest > 0 ? (r.best_for_n / maxBest * 100) : 0;
            const label = r.name || resolveRelayDisplay(r.relay);
            const adv = r.snr_advantage > 0 ? `+${r.snr_advantage.toFixed(1)} dB` : `${r.snr_advantage.toFixed(1)} dB`;
            return `<div class="rc-row">
                <div class="rc-head">
                    <span class="rc-name">${esc(label)}</span>
                    <span class="rc-best">best for <b>${r.best_for_n}</b> senders</span>
                </div>
                <div class="rc-bar-track"><div class="rc-bar-fill" style="width:${pct}%"></div></div>
                <div class="rc-meta">
                    avg SNR <b>${r.avg_best_snr.toFixed(1)}</b> ·
                    advantage vs 2nd <b>${adv}</b> ·
                    ${r.total_pkts} pkts carried
                </div>
            </div>`;
        }).join('');
    }

    // ---- Frequency offset ----
    const freqDiv = document.getElementById('radio-freq');
    const freqSummary = document.getElementById('radio-freq-summary');
    const fo = rh.freq_offset;
    if (!fo || !fo.count) {
        freqDiv.innerHTML = '<div class="radio-empty">No data</div>';
        freqSummary.textContent = '';
    } else {
        freqSummary.textContent = `· mean ${fo.mean_hz.toFixed(0)} Hz · σ ${fo.stddev_hz.toFixed(0)} Hz · [${fo.min_hz.toFixed(0)}, ${fo.max_hz.toFixed(0)}] (${fo.count} samples)`;
        const hist = fo.histogram || {};
        // Keys look like "+0..+100", "-200..-100" — sort numerically by
        // the first number, otherwise JSON alphabetical order mixes
        // positive and negative buckets.
        const keys = Object.keys(hist).sort((a, b) => parseInt(a) - parseInt(b));
        if (keys.length === 0) {
            freqDiv.innerHTML = '<div class="radio-empty">No histogram</div>';
        } else {
            const max = Math.max(...Object.values(hist));
            freqDiv.innerHTML = keys.map(k => {
                const pct = max > 0 ? (hist[k] / max * 100) : 0;
                return `<div class="histo-row">
                    <div class="histo-label" style="width:80px">${k} Hz</div>
                    <div class="histo-bar-track"><div class="histo-bar-fill" style="width:${pct}%;background:var(--yellow,#dcdcaa)"></div></div>
                    <div class="histo-count">${hist[k]}</div>
                </div>`;
            }).join('');
        }
    }

    // ---- History sparkline (asynchronous) ----
    renderRadioHistory();
}

export async function renderRadioHistory() {
    const wrap = document.getElementById('radio-history-wrap');
    if (!wrap) return;
    let rows;
    try {
        rows = await api('/api/radio-health/history?limit=144');
    } catch {
        rows = null;
    }
    if (!rows || rows.length < 2) {
        wrap.style.display = 'none';
        return;
    }
    wrap.style.display = '';
    const svg = document.getElementById('radio-history-chart');
    const W = 600, H = 80;
    const rxs = rows.map(r => r.rx_last_5min || 0);
    const dupRates = rows.map(r => (r.dup_rate_5min || 0) * 100);
    const maxRx = Math.max(1, ...rxs);
    const maxDup = 100;
    const toX = i => (i / Math.max(1, rows.length - 1)) * W;
    const toYRx = v => H - (v / maxRx) * (H - 4) - 2;
    const toYDup = v => H - (v / maxDup) * (H - 4) - 2;
    const pathRx = rxs.map((v, i) => `${i === 0 ? 'M' : 'L'}${toX(i).toFixed(1)},${toYRx(v).toFixed(1)}`).join(' ');
    const pathDup = dupRates.map((v, i) => `${i === 0 ? 'M' : 'L'}${toX(i).toFixed(1)},${toYDup(v).toFixed(1)}`).join(' ');
    svg.innerHTML = `
        <line x1="0" y1="${H - 2}" x2="${W}" y2="${H - 2}" stroke="rgba(255,255,255,0.06)"/>
        <line x1="0" y1="2" x2="${W}" y2="2" stroke="rgba(255,255,255,0.04)" stroke-dasharray="2,2"/>
        <path d="${pathRx}" stroke="var(--cyan,#4ec9b0)" stroke-width="1.5" fill="none"/>
        <path d="${pathDup}" stroke="var(--red,#d75f5f)" stroke-width="1.5" fill="none" opacity="0.8"/>
        <text x="4" y="12" fill="var(--text-muted,#888)" font-size="10">peak RX ${maxRx}</text>
    `;
}

// Resolve a relay display string. If it's "!xxxxxxxx", look up a friendly name.
export function resolveRelayDisplay(s) {
    if (!s) return '';
    if (s.startsWith('!')) {
        const num = parseInt(s.slice(1), 16);
        const n = state.nodes[num];
        if (n && (n.short_name || n.long_name)) return n.short_name || n.long_name;
    }
    return s;
}

export function renderHopStats() {
    const container = document.getElementById('hop-stats-container');
    if (!container) return;
    const hs = state.stats.hop_stats_by_type;
    if (!hs || Object.keys(hs).length === 0) {
        container.innerHTML = '<div style="color:var(--text-muted);font-size:0.8rem;padding:0.5rem 0">No hop data yet</div>';
        return;
    }

    const entries = Object.entries(hs).sort((a, b) => b[1].count - a[1].count);
    // Find max hop_start across all types for bar scaling
    const maxStart = Math.max(...entries.map(([, h]) => h.max_hop_start || 7), 7);

    let html = '';
    for (const [type, h] of entries) {
        const color = getTypeColor(type);
        const avgTraveled = h.avg_hops_traveled || 0;
        const avgRemaining = h.avg_hop_limit || 0;
        const avgStart = h.avg_hop_start || 0;
        // Bar: traveled portion vs total start
        const barPct = avgStart > 0 ? (avgTraveled / avgStart * 100) : 0;
        const totalPct = avgStart > 0 ? (avgStart / maxStart * 100) : 0;

        // Color the bar based on how many hops traveled
        let barColor = 'var(--green)';
        if (avgTraveled >= 3) barColor = 'var(--orange)';
        else if (avgTraveled >= 1.5) barColor = 'var(--yellow)';

        html += `<div class="hop-row">
            <div><span class="badge badge-${type}">${shortTypeName(type)}</span></div>
            <div class="hop-row-count">${fmtNum(h.count)}</div>
            <div class="hop-bar-track">
                <div class="hop-bar-fill" style="width:${totalPct}%;background:rgba(255,255,255,0.06);position:absolute;"></div>
                <div class="hop-bar-fill" style="width:${Math.min(barPct * totalPct / 100, totalPct)}%;background:${barColor};opacity:0.7;position:relative;z-index:1;"></div>
                <div class="hop-bar-labels">
                    <span>${avgTraveled.toFixed(1)} hops used</span>
                    <span class="hbl-dim">${avgRemaining.toFixed(1)} left / ${avgStart.toFixed(0)} max</span>
                </div>
            </div>
            <div class="hop-traveled-val" style="color:${barColor}">${avgTraveled.toFixed(1)}</div>
        </div>`;
    }
    container.innerHTML = html;
}

export function renderRelayStats() {
    const container = document.getElementById('relay-stats-container');
    if (!container) return;
    const rs = state.stats.relay_stats;
    if (!rs || rs.length === 0) {
        container.innerHTML = '<div style="color:var(--text-muted);font-size:0.8rem;padding:0.5rem 0">No relay data yet</div>';
        return;
    }

    const maxCount = rs[0].count; // already sorted desc
    let html = '';
    for (const r of rs) {
        const pct = maxCount > 0 ? (r.count / maxCount * 100) : 0;
        const displayName = r.name || r.node_id;
        let ambigHtml = '';
        if (r.candidates && r.candidates.length > 1) {
            const names = r.candidates.map(id => nodeName(id)).join(', ');
            ambigHtml = ` <span class="relay-tag ambiguous" title="${esc(names)}">${r.candidates.length}?</span>`;
        }
        // Per-relay "what does it mostly forward?" — top event types with
        // their counts. Re-uses the same pkt-badge styling as the node
        // rows so colors are consistent across the dashboard.
        let typesHtml = '';
        if (r.top_types && r.top_types.length) {
            typesHtml = '<div class="relay-types">' + r.top_types.map(t => {
                const color = getTypeColor(t.type);
                const label = shortTypeName(t.type);
                return `<span class="pkt-badge" style="background:${color}20;color:${color}" title="${esc(t.type)}: ${t.count}">${esc(label)}<span class="pkt-count">${fmtNum(t.count)}</span></span>`;
            }).join('') + '</div>';
        }
        html += `<div class="relay-block">
            <div class="relay-row">
                <div class="relay-name">${esc(displayName)}${ambigHtml}</div>
                <div class="relay-bar-track">
                    <div class="relay-bar-fill" style="width:${pct}%"></div>
                </div>
                <div class="relay-count">${fmtNum(r.count)}</div>
            </div>
            ${typesHtml}
        </div>`;
    }
    container.innerHTML = html;
}

export function countActive() {
    const now = Math.floor(Date.now() / 1000);
    return Object.values(state.nodes).filter(n => now - n.last_heard < 1800).length;
}

export function renderTypesChart() {
    const pbt = state.stats.packets_by_type || {};
    // Sort by count descending
    const sorted = Object.entries(pbt).sort((a, b) => b[1] - a[1]);
    const labels = sorted.map(e => shortTypeName(e[0]));
    const data = sorted.map(e => e[1]);
    const colors = sorted.map(e => getTypeColor(e[0]));

    if (state.charts.types) {
        state.charts.types.data.labels = labels;
        state.charts.types.data.datasets[0].data = data;
        state.charts.types.data.datasets[0].backgroundColor = colors;
        state.charts.types.update();
        return;
    }
    const ctx = document.getElementById('chart-types');
    if (!ctx) return;
    state.charts.types = new Chart(ctx, {
        type: 'doughnut',
        data: {
            labels,
            datasets: [{
                data,
                backgroundColor: colors,
                borderWidth: 0,
                hoverOffset: 6,
            }]
        },
        options: {
            responsive: true,
            cutout: '62%',
            plugins: {
                legend: {
                    position: 'right',
                    labels: {
                        color: '#6b7394',
                        font: { size: 11, weight: '500' },
                        padding: 8,
                        usePointStyle: true,
                        pointStyleWidth: 8,
                    }
                },
                tooltip: {
                    backgroundColor: '#181c28',
                    titleColor: '#d8dce6',
                    bodyColor: '#d8dce6',
                    borderColor: '#2a3050',
                    borderWidth: 1,
                    cornerRadius: 6,
                    padding: 8,
                }
            }
        }
    });
}

export function makeEventRow(ev) {
    const tr = document.createElement('tr');
    const hopHtml = makeHopPill(ev);
    const relayHtml = makeRelayTag(ev);
    // Make rows clickable when we can resolve the source: opens the path-tracing modal.
    if (ev.from_num) {
        tr.classList.add('event-row-clickable');
        tr.title = 'Click to inspect packet path';
        tr.addEventListener('click', () => openPacketPath(ev.from_num, ev.packet_id || 0));
    }
    tr.innerHTML = `
        <td style="white-space:nowrap;color:var(--text-dim);font-variant-numeric:tabular-nums">${fmtTime(ev.time)}</td>
        <td><span class="badge badge-${ev.type}">${shortTypeName(ev.type)}</span></td>
        <td style="font-weight:500">${nodeName(ev.from)}</td>
        <td>${eventInfo(ev)}${hopHtml}${relayHtml}</td>`;
    return tr;
}

// ---- Channel Utilization ----
export async function renderChannelUtil() {
    const strip = document.getElementById('diag-strip');
    if (!strip) return;
    let cu;
    try { cu = await api('/api/channel-util'); } catch { return; }
    if (!cu || !cu.nodes_reporting) { strip.style.display = 'none'; return; }
    strip.style.display = '';
    document.getElementById('cu-avg').textContent = cu.avg_chan_util.toFixed(1);
    document.getElementById('cu-max').textContent = cu.max_chan_util.toFixed(1);
    document.getElementById('cu-air-avg').textContent = cu.avg_air_util.toFixed(1);
    document.getElementById('cu-nodes').textContent = cu.nodes_reporting;
    const talkerName = cu.top_talker_name || `!${(cu.top_talker_num||0).toString(16).padStart(8,'0')}`;
    document.getElementById('cu-talker').textContent = talkerName;
    document.getElementById('cu-talker-val').textContent = `air ${cu.top_talker_util.toFixed(1)}%`;
    // Alert
    const alert = document.getElementById('chan-alert');
    if (alert) alert.style.display = cu.congested ? '' : 'none';
    // History chart
    renderChanHistory();
}

export async function renderChanHistory() {
    const wrap = document.getElementById('cu-history-wrap');
    if (!wrap) return;
    let rows;
    try { rows = await api('/api/channel-util/history?limit=144'); } catch { return; }
    if (!rows || rows.length < 2) { wrap.style.display = 'none'; return; }
    wrap.style.display = '';
    const svg = document.getElementById('cu-history-chart');
    const W = 600, H = 60;
    const avgs = rows.map(r => r.avg_chan_util || 0);
    const maxs = rows.map(r => r.max_chan_util || 0);
    const maxVal = Math.max(1, ...maxs, 30); // at least 30% scale
    const toX = i => (i / Math.max(1, rows.length - 1)) * W;
    const toY = v => H - (v / maxVal) * (H - 4) - 2;
    const pAvg = avgs.map((v,i) => `${i?'L':'M'}${toX(i).toFixed(1)},${toY(v).toFixed(1)}`).join(' ');
    const pMax = maxs.map((v,i) => `${i?'L':'M'}${toX(i).toFixed(1)},${toY(v).toFixed(1)}`).join(' ');
    // Threshold line at 25%
    const thY = toY(25);
    svg.innerHTML = `
        <line x1="0" y1="${thY}" x2="${W}" y2="${thY}" stroke="rgba(249,115,22,0.3)" stroke-dasharray="4,3"/>
        <text x="${W-40}" y="${thY-3}" fill="rgba(249,115,22,0.5)" font-size="8">25%</text>
        <path d="${pMax}" stroke="var(--red,#d75f5f)" stroke-width="1" fill="none" opacity="0.6"/>
        <path d="${pAvg}" stroke="var(--orange,#f97316)" stroke-width="1.5" fill="none"/>
    `;
}

// ---- Availability Events ----
export async function renderAvailEvents() {
    const div = document.getElementById('avail-events');
    if (!div) return;
    let events;
    try { events = await api('/api/availability?limit=30'); } catch { return; }
    if (!events || events.length === 0) {
        div.innerHTML = '<div class="radio-empty">No transitions recorded yet</div>';
        return;
    }
    // Show most recent first
    const recent = events.slice(-30).reverse();
    div.innerHTML = recent.map(e => {
        const t = new Date(e.time * 1000);
        const ts = t.toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'});
        const date = t.toLocaleDateString([], {month:'short', day:'numeric'});
        const nodeId = `!${(e.node_num||0).toString(16).padStart(8,'0')}`;
        const name = nodeName(nodeId) || nodeId;
        const cls = e.event === 'online' ? 'avail-on' : 'avail-off';
        const icon = e.event === 'online' ? '&#9650;' : '&#9660;';
        return `<div class="avail-ev ${cls}">
            <span class="avail-icon">${icon}</span>
            <span class="avail-name">${esc(name)}</span>
            <span class="avail-ts">${date} ${ts}</span>
        </div>`;
    }).join('');
}

// ---- Signal Sparkline for Nodes table ----
export async function loadSignalSparkline(nodeNum, cell) {
    const hex = nodeNum.toString(16).padStart(8, '0');
    let samples;
    try { samples = await api(`/api/signal/${hex}?limit=50`); } catch { return; }
    if (!samples || samples.length < 2) { cell.textContent = '-'; return; }
    const W = 80, H = 24;
    const snrs = samples.map(s => s.snr);
    const minS = Math.min(...snrs);
    const maxS = Math.max(...snrs);
    const range = Math.max(1, maxS - minS);
    const toX = i => (i / (samples.length - 1)) * W;
    const toY = v => H - 2 - ((v - minS) / range) * (H - 4);
    const path = snrs.map((v,i) => `${i?'L':'M'}${toX(i).toFixed(1)},${toY(v).toFixed(1)}`).join(' ');
    // Color based on last SNR
    const last = snrs[snrs.length - 1];
    const color = last > -6 ? 'var(--green)' : last > -10 ? 'var(--yellow)' : 'var(--red,#d75f5f)';
    cell.innerHTML = `<svg viewBox="0 0 ${W} ${H}" style="width:${W}px;height:${H}px">
        <path d="${path}" stroke="${color}" stroke-width="1.5" fill="none"/>
    </svg><div class="spark-label">${last.toFixed(1)} dB</div>`;
}
