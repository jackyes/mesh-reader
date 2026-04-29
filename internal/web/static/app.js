// Mesh Reader Dashboard v2 — vanilla JS
(function () {
    'use strict';

    // ---- State ----
    const state = {
        nodes: {},        // nodeNum -> node
        messages: [],
        stats: {},
        traceroutes: [],
        ws: null,
        map: null,
        networkMap: null,
        markers: {},      // nodeNum -> L.marker
        connLines: [],    // L.polyline[]
        charts: {},
        activeTab: 'overview',
        selectedNode: null,
        autoRefreshTimer: null,
        autoRefreshCountdown: 15,
        autoRefreshCountdownTimer: null,
    };

    const AUTO_REFRESH_INTERVAL = 15; // seconds

    // ---- Tab navigation ----
    document.querySelectorAll('.tab').forEach(btn => {
        btn.addEventListener('click', () => {
            document.querySelectorAll('.tab').forEach(b => b.classList.remove('active'));
            document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
            btn.classList.add('active');
            const tab = btn.dataset.tab;
            document.getElementById(tab).classList.add('active');
            state.activeTab = tab;
            if (tab === 'map') initMap();
            if (tab === 'nodes') renderNodesTable();
            if (tab === 'network') {
                // Re-fetch traceroutes every time the tab is opened — they accumulate
                // server-side from incoming TRACEROUTE events but the dashboard is
                // pull-only, so without this they would only appear after a manual
                // Refresh (or app restart, which reloads them from the DB).
                refreshTraceroutes().then(() => {
                    initNetworkMap();
                    renderSNRDistanceChart();
                });
            }
            if (tab === 'telemetry') initCharts();
            if (tab === 'local-node') renderLocalNode();
            if (tab === 'misbehaving') renderMisbehaving();
        });
    });

    // ---- Initial load ----
    async function init() {
        try {
            const [stats, nodes, messages, traceroutes, events, radio] = await Promise.all([
                api('/api/stats'),
                api('/api/nodes'),
                api('/api/messages?limit=200'),
                api('/api/traceroutes'),
                api('/api/events?limit=30'),
                api('/api/radio-health').catch(() => null),
            ]);
            state.radio = radio;
            state.stats = stats;
            state.traceroutes = traceroutes || [];
            (nodes || []).forEach(n => state.nodes[n.node_num] = n);
            state.messages = messages || [];
            renderOverview(events || []);
            renderMessages();
            populateNodeSelect();
        } catch (e) {
            console.error('init failed:', e);
        }
        document.getElementById('status').textContent = 'OK';
        document.getElementById('status').className = 'connected';
    }

    async function api(path) {
        const r = await fetch(path);
        return r.json();
    }

    // refreshTraceroutes pulls the latest traceroute list and re-renders the
    // Network sidebar. Used on Network-tab activation so newly-arrived TRs
    // (including those triggered via the on-demand TR button) show up without
    // requiring a full Refresh.
    async function refreshTraceroutes() {
        try {
            const trs = await api('/api/traceroutes');
            state.traceroutes = trs || [];
            renderTracerouteList();
        } catch (e) {
            console.error('refreshTraceroutes:', e);
        }
    }

    // ---- Manual refresh ----
    async function refreshAll() {
        const btn = document.getElementById('refresh-btn');
        if (btn) { btn.disabled = true; btn.textContent = '...'; }
        try {
            const [stats, nodes, messages, traceroutes, events, radio] = await Promise.all([
                api('/api/stats'),
                api('/api/nodes'),
                api('/api/messages?limit=200'),
                api('/api/traceroutes'),
                api('/api/events?limit=30'),
                api('/api/radio-health').catch(() => null),
            ]);
            state.radio = radio;
            state.stats = stats;
            state.traceroutes = traceroutes || [];
            state.nodes = {};
            (nodes || []).forEach(n => state.nodes[n.node_num] = n);
            state.messages = messages || [];
            renderOverview(events || []);
            renderMessages();
            populateNodeSelect();
            if (state.activeTab === 'nodes') renderNodesTable();
            if (state.activeTab === 'telemetry') loadTelemetryData();
            if (state.activeTab === 'network') renderTracerouteList();
            if (state.activeTab === 'map') refreshChUtilLayer();
            if (state.activeTab === 'local-node') renderLocalNode();
            if (state.activeTab === 'misbehaving') renderMisbehaving();
            document.getElementById('status').textContent = 'OK';
            document.getElementById('status').className = 'connected';
        } catch (e) {
            console.error('refresh failed:', e);
            document.getElementById('status').textContent = 'Error';
            document.getElementById('status').className = 'disconnected';
        }
        if (btn) { btn.disabled = false; btn.textContent = 'Refresh'; }
    }

    document.getElementById('refresh-btn')?.addEventListener('click', refreshAll);

    // ---- Auto-refresh toggle ----
    function setAutoRefreshLabel(on) {
        const label = document.getElementById('auto-refresh-state');
        const btn = document.getElementById('auto-refresh-btn');
        if (!label || !btn) return;
        if (on) {
            label.textContent = `ON (${state.autoRefreshCountdown}s)`;
            btn.classList.add('active');
        } else {
            label.textContent = 'OFF';
            btn.classList.remove('active');
        }
    }

    function stopAutoRefresh() {
        if (state.autoRefreshTimer) {
            clearInterval(state.autoRefreshTimer);
            state.autoRefreshTimer = null;
        }
        if (state.autoRefreshCountdownTimer) {
            clearInterval(state.autoRefreshCountdownTimer);
            state.autoRefreshCountdownTimer = null;
        }
        setAutoRefreshLabel(false);
        try { localStorage.setItem('auto-refresh', '0'); } catch (e) { /* ignore */ }
    }

    function startAutoRefresh() {
        stopAutoRefresh();
        state.autoRefreshCountdown = AUTO_REFRESH_INTERVAL;
        setAutoRefreshLabel(true);
        state.autoRefreshTimer = setInterval(() => {
            refreshAll();
            state.autoRefreshCountdown = AUTO_REFRESH_INTERVAL;
            setAutoRefreshLabel(true);
        }, AUTO_REFRESH_INTERVAL * 1000);
        state.autoRefreshCountdownTimer = setInterval(() => {
            state.autoRefreshCountdown = Math.max(0, state.autoRefreshCountdown - 1);
            setAutoRefreshLabel(true);
        }, 1000);
        try { localStorage.setItem('auto-refresh', '1'); } catch (e) { /* ignore */ }
    }

    function toggleAutoRefresh() {
        if (state.autoRefreshTimer) {
            stopAutoRefresh();
        } else {
            startAutoRefresh();
        }
    }

    document.getElementById('auto-refresh-btn')?.addEventListener('click', toggleAutoRefresh);

    // Restore last state from localStorage (default OFF)
    try {
        if (localStorage.getItem('auto-refresh') === '1') {
            // delay to let the init pass complete first
            setTimeout(startAutoRefresh, 500);
        }
    } catch (e) { /* ignore */ }

    // ---- Overview ----
    function renderOverview(events) {
        updateStatsCards();
        renderTypesChart();
        const tbody = document.querySelector('#recent-events tbody');
        tbody.innerHTML = '';
        (events || []).forEach(ev => tbody.appendChild(makeEventRow(ev)));
    }

    function updateStatsCards() {
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

    // ---- Anomalies (GPS teleport / spammer / SNR jump) ----
    async function renderAnomalies() {
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
    // Mode drives both which value each cell displays and the color ramp used:
    //   volume : sequential (dark → cyan → purple → red), key = cell.count
    //   nodes  : sequential (dark → green → violet),      key = cell.unique_nodes
    //   snr    : divergent  (red → amber → green),        key = cell.avg_snr
    //            (SNR is signed; we map min→red, max→green so "bad hours" stand out)
    //
    // Rich tooltip (HTML panel) on hover — not the native SVG <title> — so we
    // can show count + unique nodes + dominant type + avg SNR in one place.
    // Click → opens drill-down modal with top nodes / type breakdown / samples.
    let heatmapCells = [];   // last payload, so click handler can reuse data
    let heatmapMax = {volume:0, nodes:0, snrMin:0, snrMax:0};
    const heatmapDayLabels = ['Mon','Tue','Wed','Thu','Fri','Sat','Sun'];

    async function renderHeatmapTemporal() {
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
    function heatmapCellValue(c, mode) {
        if (!c) return 0;
        if (mode === 'nodes') return c.unique_nodes || 0;
        if (mode === 'snr')   return c.avg_snr || 0;
        return c.count || 0;
    }
    // --- mode → color for a raw cell ---
    function heatmapCellColor(c, mode) {
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
    function heatmapRampColor(t, mode) {
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
    function heatmapLegend(mode) {
        if (mode === 'nodes') return {left:'1 node', right:`${heatmapMax.nodes} nodes`};
        if (mode === 'snr')   return {
            left: heatmapMax.snrAny ? `${heatmapMax.snrMin.toFixed(1)} dB (worst)` : 'no SNR',
            right: heatmapMax.snrAny ? `${heatmapMax.snrMax.toFixed(1)} dB (best)` : ''
        };
        return {left:'less', right:`max ${heatmapMax.volume}`};
    }

    // --- rich HTML tooltip ---
    function onHeatmapCellHover(e) {
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
    function onHeatmapCellMove(e) {
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
    function onHeatmapCellLeave() {
        const tip = document.getElementById('heatmap-tooltip');
        if (tip) tip.style.display = 'none';
    }

    // --- drill-down modal ---
    async function onHeatmapCellClick(e) {
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
    document.getElementById('heatmap-modal-close')?.addEventListener('click', () => {
        document.getElementById('heatmap-modal').style.display = 'none';
    });
    document.getElementById('heatmap-modal')?.addEventListener('click', (e) => {
        if (e.target.id === 'heatmap-modal') e.target.style.display = 'none';
    });
    document.getElementById('heatmap-days')?.addEventListener('change', renderHeatmapTemporal);
    document.getElementById('heatmap-mode')?.addEventListener('change', renderHeatmapTemporal);

    // ---- DX leaderboard ----
    async function renderDXLeaderboard() {
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
    document.getElementById('dx-direct-only')?.addEventListener('change', renderDXLeaderboard);

    // ---- Path tracing modal ----
    async function openPacketPath(fromHexOrNum, packetID) {
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
    document.getElementById('path-modal-close')?.addEventListener('click', () => {
        document.getElementById('path-modal').style.display = 'none';
    });
    document.getElementById('path-modal')?.addEventListener('click', (e) => {
        if (e.target.id === 'path-modal') e.target.style.display = 'none';
    });

    // ---- Node detail modal (Nodes table → click on the name) ----
    // Reuses nodePopup() so the modal layout stays consistent with the map
    // popup (and any future enrichment shows up in both places at once).
    function openNodeModal(nodeNum) {
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
        body.innerHTML = nodePopup(n);
        modal.style.display = '';
    }
    document.getElementById('node-modal-close')?.addEventListener('click', () => {
        document.getElementById('node-modal').style.display = 'none';
    });
    document.getElementById('node-modal')?.addEventListener('click', (e) => {
        if (e.target.id === 'node-modal') e.target.style.display = 'none';
    });
    // Delegated handler: click on a .node-name-link inside the Nodes table.
    document.querySelector('#nodes-table tbody')?.addEventListener('click', (e) => {
        const link = e.target.closest('.node-name-link');
        if (!link) return;
        e.preventDefault();
        const num = parseInt(link.dataset.nodeNum, 10);
        if (num) openNodeModal(num);
    });

    // ---- On-demand traceroute (button on Nodes table) ----
    async function sendTracerouteToNode(nodeNum, btn) {
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
    // Event delegation for TR buttons (rows are recreated on each render)
    document.addEventListener('click', (e) => {
        const btn = e.target.closest('.btn-tr');
        if (btn) {
            const num = parseInt(btn.dataset.nodeNum, 10);
            if (num) sendTracerouteToNode(num, btn);
        }
    });

    // ---- Signal degradation trends ----
    async function renderSignalTrends() {
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
    function buildTrendSparkline(r) {
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

    document.getElementById('trend-window')?.addEventListener('change', renderSignalTrends);

    // ---- Fragile / isolated nodes ----
    async function renderIsolatedNodes() {
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
    async function renderSNRDistanceChart() {
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
    function niceStep(raw) {
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
    async function renderEventsSparkline() {
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

    function renderRadioHealth() {
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

    async function renderRadioHistory() {
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
    function resolveRelayDisplay(s) {
        if (!s) return '';
        if (s.startsWith('!')) {
            const num = parseInt(s.slice(1), 16);
            const n = state.nodes[num];
            if (n && (n.short_name || n.long_name)) return n.short_name || n.long_name;
        }
        return s;
    }

    function renderHopStats() {
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

    function renderRelayStats() {
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

    function countActive() {
        const now = Math.floor(Date.now() / 1000);
        return Object.values(state.nodes).filter(n => now - n.last_heard < 1800).length;
    }

    function renderTypesChart() {
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

    function makeEventRow(ev) {
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

    function makeHopPill(ev) {
        if (!ev.hop_start && !ev.hop_limit) return '';
        const start = ev.hop_start || 0;
        const remaining = ev.hop_limit || 0;
        const used = start >= remaining ? start - remaining : 0;
        if (start === 0 && remaining === 0) return '';
        // Format: "2↑ 1↓ /3"  — used↑  remaining↓  /max
        return ` <span class="hop-pill"><span class="hop-used">${used}</span>\u2191 <span class="hop-remaining">${remaining}</span>\u2193 /${start}</span>`;
    }

    function makeRelayTag(ev) {
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

    // ---- Channel Utilization ----
    async function renderChannelUtil() {
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

    async function renderChanHistory() {
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
    async function renderAvailEvents() {
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
    async function loadSignalSparkline(nodeNum, cell) {
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

    // ---- Misbehaving nodes page ----
    // The four config-sensitive metrics tracked here are NodeInfo, Telemetry,
    // Position (counts in window) and Max hop (mode of hop_start in window).
    // Each metric has its own count threshold + window length (minutes), and
    // can be toggled independently. The backend's sliding-window logic
    // auto-removes a node from the report once it drops back under every
    // active threshold — no client-side bookkeeping needed.
    //
    // Settings flow:
    //   - At first render we GET /api/misbehaving/config (server-active config,
    //     which is the user's persisted defaults if any, else built-in).
    //   - "Apply"          → POST /api/misbehaving/config (runtime only)
    //   - "Save as default"→ POST /api/misbehaving/config?save=1 (also persists)
    //   - "Reset"          → GET /api/misbehaving/defaults, fills the form,
    //                        does NOT auto-apply (user can edit before Apply)
    //
    // NB: state.sort is reassigned later in this file (line ~1762), so we
    // ensure our slot exists at call time rather than at script load.
    function ensureMisbSort() {
        if (!state.sort) state.sort = {};
        if (!state.sort.misbehaving) state.sort.misbehaving = { key: 'excess', dir: -1 };
    }

    // Field mapping between the JSON config (server) and the four UI tiles.
    const MISB_METRICS = [
        { ui: 'node_info', enabled: 'node_info_enabled', count: 'node_info_count', win: 'node_info_window_sec' },
        { ui: 'telemetry', enabled: 'telemetry_enabled', count: 'telemetry_count', win: 'telemetry_window_sec' },
        { ui: 'position',  enabled: 'position_enabled',  count: 'position_count',  win: 'position_window_sec'  },
        { ui: 'max_hop',   enabled: 'max_hop_enabled',   count: 'max_hop_value',   win: 'max_hop_window_sec'   },
    ];

    function misbConfigToForm(cfg) {
        MISB_METRICS.forEach(m => {
            const tile = document.querySelector(`.misb-tile[data-metric="${m.ui}"]`);
            if (!tile) return;
            tile.querySelector('[data-field="enabled"]').checked   = !!cfg[m.enabled];
            tile.querySelector('[data-field="count"]').value       = cfg[m.count] ?? 0;
            tile.querySelector('[data-field="window_min"]').value  = Math.max(5, Math.round((cfg[m.win] ?? 3600) / 60));
            tile.classList.toggle('misb-tile-off', !cfg[m.enabled]);
        });
    }

    function misbFormToConfig() {
        const cfg = {};
        MISB_METRICS.forEach(m => {
            const tile = document.querySelector(`.misb-tile[data-metric="${m.ui}"]`);
            if (!tile) return;
            cfg[m.enabled] = tile.querySelector('[data-field="enabled"]').checked;
            cfg[m.count]   = Math.max(0, parseInt(tile.querySelector('[data-field="count"]').value, 10) || 0);
            const winMin   = Math.max(5, parseInt(tile.querySelector('[data-field="window_min"]').value, 10) || 60);
            cfg[m.win]     = winMin * 60;
            tile.classList.toggle('misb-tile-off', !cfg[m.enabled]);
        });
        return cfg;
    }

    function setMisbStatus(text, kind) {
        const el = document.getElementById('misb-status');
        if (!el) return;
        el.textContent = text || '';
        el.className = 'misb-status' + (kind ? ' misb-status-' + kind : '');
        if (text) {
            clearTimeout(el._t);
            el._t = setTimeout(() => { el.textContent = ''; el.className = 'misb-status'; }, 4000);
        }
    }

    async function applyMisbConfig(persist) {
        const cfg = misbFormToConfig();
        try {
            const url = '/api/misbehaving/config' + (persist ? '?save=1' : '');
            const r = await fetch(url, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(cfg),
            });
            const body = await r.json();
            if (body && body.config) misbConfigToForm(body.config);
            if (persist) {
                if (body.saved) {
                    setMisbStatus('Saved as default ✓', 'ok');
                } else {
                    setMisbStatus('Applied (save failed: ' + (body.save_error || 'unknown') + ')', 'warn');
                }
            } else {
                setMisbStatus('Applied ✓', 'ok');
            }
            await renderMisbehaving({ skipConfigFetch: true });
        } catch (e) {
            console.error('misb apply:', e);
            setMisbStatus('Error: ' + e.message, 'err');
        }
    }

    async function resetMisbForm() {
        try {
            const def = await api('/api/misbehaving/defaults');
            misbConfigToForm(def);
            setMisbStatus('Form reset to built-in defaults — click Apply to use', 'warn');
        } catch (e) {
            console.error('misb reset:', e);
            setMisbStatus('Error: ' + e.message, 'err');
        }
    }

    async function renderMisbehaving(opts) {
        opts = opts || {};
        ensureMisbSort();
        const tbody = document.querySelector('#misbehaving-table tbody');
        const card  = document.getElementById('misb-card');
        const empty = document.getElementById('misb-empty');
        const subEl = document.getElementById('misb-subtitle');
        if (!tbody || !card || !empty) return;

        // Pull the active config so the form mirrors the server (unless we
        // just POSTed it ourselves — applyMisbConfig already filled the form).
        if (!opts.skipConfigFetch) {
            try {
                const cfg = await api('/api/misbehaving/config');
                misbConfigToForm(cfg);
            } catch (e) { /* non-fatal: form keeps whatever values it has */ }
        }

        let rep;
        try {
            rep = await api('/api/misbehaving');
        } catch (e) {
            console.error('misbehaving fetch:', e);
            tbody.innerHTML = '<tr><td colspan="11" style="padding:1rem;color:var(--red)">Unable to load misbehaving nodes.</td></tr>';
            card.style.display = '';
            empty.style.display = 'none';
            return;
        }
        const cfg   = (rep && rep.config) || {};
        const nodes = (rep && rep.nodes)  || [];

        // Subtitle reflects the active window mix.
        const winsMin = [
            cfg.node_info_window_sec, cfg.telemetry_window_sec,
            cfg.position_window_sec,  cfg.max_hop_window_sec,
        ].filter(v => v).map(v => Math.round(v / 60));
        const minW = winsMin.length ? Math.min(...winsMin) : 60;
        const maxW = winsMin.length ? Math.max(...winsMin) : 60;
        if (subEl) {
            subEl.textContent = (minW === maxW)
                ? `Nodes exceeding any active threshold in the last ${minW} minutes — auto-removed once back under all of them.`
                : `Nodes exceeding any active threshold (per-metric windows ${minW}–${maxW} min) — auto-removed once back under all.`;
        }

        if (nodes.length === 0) {
            card.style.display = 'none';
            empty.style.display = '';
            tbody.innerHTML = '';
            return;
        }
        card.style.display = '';
        empty.style.display = 'none';

        const sortKey = state.sort.misbehaving.key;
        const sortDir = state.sort.misbehaving.dir;
        const sorted = nodes.slice().sort((a, b) => {
            const va = misbSortVal(a, sortKey, cfg);
            const vb = misbSortVal(b, sortKey, cfg);
            if (va < vb) return -1 * sortDir;
            if (va > vb) return  1 * sortDir;
            return 0;
        });

        const now = Math.floor(Date.now() / 1000);
        tbody.innerHTML = sorted.map(n => {
            const niBad = cfg.node_info_enabled && n.node_info_count > cfg.node_info_count;
            const teBad = cfg.telemetry_enabled && n.telemetry_count > cfg.telemetry_count;
            const poBad = cfg.position_enabled  && n.position_count  > cfg.position_count;
            const mhBad = cfg.max_hop_enabled   && n.hop_start_mode  > cfg.max_hop_value;
            const mhCell = (n.hop_start_mode === undefined || n.hop_start_mode < 0)
                ? '<span class="ln-dash">—</span>'
                : String(n.hop_start_mode);
            const issues = (n.reasons || []).map(r => `<span class="misb-issue">${esc(r)}</span>`).join(' ');
            const lh = n.last_heard ? `${Math.max(0, now - n.last_heard)}s ago` : '-';
            return `<tr>
                <td>${esc(n.long_name || '-')}</td>
                <td><span class="misb-short">${esc(n.short_name || '')}</span></td>
                <td><code class="misb-id">${esc(n.id || '')}</code></td>
                <td>${esc(n.hw_model || '')}</td>
                <td>${n.role ? roleBadge(n.role) : ''}</td>
                <td class="misb-num ${niBad ? 'misb-bad' : ''}">${n.node_info_count}</td>
                <td class="misb-num ${teBad ? 'misb-bad' : ''}">${n.telemetry_count}</td>
                <td class="misb-num ${poBad ? 'misb-bad' : ''}">${n.position_count}</td>
                <td class="misb-num ${mhBad ? 'misb-bad' : ''}">${mhCell}</td>
                <td class="misb-issues">${issues}</td>
                <td class="misb-last">${esc(lh)}</td>
            </tr>`;
        }).join('');
    }

    // misbSortVal — total "excess" = sum of (count - threshold) for each
    // breached metric. Higher = more egregious offender.
    function misbSortVal(n, key, cfg) {
        switch (key) {
            case 'name':            return (n.long_name || '').toLowerCase();
            case 'short_name':      return (n.short_name || '').toLowerCase();
            case 'node_id':         return (n.id || '').toLowerCase();
            case 'hw':              return (n.hw_model || '').toLowerCase();
            case 'role':            return roleSortWeight(n.role);
            case 'node_info_count': return n.node_info_count | 0;
            case 'telemetry_count': return n.telemetry_count | 0;
            case 'position_count':  return n.position_count  | 0;
            case 'hop_start_mode':  return n.hop_start_mode  | 0;
            case 'last_heard':      return n.last_heard | 0;
            case 'excess':
            default: {
                let x = 0;
                if (cfg.node_info_enabled && n.node_info_count > (cfg.node_info_count|0)) x += n.node_info_count - cfg.node_info_count;
                if (cfg.telemetry_enabled && n.telemetry_count > (cfg.telemetry_count|0)) x += n.telemetry_count - cfg.telemetry_count;
                if (cfg.position_enabled  && n.position_count  > (cfg.position_count |0)) x += n.position_count  - cfg.position_count;
                if (cfg.max_hop_enabled   && (n.hop_start_mode|0) > (cfg.max_hop_value|0)) x += (n.hop_start_mode|0) - cfg.max_hop_value;
                return x;
            }
        }
    }

    // Wire up sort headers for the misbehaving table.
    document.querySelectorAll('#misbehaving-table th[data-sort]').forEach(th => {
        th.addEventListener('click', () => {
            ensureMisbSort();
            const k = th.dataset.sort;
            const cur = state.sort.misbehaving;
            if (cur.key === k) {
                cur.dir *= -1;
            } else {
                cur.key = k;
                cur.dir = (th.dataset.sortType === 'num') ? -1 : 1;
            }
            renderMisbehaving({ skipConfigFetch: true });
        });
    });

    // Wire up the settings panel buttons.
    document.getElementById('misb-apply')?.addEventListener('click', () => applyMisbConfig(false));
    document.getElementById('misb-save') ?.addEventListener('click', () => applyMisbConfig(true));
    document.getElementById('misb-reset')?.addEventListener('click', () => resetMisbForm());

    // Toggling a metric off should grey out its tile so the user sees what's
    // actually being evaluated. Sync this on every checkbox change.
    document.querySelectorAll('.misb-tile [data-field="enabled"]').forEach(cb => {
        cb.addEventListener('change', () => {
            const tile = cb.closest('.misb-tile');
            if (tile) tile.classList.toggle('misb-tile-off', !cb.checked);
        });
    });

    // ---- Local node page ----
    // The "My Node" tab shows information about the Meshtastic device we're
    // physically connected to over serial. The data is assembled by the Go
    // side from several boot-time packets (MY_INFO, NODE_INFO for own id,
    // METADATA, CONFIG LoRa) into the /api/local-node endpoint. Fields can
    // appear incrementally — we render whatever is present and leave the
    // rest as "-" so the page is useful even during the first seconds.
    async function renderLocalNode() {
        const idEl   = document.getElementById('ln-identity');
        const fwEl   = document.getElementById('ln-firmware');
        const loraEl = document.getElementById('ln-lora');
        const capsEl = document.getElementById('ln-caps');
        const titleName = document.getElementById('ln-title-name');
        const titleRole = document.getElementById('ln-title-role');
        const subtitle  = document.getElementById('ln-subtitle');
        if (!idEl || !fwEl || !loraEl || !capsEl) return;

        let ln;
        try {
            ln = await api('/api/local-node');
        } catch (e) {
            console.error('local-node fetch:', e);
            idEl.innerHTML = '<div class="ln-empty">Unable to load local node info.</div>';
            fwEl.innerHTML = '';
            loraEl.innerHTML = '';
            capsEl.innerHTML = '';
            return;
        }

        if (!ln || !ln.node_num) {
            idEl.innerHTML = '<div class="ln-empty">No data yet. Waiting for the device handshake over the serial port…</div>';
            fwEl.innerHTML = '';
            loraEl.innerHTML = '';
            capsEl.innerHTML = '';
            if (titleName) titleName.textContent = 'My Node';
            if (titleRole) titleRole.innerHTML = '';
            if (subtitle) subtitle.textContent = 'Connected device on the serial port';
            return;
        }

        // Header
        if (titleName) titleName.textContent = ln.long_name || ln.node_id || 'My Node';
        if (titleRole) titleRole.innerHTML = ln.role ? roleBadge(ln.role) : '';
        if (subtitle) {
            const parts = [];
            if (ln.short_name) parts.push(`<span class="ln-sub-short">${esc(ln.short_name)}</span>`);
            if (ln.node_id)    parts.push(`<span class="ln-sub-id">${esc(ln.node_id)}</span>`);
            if (ln.hw_model)   parts.push(`<span class="ln-sub-hw">${esc(ln.hw_model)}</span>`);
            parts.push(`<span class="ln-sub-uptime">dashboard uptime ${fmtUptime(ln.uptime_seconds | 0)}</span>`);
            subtitle.innerHTML = parts.join(' <span class="ln-sub-sep">·</span> ');
        }

        // Identity
        idEl.innerHTML = kvRows([
            ['Long name',   ln.long_name],
            ['Short name',  ln.short_name],
            ['Node ID',     ln.node_id],
            ['Node num',    ln.node_num ? String(ln.node_num) : ''],
            ['Role',        ln.role ? roleBadge(ln.role) + ' ' + esc(ln.role) : '', { raw: true }],
            ['Hardware',    ln.hw_model],
            ['Seen at',     ln.seen_at ? new Date(ln.seen_at * 1000).toLocaleString('it-IT') : ''],
        ]);

        // Firmware
        fwEl.innerHTML = kvRows([
            ['Firmware',      ln.firmware_version],
            ['PlatformIO env', ln.pio_env],
            ['Reboots',       ln.reboot_count ? String(ln.reboot_count) : ''],
            ['NodeDB entries', ln.nodedb_count ? String(ln.nodedb_count) : ''],
            ['DeviceState ver', ln.device_state_version ? String(ln.device_state_version) : ''],
        ]);

        // LoRa Radio
        const loraRows = [];
        loraRows.push(['Region', ln.region]);
        if (ln.use_preset) {
            loraRows.push(['Preset', ln.modem_preset ? `${esc(ln.modem_preset)} <span class="th-hint">(preset)</span>` : '', { raw: true }]);
        } else {
            loraRows.push(['Mode', 'Custom <span class="th-hint">(use_preset=false)</span>', { raw: true }]);
        }
        if (ln.bandwidth)      loraRows.push(['Bandwidth',  `${ln.bandwidth} kHz`]);
        if (ln.spread_factor)  loraRows.push(['Spread factor', `SF${ln.spread_factor}`]);
        if (ln.coding_rate)    loraRows.push(['Coding rate', `4/${ln.coding_rate}`]);
        if (ln.channel_num !== undefined && ln.channel_num !== null) {
            loraRows.push(['Channel num', String(ln.channel_num)]);
        }
        if (ln.hop_limit) loraRows.push(['Hop limit',  `${ln.hop_limit}`]);
        if (ln.tx_power) {
            loraRows.push(['TX power',   `${ln.tx_power} dBm`]);
        }
        loraRows.push(['TX enabled', ln.tx_enabled === undefined ? '' : (ln.tx_enabled ? 'yes' : '<span class="ln-warn">NO</span>'), { raw: true }]);
        loraEl.innerHTML = kvRows(loraRows);

        // Capabilities
        capsEl.innerHTML = kvRows([
            ['Wi-Fi',       boolBadge(ln.has_wifi)],
            ['Bluetooth',   boolBadge(ln.has_bluetooth)],
            ['PKC',         boolBadge(ln.has_pkc)],
            ['Can shutdown', boolBadge(ln.can_shutdown)],
        ], { raw: true });
    }

    // kvRows renders a flat array of [label, value, opts?] tuples as a
    // two-column grid. Empty values render a muted dash so the grid stays
    // aligned. When opts.raw is true or a per-row opts.raw override is set,
    // the value is inserted as HTML (for badges/spans).
    function kvRows(rows, globalOpts) {
        const gRaw = !!(globalOpts && globalOpts.raw);
        return rows.map(row => {
            const [label, value, opts] = row;
            const rowRaw = gRaw || !!(opts && opts.raw);
            const v = (value === undefined || value === null || value === '') ? '<span class="ln-dash">—</span>' : (rowRaw ? value : esc(String(value)));
            return `<div class="ln-row"><span class="ln-key">${esc(label)}</span><span class="ln-val">${v}</span></div>`;
        }).join('');
    }

    // boolBadge — yes/no pill with color. Undefined/null renders a muted dash.
    function boolBadge(v) {
        if (v === undefined || v === null) return '<span class="ln-dash">—</span>';
        return v
            ? '<span class="ln-bool ln-bool-yes">yes</span>'
            : '<span class="ln-bool ln-bool-no">no</span>';
    }

    // Format a number of seconds as "Xd Yh Zm" or "Yh Zm" or "Zm Ss".
    function fmtUptime(sec) {
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

    // ---- Messages ----
    function renderMessages() {
        const tbody = document.querySelector('#messages-table tbody');
        tbody.innerHTML = '';
        const { key, dir } = state.sort.messages;
        const sorted = state.messages.slice().sort((a, b) => {
            const va = msgSortVal(a, key);
            const vb = msgSortVal(b, key);
            if (va < vb) return -1 * dir;
            if (va > vb) return  1 * dir;
            return 0;
        });
        sorted.forEach(ev => tbody.appendChild(makeMessageRow(ev)));
        updateSortIndicators('messages-table', state.sort.messages);
    }

    function makeMessageRow(ev) {
        const tr = document.createElement('tr');
        const text = ev.details ? ev.details.text || '' : '';
        const relayHtml = makeRelayTag(ev);
        tr.innerHTML = `
            <td style="white-space:nowrap;color:var(--text-dim);font-variant-numeric:tabular-nums">${fmtTime(ev.time)}</td>
            <td style="font-weight:500">${nodeName(ev.from)}</td>
            <td>${nodeName(ev.to)}</td>
            <td class="msg-text">${esc(text)}${relayHtml}</td>
            <td class="signal-val rssi">${ev.rssi || '-'}</td>
            <td class="signal-val snr">${ev.snr ? ev.snr.toFixed(1) : '-'}</td>`;
        return tr;
    }

    // ---- Sort state per table ----
    state.sort = {
        nodes:    { key: 'packets',  dir: -1 }, // default: packets desc
        messages: { key: 'time',     dir: -1 }, // default: time desc
    };

    // Extracts the sort value for a given column key from a node or message row.
    function nodeSortVal(n, key) {
        switch (key) {
            // Sort by long_name only; empty ones sort to the bottom (use
            // "\uffff" as tail sentinel so they group together when
            // ascending, not interleaved with "A"-prefixed names).
            case 'name':       return (n.long_name || '\uffff').toLowerCase();
            case 'short_name': return (n.short_name || '\uffff').toLowerCase();
            case 'node_id':    return (n.id || `!${(n.node_num || 0).toString(16).padStart(8,'0')}`).toLowerCase();
            case 'hw':         return (n.hw_model || '').toLowerCase();
            // Sort by role family weight so infrastructure nodes group
            // together (routers first, repeaters next, clients last) rather
            // than alphabetically. Unknown/empty roles go to the bottom.
            case 'role':       return roleSortWeight(n.role);
            case 'last_heard': return n.last_heard || 0;
            case 'rssi':       return n.rssi || -9999;
            case 'battery':    return n.battery_level || 0;
            case 'packets':    return sumPackets(n.packets_by_type || {});
            // Sort by peak (max ever observed): outliers surface first.
            // Fall back to mode so nodes with only one unique hop_start sort
            // correctly too (max=mode=0 means "no data", ranks lowest).
            case 'hopstart':   return (n.hop_start_max || 0) * 10 + (n.hop_start_mode || 0);
        }
        return 0;
    }

    // Short label + CSS class for a Meshtastic device role. The enum string
    // comes straight from the protobuf (User.Role.String()) so we switch on
    // the exact values the firmware emits. Unknown roles fall back to a
    // neutral gray badge so new firmware values still render.
    function roleBadge(role) {
        if (!role) return '';
        const map = {
            CLIENT:         { short: 'CL',  cls: 'role-client',   name: 'Client' },
            CLIENT_MUTE:    { short: 'CM',  cls: 'role-mute',     name: 'Client mute' },
            CLIENT_HIDDEN:  { short: 'CH',  cls: 'role-hidden',   name: 'Client hidden' },
            ROUTER:         { short: 'RT',  cls: 'role-router',   name: 'Router' },
            ROUTER_CLIENT:  { short: 'RC',  cls: 'role-router',   name: 'Router client (deprecated)' },
            ROUTER_LATE:    { short: 'RL',  cls: 'role-router',   name: 'Router late' },
            REPEATER:       { short: 'RP',  cls: 'role-repeater', name: 'Repeater' },
            TRACKER:        { short: 'TR',  cls: 'role-tracker',  name: 'Tracker' },
            SENSOR:         { short: 'SN',  cls: 'role-sensor',   name: 'Sensor' },
            TAK:            { short: 'TAK', cls: 'role-tak',      name: 'TAK' },
            TAK_TRACKER:    { short: 'TKT', cls: 'role-tak',      name: 'TAK tracker' },
            LOST_AND_FOUND: { short: 'LF',  cls: 'role-lost',     name: 'Lost & Found' },
        };
        const r = map[role] || { short: role.slice(0, 3).toUpperCase(), cls: 'role-unknown', name: role };
        return `<span class="role-badge ${r.cls}" title="${esc(r.name)} (${esc(role)})">${esc(r.short)}</span>`;
    }

    // Ordering weight for role sort: infrastructure first (ROUTER →
    // REPEATER → ROUTER_LATE), specialized next (TRACKER, SENSOR, TAK…),
    // plain clients after, unknowns/empty at the tail. Lower = earlier when
    // sorting ascending.
    function roleSortWeight(role) {
        const w = {
            ROUTER:         10,
            ROUTER_LATE:    11,
            ROUTER_CLIENT:  12,
            REPEATER:       20,
            TRACKER:        30,
            TAK_TRACKER:    31,
            SENSOR:         40,
            TAK:            50,
            CLIENT:         60,
            CLIENT_MUTE:    61,
            CLIENT_HIDDEN:  62,
            LOST_AND_FOUND: 90,
        };
        if (!role) return 999;
        return w[role] !== undefined ? w[role] : 500;
    }

    // Color band for a hop_start value (TTL set at sender).
    // 0-3 = standard Meshtastic default, green.
    // 4-5 = elevated, yellow.
    // 6-7 = aggressive TTL, wastes airtime, orange/red.
    function hopStartColor(v) {
        if (v >= 6) return 'var(--red)';
        if (v >= 4) return 'var(--yellow)';
        return 'var(--green)';
    }

    // Build the "Max hop" cell content for a node row: mode + optional peak.
    // Empty when the node has no HopStart observations yet.
    function hopStartCellHTML(n) {
        const mode = n.hop_start_mode | 0;
        const max  = n.hop_start_max  | 0;
        const hist = n.hop_start_hist || {};
        if (!mode && !max && Object.keys(hist).length === 0) {
            return '<span style="color:var(--text-muted)">-</span>';
        }
        // Tooltip: full distribution sorted by hop_start value.
        const total = Object.values(hist).reduce((a, b) => a + b, 0) || 1;
        const tip = Object.entries(hist)
            .sort((a, b) => (+a[0]) - (+b[0]))
            .map(([k, v]) => `hop_start=${k}: ${v} (${(v/total*100).toFixed(0)}%)`)
            .join('\n');
        const modeColor = hopStartColor(mode);
        if (max > mode) {
            const maxColor = hopStartColor(max);
            return `<span class="hopstart-pill" title="${esc(tip)}">
                <span class="hopstart-mode" style="color:${modeColor}">${mode}</span><span class="hopstart-peak" style="color:${maxColor}" title="peak ${max}">&nbsp;\u2191${max}</span></span>`;
        }
        return `<span class="hopstart-pill" title="${esc(tip)}"><span class="hopstart-mode" style="color:${modeColor}">${mode}</span></span>`;
    }
    function msgSortVal(m, key) {
        switch (key) {
            case 'time': return m.time || '';
            case 'from': return (m.from || '').toLowerCase();
            case 'to':   return (m.to || '').toLowerCase();
            case 'text': return (m.details && m.details.text || '').toLowerCase();
            case 'rssi': return m.rssi || -9999;
            case 'snr':  return m.snr  || -9999;
        }
        return '';
    }

    // ---- Nodes table ----
    function renderNodesTable() {
        const tbody = document.querySelector('#nodes-table tbody');
        if (!tbody) return;
        tbody.innerHTML = '';
        const filterEl = document.getElementById('nodes-filter');
        const q = (filterEl ? filterEl.value : '').toLowerCase().trim();
        // Difensivo: se lo stato di sort è assente o con chiave sconosciuta,
        // ricade sul default (pacchetti decrescenti).
        if (!state.sort || !state.sort.nodes || !state.sort.nodes.key) {
            state.sort = state.sort || {};
            state.sort.nodes = { key: 'packets', dir: -1 };
        }
        const { key, dir } = state.sort.nodes;
        const sorted = Object.values(state.nodes)
            .filter(n => nodeMatchesFilter(n, q))
            .sort((a, b) => {
                const va = nodeSortVal(a, key);
                const vb = nodeSortVal(b, key);
                if (va < vb) return -1 * dir;
                if (va > vb) return  1 * dir;
                return 0;
            });
        sorted.forEach(n => tbody.appendChild(makeNodeRow(n)));
        updateSortIndicators('nodes-table', state.sort.nodes);
    }

    // Indicator arrows and click handlers for sortable tables.
    function updateSortIndicators(tableId, sortState) {
        const table = document.getElementById(tableId);
        if (!table) return;
        table.querySelectorAll('th[data-sort]').forEach(th => {
            th.classList.remove('sorted-asc', 'sorted-desc');
            if (th.dataset.sort === sortState.key) {
                th.classList.add(sortState.dir === 1 ? 'sorted-asc' : 'sorted-desc');
            }
        });
    }
    function wireSortableTable(tableId, stateKey, rerender) {
        const table = document.getElementById(tableId);
        if (!table) return;
        table.querySelectorAll('th[data-sort]').forEach(th => {
            th.style.cursor = 'pointer';
            th.addEventListener('click', () => {
                const key = th.dataset.sort;
                const st = state.sort[stateKey];
                if (st.key === key) st.dir = -st.dir;
                else { st.key = key; st.dir = 1; }
                rerender();
            });
        });
    }
    // Wire after DOM ready (script runs at bottom so DOM is ready)
    wireSortableTable('nodes-table', 'nodes', renderNodesTable);
    wireSortableTable('messages-table', 'messages', renderMessages);

    // Shared filter: matches long_name, short_name, id, hw_model, hex node_num, role.
    function nodeMatchesFilter(n, q) {
        if (!q) return true;
        const hay = [
            n.long_name, n.short_name, n.id, n.hw_model, n.role,
            `!${(n.node_num || 0).toString(16).padStart(8, '0')}`
        ].filter(Boolean).join(' ').toLowerCase();
        return q.split(/\s+/).every(term => hay.includes(term));
    }

    // Wire up the nodes filter input.
    document.getElementById('nodes-filter')?.addEventListener('input', () => renderNodesTable());

    function makeNodeRow(n) {
        const tr = document.createElement('tr');
        tr.dataset.nodeNum = n.node_num;
        const pbt = n.packets_by_type || {};
        const total = sumPackets(pbt);
        // Split identity into three independent cells so each can be sorted
        // and scanned on its own. Fallback to "-" when a field is missing
        // rather than cascading between fields — makes empty long_names
        // immediately obvious (a node that never sent NODEINFO).
        const longName  = n.long_name  || '';
        const shortName = n.short_name || '';
        const nodeId    = n.id || `!${(n.node_num || 0).toString(16).padStart(8, '0')}`;
        const lastHeard = n.last_heard ? relativeTime(n.last_heard) : '-';

        // Status dot
        const age = n.last_heard ? Math.floor(Date.now() / 1000) - n.last_heard : Infinity;
        const statusClass = age < 900 ? 'online' : age < 3600 ? 'recent' : 'offline';

        // Signal
        const rssiStr = n.rssi ? `${n.rssi}` : '-';
        const snrStr = n.snr ? n.snr.toFixed(1) : '-';

        // Battery bar (101 = mains-powered per Meshtastic convention)
        let batHtml = '-';
        if (n.battery_level && n.battery_level > 0) {
            const bat = n.battery_level;
            if (bat >= 101) {
                batHtml = `<span class="bat-pwd" title="Mains powered">PWD</span>`;
            } else {
                const batColor = bat > 50 ? 'var(--green)' : bat > 20 ? 'var(--yellow)' : 'var(--red)';
                batHtml = `<div class="bat-bar">
                    <div class="bat-bar-track"><div class="bat-bar-fill" style="width:${bat}%;background:${batColor}"></div></div>
                    <span>${bat}%</span></div>`;
            }
        }

        // Packet badges
        const badgesHtml = Object.entries(pbt)
            .sort((a, b) => b[1] - a[1])
            .map(([type, count]) => {
                const color = getTypeColor(type);
                return `<span class="pkt-badge" style="background:${color}20;color:${color}">${shortTypeName(type)}<span class="pkt-count">${count}</span></span>`;
            }).join('');

        const longCell  = longName
            ? `<a href="#" class="node-name node-name-link" data-node-num="${n.node_num}" title="Open node detail">${esc(longName)}</a>`
            : `<a href="#" class="node-name-link node-empty" data-node-num="${n.node_num}" title="Open node detail">-</a>`;
        const shortCell = shortName ? `<span class="node-short">${esc(shortName)}</span>` : '<span class="node-empty">-</span>';
        const roleCell  = n.role ? roleBadge(n.role) : '<span class="node-empty">-</span>';
        tr.innerHTML = `
            <td class="node-name-cell">
                <span class="node-status ${statusClass}"></span>
                ${longCell}
            </td>
            <td class="node-short-cell">${shortCell}</td>
            <td class="node-id-cell">${esc(nodeId)}</td>
            <td style="color:var(--text-dim);font-size:0.75rem">${esc(n.hw_model || '-')}</td>
            <td class="node-role-cell">${roleCell}</td>
            <td style="white-space:nowrap">${lastHeard}</td>
            <td style="font-variant-numeric:tabular-nums;font-size:0.75rem">
                <span class="signal-val rssi">${rssiStr}</span> / <span class="signal-val snr">${snrStr}</span>
            </td>
            <td class="hopstart-cell">${hopStartCellHTML(n)}</td>
            <td class="spark-cell"></td>
            <td>${batHtml}</td>
            <td><span class="node-total">${total}</span></td>
            <td><div class="pkt-badges">${badgesHtml || '<span style="color:var(--text-muted)">-</span>'}</div></td>
            <td><button class="btn-tr" data-node-num="${n.node_num}" title="Send traceroute to this node">TR</button></td>`;
        // Load sparkline asynchronously
        const sparkCell = tr.querySelector('.spark-cell');
        if (n.node_num) loadSignalSparkline(n.node_num, sparkCell);
        return tr;
    }

    function sumPackets(pbt) {
        if (!pbt) return 0;
        return Object.values(pbt).reduce((s, v) => s + v, 0);
    }

    function relativeTime(unixTs) {
        const secs = Math.floor(Date.now() / 1000) - unixTs;
        if (secs < 60) return `${secs}s ago`;
        if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
        if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
        return `${Math.floor(secs / 86400)}d ago`;
    }

    // ---- Map ----
    // Map search: type a name/ID and the map flies to that node + opens its popup.
    document.getElementById('map-filter')?.addEventListener('input', function () {
        if (!state.map) return;
        const q = this.value.toLowerCase().trim();
        if (!q) return;
        // Find first matching node that has a position.
        const match = Object.values(state.nodes).find(n => {
            if (!n.has_pos || !n.lat || !n.lon) return false;
            return nodeMatchesFilter(n, q);
        });
        if (!match) return;
        const marker = state.markers[match.node_num];
        if (marker) {
            state.map.flyTo([match.lat, match.lon], 14, { duration: 0.8 });
            marker.openPopup();
        }
    });

    function initMap() {
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

    function addMarker(node) {
        const color = markerColor(node);
        const marker = L.circleMarker([node.lat, node.lon], {
            radius: 8, fillColor: color, color: '#fff', weight: 1, fillOpacity: 0.9,
        }).addTo(state.map);
        marker.bindPopup(nodePopup(node));
        state.markers[node.node_num] = marker;
    }

    function updateMapMarker(ev) {
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

    function markerColor(node) {
        const age = Math.floor(Date.now() / 1000) - (node.last_heard || 0);
        if (age < 900) return '#22c55e';
        if (age < 3600) return '#eab308';
        return '#555';
    }

    // Rich popup for a node on the map. Re-uses the same visual vocabulary
    // as the Nodes table (role-badge, pkt-badge, hopstart-pill) so one
    // glance tells you the same things in both views.
    function nodePopup(n) {
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
            ? `<div class="np-row">${telBits.join(' &middot; ')}</div>`
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
                <div class="np-sub">${esc(nodeId)} &middot; ${esc(n.hw_model || '?')}</div>
                <div class="np-section">
                    <div class="np-row">
                        <span class="np-label">RSSI</span> ${rssi}
                        &middot; <span class="np-label">SNR</span> ${snr}
                        ${hopHtml ? '&middot; ' + hopHtml : ''}
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
    function renderNeighborsSection(n) {
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

    // ---- Channel utilization heatmap overlay ----
    // ── ChUtil Geo-Monitor ────────────────────────────────────────────────
    // Shows per-node channel utilization on the map, colored on a fixed
    // scale so a given color always means the same ChUtil% across sessions.
    // Two stacked representations:
    //   A. Per-node filled circles (always on when the layer toggle is on).
    //      Radius decays with staleness so freshly-reported nodes stand out.
    //      Center label = the currently selected metric as "%%" text.
    //   B. Optional heat bloom: Leaflet.heat overlay for a smooth gradient
    //      when ≥6 nodes reported — gives a "zone" feel, not just dots.
    state.chutilLayer = null;    // L.LayerGroup of circles + labels
    state.chutilBloom = null;    // L.heatLayer
    state.chutilZones = [];      // last payload, cached for re-renders
    state.chutilLegend = null;   // L.Control

    // Fixed scale. Keep in sync with the legend markup.
    const CHUTIL_BANDS = [
        { max: 10, color: '#16a34a', label: '0–10%   sana' },
        { max: 20, color: '#eab308', label: '10–20%  ok' },
        { max: 30, color: '#f97316', label: '20–30%  alta' },
        { max: 40, color: '#dc2626', label: '30–40%  problematica' },
        { max: Infinity, color: '#9333ea', label: '≥40%   critica' },
    ];
    function chutilColor(v) {
        if (!v || v <= 0) return '#374151'; // no data / zero → neutral grey
        for (const b of CHUTIL_BANDS) {
            if (v < b.max) return b.color;
        }
        return CHUTIL_BANDS[CHUTIL_BANDS.length - 1].color;
    }

    // Fetches the payload and re-renders both layers + banner.
    async function refreshChUtilLayer() {
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

    function renderChUtilBanner(data) {
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

    function renderChUtilCircles() {
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

    function pickChUtilMetric(z, metric) {
        switch (metric) {
            case 'avg': return z.avg || 0;
            case 'p95': return z.p95 || 0;
            case 'max': return z.max || 0;
            default:    return z.current || 0;
        }
    }

    function renderChUtilBloom() {
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
        if (pts.length < 3) return; // avoid noise with too few points
        state.chutilBloom = L.heatLayer(pts, {
            radius: 90, blur: 65, maxZoom: 16, max: 1.0, minOpacity: 0.3,
            gradient: {
                0.00: '#16a34a',  // 0%    green
                0.25: '#eab308',  // 10%   yellow
                0.50: '#f97316',  // 20%   orange
                0.75: '#dc2626',  // 30%   red
                1.00: '#9333ea',  // 40%+  purple (critical)
            },
        }).addTo(state.map);
    }

    function chutilPopupHTML(z, metric) {
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
                <div class="np-sub">${esc(nodeId)}${hw ? ' &middot; ' + esc(hw) : ''}</div>
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
                        ${hopHtml ? hopHtml + ' &middot; ' : ''}<span class="np-label">last</span> ${lastHeard}
                    </div>
                </div>
                ${pktHtml}
            </div>
        `;
    }

    // Fetch per-node ChUtil history and render an inline SVG sparkline inside
    // the just-opened popup. Data lives outside the payload to keep /zones cheap.
    async function injectChUtilSparkline(nodeNum) {
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
    function addChUtilLegend(map) {
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

    // Wire up controls.
    document.getElementById('chutil-layer')?.addEventListener('change', renderChUtilCircles);
    document.getElementById('chutil-metric')?.addEventListener('change', () => {
        renderChUtilCircles();
        renderChUtilBloom();
    });
    document.getElementById('chutil-window')?.addEventListener('change', refreshChUtilLayer);
    document.getElementById('chutil-bloom')?.addEventListener('change', renderChUtilBloom);

    // Periodically refresh zone data while the Map tab is open.
    setInterval(() => {
        if (state.activeTab === 'map') refreshChUtilLayer();
    }, 30000);

    // ---- Telemetry charts ----
    function initCharts() {
        if (state.charts.battery) return;
        const chartColors = {
            battery: { line: '#22c55e', bg: 'rgba(34,197,94,0.1)' },
            voltage: { line: '#3b82f6', bg: 'rgba(59,130,246,0.1)' },
            channel: { line: '#f97316', bg: 'rgba(249,115,22,0.1)' },
            temp:    { line: '#ef4444', bg: 'rgba(239,68,68,0.1)' },
        };
        const mkChart = (id, label, key, yMax) => {
            const c = chartColors[key];
            return new Chart(document.getElementById(id), {
                type: 'line',
                data: {
                    labels: [],
                    datasets: [{
                        label,
                        data: [],
                        borderColor: c.line,
                        backgroundColor: c.bg,
                        fill: true,
                        tension: 0.35,
                        pointRadius: 1.5,
                        pointHoverRadius: 4,
                        borderWidth: 2,
                    }]
                },
                options: {
                    responsive: true,
                    animation: false,
                    interaction: { mode: 'index', intersect: false },
                    plugins: {
                        legend: { display: false },
                        tooltip: {
                            backgroundColor: '#181c28',
                            titleColor: '#d8dce6',
                            bodyColor: '#d8dce6',
                            borderColor: '#2a3050',
                            borderWidth: 1,
                            cornerRadius: 6,
                        }
                    },
                    scales: {
                        x: { display: false },
                        y: {
                            beginAtZero: true,
                            max: yMax,
                            ticks: { color: '#4a5070', font: { size: 10 } },
                            grid: { color: '#1e2235' }
                        }
                    }
                }
            });
        };
        state.charts.battery = mkChart('chart-battery', 'Battery %', 'battery', 100);
        state.charts.voltage = mkChart('chart-voltage', 'Voltage V', 'voltage');
        state.charts.channel = mkChart('chart-channel', 'Channel %', 'channel', 100);
        state.charts.temp    = mkChart('chart-temp', 'Temp C', 'temp');

        loadTelemetryData();
    }

    function populateNodeSelect(filterText) {
        const sel = document.getElementById('tel-node');
        if (!sel) return;
        const current = sel.value;
        const q = (filterText || '').toLowerCase().trim();
        sel.innerHTML = '';
        Object.values(state.nodes)
            .filter(n => nodeMatchesFilter(n, q))
            .sort((a, b) => {
                const na = (a.long_name || a.short_name || a.id || '').toLowerCase();
                const nb = (b.long_name || b.short_name || b.id || '').toLowerCase();
                return na.localeCompare(nb);
            })
            .forEach(n => {
                const opt = document.createElement('option');
                opt.value = n.node_num;
                opt.textContent = n.long_name || n.short_name || n.id || `!${n.node_num.toString(16).padStart(8, '0')}`;
                sel.appendChild(opt);
            });
        if (current && sel.querySelector(`option[value="${current}"]`)) {
            sel.value = current;
        }
        sel.onchange = () => { state.selectedNode = parseInt(sel.value); loadTelemetryData(); };
        if (!state.selectedNode && sel.options.length > 0) {
            state.selectedNode = parseInt(sel.options[0].value);
        }
    }

    // Wire up telemetry filter.
    document.getElementById('tel-filter')?.addEventListener('input', (e) => {
        populateNodeSelect(e.target.value);
    });

    async function loadTelemetryData() {
        if (!state.selectedNode || !state.charts.battery) return;
        const hex = state.selectedNode.toString(16).padStart(8, '0');
        const events = await api(`/api/telemetry/${hex}?limit=200`);
        ['battery', 'voltage', 'channel', 'temp'].forEach(k => {
            state.charts[k].data.labels = [];
            state.charts[k].data.datasets[0].data = [];
        });
        (events || []).reverse().forEach(ev => pushTelemetryPoint(ev));
        ['battery', 'voltage', 'channel', 'temp'].forEach(k => state.charts[k].update());
    }

    function pushTelemetryPoint(ev) {
        const d = ev.details || {};
        const t = fmtTime(ev.time);
        if (d.type === 'device') {
            addPoint(state.charts.battery, t, d['battery_level_%']);
            addPoint(state.charts.voltage, t, d['voltage_v']);
            addPoint(state.charts.channel, t, d['channel_utilization_%']);
        }
        if (d.type === 'environment') {
            addPoint(state.charts.temp, t, d['temperature_c']);
        }
    }

    function addPoint(chart, label, value) {
        if (value === undefined || value === null) return;
        chart.data.labels.push(label);
        chart.data.datasets[0].data.push(value);
        if (chart.data.labels.length > 200) {
            chart.data.labels.shift();
            chart.data.datasets[0].data.shift();
        }
    }

    // =====================================================================
    //  NETWORK / TRACEROUTE MAP
    // =====================================================================

    let networkLayers = { nodes: null, traces: null, links: null };
    let networkLinksData = [];

    function initNetworkMap() {
        if (state.networkMap) { state.networkMap.invalidateSize(); return; }
        state.networkMap = L.map('networkMapContainer').setView([44, 11], 6);
        L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
            attribution: '&copy; OpenStreetMap', maxZoom: 18,
        }).addTo(state.networkMap);

        networkLayers.nodes  = L.layerGroup().addTo(state.networkMap);
        networkLayers.traces = L.layerGroup().addTo(state.networkMap);
        networkLayers.links  = L.layerGroup();

        const bounds = [];
        Object.values(state.nodes).forEach(n => {
            if (n.has_pos && n.lat && n.lon) {
                L.circleMarker([n.lat, n.lon], {
                    radius: 7, fillColor: '#3b82f6', color: '#fff', weight: 1.5, fillOpacity: 0.85,
                }).bindTooltip(n.long_name || n.short_name || n.id || '?', { permanent: false })
                  .addTo(networkLayers.nodes);
                bounds.push([n.lat, n.lon]);
            }
        });
        if (bounds.length > 0) state.networkMap.fitBounds(bounds, { padding: [30, 30] });

        const showLinksEl = document.getElementById('show-links');
        if (showLinksEl) {
            showLinksEl.addEventListener('change', async (e) => {
                if (e.target.checked) {
                    if (networkLinksData.length === 0) {
                        networkLinksData = await api('/api/links') || [];
                    }
                    drawHeardLinks();
                    state.networkMap.addLayer(networkLayers.links);
                } else {
                    state.networkMap.removeLayer(networkLayers.links);
                }
            });
        }

        renderTracerouteList();
    }

    function drawHeardLinks() {
        if (!state.networkMap || !networkLayers.links) return;
        networkLayers.links.clearLayers();

        (networkLinksData || []).forEach(link => {
            const nodeA = state.nodes[link.node_a];
            const nodeB = state.nodes[link.node_b];
            if (!nodeA || !nodeB || !nodeA.has_pos || !nodeB.has_pos) return;

            const isNeighbor = !!link.neighbor;
            const color = isNeighbor ? snrQualityColor(link.snr) : rssiColor(link.rssi);
            const weight = isNeighbor
                ? Math.max(2, Math.min(5, 2 + link.count * 0.3))
                : Math.max(1.5, Math.min(4, 1.5 + link.count * 0.3));

            const line = L.polyline(
                [[nodeA.lat, nodeA.lon], [nodeB.lat, nodeB.lon]],
                { color, weight, opacity: isNeighbor ? 0.7 : 0.45, dashArray: isNeighbor ? null : '6, 4' }
            );

            let popupHtml = `<b>${nodeNameByNum(link.node_a)}</b> &harr; <b>${nodeNameByNum(link.node_b)}</b><br>`;
            if (isNeighbor) {
                popupHtml += `SNR: ${link.snr.toFixed(1)} dB<br>`;
                popupHtml += `<em style="color:#14b8a6">&#x2713; Direct neighbor</em><br>`;
            }
            if (link.rssi) popupHtml += `RSSI: ${link.rssi} dBm<br>`;
            popupHtml += `Packets: ${link.count}`;
            line.bindPopup(popupHtml);

            if (isNeighbor) {
                const mid = [(nodeA.lat + nodeB.lat) / 2, (nodeA.lon + nodeB.lon) / 2];
                L.marker(mid, {
                    icon: L.divIcon({
                        className: 'snr-label',
                        html: `<span>${link.snr.toFixed(1)} dB</span>`,
                        iconSize: [56, 16], iconAnchor: [28, 8],
                    }),
                    interactive: false,
                }).addTo(networkLayers.links);
            }

            line.addTo(networkLayers.links);
        });
    }

    async function refreshHeardLinks() {
        networkLinksData = await api('/api/links') || [];
        drawHeardLinks();
    }

    // ---- Traceroute sidebar ----

    // Render the Network-tab sidebar of traceroutes.
    //
    // Each card shows the full forward path (and the return path, when the
    // packet is a reply) including:
    //   - per-node "no GPS" badge if we don't have a position for that node
    //     (the segment will not be drawable on the map but is shown here)
    //   - per-hop SNR pill colored by quality (always known regardless of GPS)
    //   - REQUEST / REPLY badge (REQUEST = in-transit observation without
    //     a return path yet; REPLY = full forward+back).
    //   - "PARTIAL MAP" badge if at least one node in the chain has no GPS
    //     (the polyline on the map will skip the broken segment).
    function renderTracerouteList() {
        const container = document.getElementById('traceroute-list');
        if (!container) return;
        if (!state.traceroutes || state.traceroutes.length === 0) {
            container.innerHTML = '<p class="text-dim">No traceroute data yet</p>';
            return;
        }
        container.innerHTML = '';
        [...state.traceroutes].reverse().forEach((tr, idx) => {
            const card = document.createElement('div');
            card.className = 'tr-card';
            card.dataset.idx = state.traceroutes.length - 1 - idx;

            const fwdNums = [tr.from, ...((tr.route || []).map(parseNodeNum)), tr.to];
            const fwdSnr  = tr.snr_towards || [];
            const fwdHtml = renderTrChain(fwdNums, fwdSnr, false);

            const hasReturn = tr.route_back && tr.route_back.length > 0;
            let retHtml = '';
            if (hasReturn) {
                const retNums = [tr.to, ...tr.route_back.map(parseNodeNum), tr.from];
                const retSnr  = tr.snr_back || [];
                retHtml = `<div class="tr-ret-label">&hookleftarrow; return</div>${renderTrChain(retNums, retSnr, true)}`;
            }

            const time = new Date(tr.time * 1000).toLocaleString('it-IT', {
                hour: '2-digit', minute: '2-digit', second: '2-digit', day: '2-digit', month: '2-digit',
            });
            const hopCount = fwdNums.length - 1;
            const allNums = hasReturn ? [...fwdNums, ...[tr.to, ...tr.route_back.map(parseNodeNum), tr.from]] : fwdNums;
            const noGpsCount = allNums.filter(n => {
                const node = state.nodes[n];
                return !(node && node.has_pos);
            }).length;
            const isPartialMap = noGpsCount > 0;

            const typeBadge = hasReturn
                ? '<span class="tr-badge tr-badge-reply">REPLY</span>'
                : '<span class="tr-badge tr-badge-request">REQUEST</span>';
            const partialBadge = isPartialMap
                ? `<span class="tr-badge tr-badge-partial" title="${noGpsCount} node(s) without GPS — segment(s) not drawable on the map">PARTIAL MAP</span>`
                : '';

            card.innerHTML = `
                <div class="tr-head">
                    <span class="tr-time">${time}</span>
                    <span class="tr-hops">${hopCount} hop${hopCount !== 1 ? 's' : ''}</span>
                    ${typeBadge}
                    ${partialBadge}
                </div>
                <div class="tr-chain-label">forward</div>
                ${fwdHtml}
                ${retHtml}`;

            card.addEventListener('click', () => highlightTraceroute(parseInt(card.dataset.idx), card));
            container.appendChild(card);
        });
    }

    // renderTrChain builds the inline HTML for a sequence of nodes connected
    // by SNR-labeled arrows. nums is the full chain (including endpoints);
    // snrRaw[i] is the SNR for hop nums[i] -> nums[i+1] (in protobuf raw
    // units = dB * 4). Nodes without GPS get a small "no GPS" tag so the
    // user can see exactly which segment will be missing on the map.
    function renderTrChain(nums, snrRaw, isReturn) {
        const parts = [];
        for (let i = 0; i < nums.length; i++) {
            const num = nums[i];
            const node = state.nodes[num];
            const name = nodeNameByNum(num) || `!${(num >>> 0).toString(16).padStart(8, '0')}`;
            const hasGps = node && node.has_pos;
            const gpsTag = hasGps
                ? ''
                : '<span class="tr-no-gps" title="No known GPS position — this segment cannot be drawn on the map">no GPS</span>';
            parts.push(`<span class="tr-hop ${hasGps ? '' : 'tr-hop-nogps'}">${esc(name)}${gpsTag}</span>`);
            if (i < nums.length - 1) {
                const raw = snrRaw[i];
                const hasSnr = raw !== undefined && raw !== null;
                const snrDb = hasSnr ? (raw / 4) : null;
                if (hasSnr) {
                    const color = snrQualityColor(snrDb);
                    parts.push(`<span class="tr-arrow tr-arrow-snr" style="--snr-color:${color}" title="SNR ${snrDb.toFixed(2)} dB">&rarr;<span class="tr-snr-pill" style="background:${color}">${snrDb.toFixed(1)}</span></span>`);
                } else {
                    parts.push('<span class="tr-arrow">&rarr;</span>');
                }
            }
        }
        return `<div class="tr-chain ${isReturn ? 'tr-chain-ret' : ''}">${parts.join('')}</div>`;
    }

    function highlightTraceroute(idx, card) {
        document.querySelectorAll('.tr-card').forEach(c => c.classList.remove('active'));
        card.classList.add('active');

        networkLayers.traces.clearLayers();

        const tr = state.traceroutes[idx];
        if (!tr) return;

        const fwdNums = [tr.from, ...((tr.route || []).map(parseNodeNum)), tr.to];
        const snrFwd  = tr.snr_towards || [];
        drawTraceChain(fwdNums, snrFwd, false, 'Forward');

        let retNums = [];
        if (tr.route_back && tr.route_back.length > 0) {
            retNums = [tr.to, ...tr.route_back.map(parseNodeNum), tr.from];
            drawTraceChain(retNums, tr.snr_back || [], true, 'Return');
        }

        // Endpoint markers: green = source, red = destination. Drawn only when
        // we actually know their GPS position; otherwise they live in the
        // sidebar with a "no GPS" badge.
        const srcNode = state.nodes[tr.from];
        if (srcNode && srcNode.has_pos) {
            L.circleMarker([srcNode.lat, srcNode.lon], {
                radius: 11, fillColor: '#22c55e', color: '#fff', weight: 2, fillOpacity: 0.9,
            }).bindTooltip('Start: ' + nodeNameByNum(tr.from), { permanent: false })
              .addTo(networkLayers.traces);
        }
        const dstNode = state.nodes[tr.to];
        if (dstNode && dstNode.has_pos) {
            L.circleMarker([dstNode.lat, dstNode.lon], {
                radius: 11, fillColor: '#ef4444', color: '#fff', weight: 2, fillOpacity: 0.9,
            }).bindTooltip('End: ' + nodeNameByNum(tr.to), { permanent: false })
              .addTo(networkLayers.traces);
        }

        // Intermediate hops with GPS get a small grey marker so the user can
        // visually walk the chain, hop by hop.
        const allNums = retNums.length > 0 ? [...fwdNums, ...retNums] : fwdNums;
        const seen = new Set([tr.from, tr.to]);
        allNums.forEach(num => {
            if (seen.has(num)) return;
            seen.add(num);
            const n = state.nodes[num];
            if (n && n.has_pos) {
                L.circleMarker([n.lat, n.lon], {
                    radius: 6, fillColor: '#94a3b8', color: '#fff', weight: 1.5, fillOpacity: 0.85,
                }).bindTooltip(nodeNameByNum(num), { permanent: false })
                  .addTo(networkLayers.traces);
            }
        });

        // Fit bounds to whatever positions we have.
        const bounds = [];
        allNums.forEach(num => {
            const n = state.nodes[num];
            if (n && n.has_pos) bounds.push([n.lat, n.lon]);
        });
        if (bounds.length > 0) {
            state.networkMap.fitBounds(bounds, { padding: [50, 50] });
        }
    }

    // drawTraceChain draws each hop of a traceroute path independently. For
    // every consecutive (i, i+1) pair we draw the segment ONLY if both
    // endpoints have a known GPS position; otherwise we silently skip just
    // that segment (the sidebar still shows the SNR for it). This means a
    // chain of e.g. 6 hops where one intermediate node has no GPS will
    // render the 5 drawable segments correctly, instead of joining the two
    // GPS-known endpoints with a misleading straight line.
    function drawTraceChain(nums, snrValues, isReturn, label) {
        for (let i = 0; i < nums.length - 1; i++) {
            const aNum = nums[i], bNum = nums[i + 1];
            const a = state.nodes[aNum];
            const b = state.nodes[bNum];
            if (!a || !a.has_pos || !b || !b.has_pos) continue; // gap — skip just this segment

            const snrRaw = snrValues[i];
            const hasSnr = snrRaw !== undefined && snrRaw !== null;
            const snrDb = hasSnr ? (snrRaw / 4) : null;
            const color = hasSnr ? snrQualityColor(snrDb) : (isReturn ? '#7dd3fc' : '#3b82f6');

            let p1 = [a.lat, a.lon];
            let p2 = [b.lat, b.lon];
            if (isReturn) {
                const off = perpOffset(a.lat, a.lon, b.lat, b.lon, 80);
                p1 = [a.lat + off.dlat, a.lon + off.dlon];
                p2 = [b.lat + off.dlat, b.lon + off.dlon];
            }

            const line = L.polyline([p1, p2], {
                color,
                weight: isReturn ? 3.5 : 4.5,
                opacity: 0.85,
                dashArray: isReturn ? '8, 6' : null,
            });
            const fromName = nodeNameByNum(aNum);
            const toName   = nodeNameByNum(bNum);
            let popupText  = `<b>${label} hop ${i + 1}</b><br>${fromName} &rarr; ${toName}`;
            if (hasSnr) popupText += `<br>SNR: ${snrDb.toFixed(2)} dB`;
            line.bindPopup(popupText);
            line.addTo(networkLayers.traces);

            const mid = [(p1[0] + p2[0]) / 2, (p1[1] + p2[1]) / 2];
            const angle = bearingDeg(p1[0], p1[1], p2[0], p2[1]);
            L.marker(mid, {
                icon: L.divIcon({
                    className: '',
                    html: `<div style="transform:rotate(${angle - 90}deg);color:${color};font-size:16px;text-shadow:0 0 3px #000,0 0 3px #000;line-height:1">&#9654;</div>`,
                    iconSize: [16, 16], iconAnchor: [8, 8],
                }),
                interactive: false,
            }).addTo(networkLayers.traces);

            if (hasSnr) {
                const lblOff = perpOffset(p1[0], p1[1], p2[0], p2[1], isReturn ? -35 : 35);
                const lblPos = [mid[0] + lblOff.dlat, mid[1] + lblOff.dlon];
                L.marker(lblPos, {
                    icon: L.divIcon({
                        className: '',
                        html: `<div class="tr-snr-label" style="background:${color}">${snrDb.toFixed(1)} dB</div>`,
                        iconSize: [50, 18], iconAnchor: [25, 9],
                    }),
                    interactive: false,
                }).addTo(networkLayers.traces);
            }
        }
    }


    // ---- Geometry helpers ----

    function bearingDeg(lat1, lon1, lat2, lon2) {
        const toRad = Math.PI / 180;
        const dLon = (lon2 - lon1) * toRad;
        const y = Math.sin(dLon) * Math.cos(lat2 * toRad);
        const x = Math.cos(lat1 * toRad) * Math.sin(lat2 * toRad)
                - Math.sin(lat1 * toRad) * Math.cos(lat2 * toRad) * Math.cos(dLon);
        return (Math.atan2(y, x) * 180 / Math.PI + 360) % 360;
    }

    function perpOffset(lat1, lon1, lat2, lon2, meters) {
        const bear = bearingDeg(lat1, lon1, lat2, lon2);
        const perpRad = (bear + 90) * Math.PI / 180;
        const R = 6371000;
        return {
            dlat: (meters * Math.cos(perpRad) / R) * (180 / Math.PI),
            dlon: (meters * Math.sin(perpRad) / (R * Math.cos(lat1 * Math.PI / 180))) * (180 / Math.PI),
        };
    }

    // ---- Signal-quality coloring ----

    function rssiColor(rssi) {
        if (!rssi || rssi === 0) return '#555';
        if (rssi >= -70)  return '#22c55e';
        if (rssi >= -90)  return '#84cc16';
        if (rssi >= -100) return '#eab308';
        if (rssi >= -110) return '#f97316';
        return '#ef4444';
    }

    function snrQualityColor(snrDb) {
        if (snrDb === null || snrDb === undefined) return '#555';
        if (snrDb >= 10) return '#22c55e';
        if (snrDb >= 5)  return '#84cc16';
        if (snrDb >= 0)  return '#eab308';
        if (snrDb >= -5) return '#f97316';
        return '#ef4444';
    }

    // ---- Helpers ----

    function nodeNameByNum(num) {
        const n = state.nodes[num];
        return n ? (n.short_name || n.long_name || n.id || `!${num.toString(16).padStart(8,'0')}`) : `!${(num||0).toString(16).padStart(8,'0')}`;
    }

    function fmtTime(isoStr) {
        if (!isoStr) return '-';
        const d = new Date(isoStr);
        return d.toLocaleTimeString('it-IT', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    }

    function fmtNum(n) {
        if (n === undefined || n === null) return '0';
        if (n >= 10000) return (n / 1000).toFixed(1) + 'k';
        return n.toLocaleString('it-IT');
    }

    function nodeName(id) {
        if (!id || id === '' || id === '^all') return id || '-';
        const num = parseNodeNum(id);
        const node = num ? state.nodes[num] : null;
        return node && node.short_name ? `${node.short_name}` : (id || '-');
    }

    function parseNodeNum(id) {
        if (!id || typeof id === 'number') return id || 0;
        if (typeof id !== 'string') return 0;
        if (id.startsWith('!')) return parseInt(id.slice(1), 16) || 0;
        return parseInt(id, 16) || 0;
    }

    function esc(s) {
        if (!s) return '';
        const div = document.createElement('div');
        div.textContent = s;
        return div.innerHTML;
    }

    function shortTypeName(type) {
        const map = {
            TEXT_MESSAGE: 'MSG',
            POSITION: 'POS',
            TELEMETRY: 'TEL',
            NODE_INFO: 'NODE',
            ROUTING: 'ROUTE',
            TRACEROUTE: 'TRACE',
            NEIGHBOR_INFO: 'NEIGH',
            STORE_FORWARD: 'S&F',
            ENCRYPTED: 'ENC',
            LOG_RECORD: 'LOG',
            MY_INFO: 'MY',
            RAW: 'RAW',
        };
        return map[type] || type;
    }

    function eventInfo(ev) {
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
            case 'STORE_FORWARD': {
                // Surface the sub-type (heartbeat / history / stats / text) and
                // the RR enum so a router doing S&F is recognizable at a glance.
                const v = d.variant && d.variant !== 'none' ? d.variant : '';
                const rr = d.rr || '';
                return [v, rr].filter(Boolean).join(' · ');
            }
            case 'RAW': return d.portnum || d.variant || `${d.size || '?'} bytes`;
            default: return '';
        }
    }

    function getTypeColor(type) {
        const colors = {
            TEXT_MESSAGE: '#3b82f6', POSITION: '#22c55e', TELEMETRY: '#eab308',
            NODE_INFO: '#a855f7', ROUTING: '#64748b', TRACEROUTE: '#f97316',
            NEIGHBOR_INFO: '#14b8a6', STORE_FORWARD: '#d946ef',
            ENCRYPTED: '#78716c', LOG_RECORD: '#475569',
            RAW: '#444', MY_INFO: '#6366f1',
        };
        return colors[type] || '#666';
    }

    // ---- Boot ----
    init();
})();
