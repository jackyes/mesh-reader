// === Telemetry tab ===
import { state } from './state.js';
import { api } from './api.js';
import { esc, fmtTime, fmtUptime } from './utils.js';
import { nodeMatchesFilter } from './nodes.js';
import { TEL_GROUPS, TEL_GROUP_LABELS, TEL_FIELDS, DEFAULT_TEL_COLOR } from './state.js';

// Re-export so other modules can import from telemetry.js
export { TEL_GROUPS, TEL_GROUP_LABELS, TEL_FIELDS, DEFAULT_TEL_COLOR };

// Resolve display metadata for a detail key, generating a fallback for
// keys not listed in TEL_FIELDS (e.g. fields added by future firmware).
export function telMeta(key) {
    if (TEL_FIELDS[key]) return TEL_FIELDS[key];
    const label = key.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase());
    return { label, unit: '', color: DEFAULT_TEL_COLOR, group: 'other' };
}

// Build one Chart.js line chart on canvas for a telemetry field.
export function mkTelChart(canvas, meta) {
    return new Chart(canvas, {
        type: 'line',
        data: { labels: [], datasets: [{
            label: meta.label, data: [],
            borderColor: meta.color, backgroundColor: meta.color + '1a',
            fill: true, tension: 0.35,
            pointRadius: 1.5, pointHoverRadius: 4, borderWidth: 2,
        }] },
        options: {
            responsive: true, animation: false,
            interaction: { mode: 'index', intersect: false },
            plugins: {
                legend: { display: false },
                tooltip: {
                    backgroundColor: '#181c28', titleColor: '#d8dce6',
                    bodyColor: '#d8dce6', borderColor: '#2a3050',
                    borderWidth: 1, cornerRadius: 6,
                },
            },
            scales: {
                x: { display: false },
                y: {
                    beginAtZero: true, max: meta.yMax,
                    ticks: { color: '#4a5070', font: { size: 10 } },
                    grid: { color: '#1e2235' },
                },
            },
        },
    });
}

// Telemetry tab entry point. Charts are built dynamically per node, so
// this just (re)loads the data for the current selection.
export function initCharts() {
    loadTelemetryData();
}

// True if the node has transmitted at least one telemetry packet.
export function nodeHasTelemetry(n) {
    return !!(n && n.packets_by_type && n.packets_by_type['TELEMETRY'] > 0);
}

export function populateNodeSelect(filterText) {
    const sel = document.getElementById('tel-node');
    if (!sel) return;
    const current = sel.value;
    const q = (filterText || '').toLowerCase().trim();
    sel.innerHTML = '';
    Object.values(state.nodes)
        .filter(n => nodeHasTelemetry(n) && nodeMatchesFilter(n, q))
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
    // Keep the selection on a node that actually has telemetry data.
    if (sel.options.length > 0 &&
        (!state.selectedNode || !sel.querySelector(`option[value="${state.selectedNode}"]`))) {
        state.selectedNode = parseInt(sel.options[0].value);
        sel.value = state.selectedNode;
    }
}

// Collect the numeric telemetry detail keys present across a list of events.
export function telKeysOf(list) {
    const keys = new Set();
    list.forEach(ev => {
        const d = ev.details || {};
        Object.keys(d).forEach(k => {
            if (k !== 'type' && typeof d[k] === 'number') keys.add(k);
        });
    });
    return [...keys].sort();
}

export async function loadTelemetryData() {
    if (!state.selectedNode) return;
    const hex = state.selectedNode.toString(16).padStart(8, '0');
    const events = await api(`/api/telemetry/${hex}?limit=200`);
    const list = (events || []).slice().reverse(); // oldest first
    const keys = telKeysOf(list);

    // Rebuild the chart layout only when the node or the set of
    // transmitted metrics changed — otherwise refresh data in place.
    const sig = state.selectedNode + '|' + keys.join(',');
    if (sig !== state.telSig) {
        buildTelemetryLayout(keys);
        state.telSig = sig;
    } else {
        Object.values(state.telCharts || {}).forEach(c => {
            c.data.labels = [];
            c.data.datasets[0].data = [];
        });
    }

    list.forEach(ev => pushTelemetryPoint(ev));
    Object.values(state.telCharts || {}).forEach(c => c.update());
    renderTelCurrent(list);
}

// (Re)build the per-field chart cards, grouped by metric type.
export function buildTelemetryLayout(keys) {
    Object.values(state.telCharts || {}).forEach(c => c.destroy && c.destroy());
    state.telCharts = {};

    const wrap = document.getElementById('tel-charts');
    if (!wrap) return;
    wrap.innerHTML = '';
    if (keys.length === 0) {
        wrap.innerHTML = '<div class="tel-empty">Nessun dato di telemetria per questo nodo.</div>';
        return;
    }

    const byGroup = {};
    keys.forEach(k => {
        const g = telMeta(k).group;
        (byGroup[g] = byGroup[g] || []).push(k);
    });

    TEL_GROUPS.forEach(g => {
        const groupKeys = byGroup[g];
        if (!groupKeys) return;
        const title = document.createElement('h3');
        title.className = 'tel-group-title';
        title.textContent = TEL_GROUP_LABELS[g] || g;
        wrap.appendChild(title);

        const grid = document.createElement('div');
        grid.className = 'chart-grid';
        wrap.appendChild(grid);

        groupKeys.forEach(k => {
            const meta = telMeta(k);
            const card = document.createElement('div');
            card.className = 'chart-card';
            const h = document.createElement('h3');
            h.textContent = meta.unit ? `${meta.label} (${meta.unit})` : meta.label;
            const canvas = document.createElement('canvas');
            card.appendChild(h);
            card.appendChild(canvas);
            grid.appendChild(card);
            state.telCharts[k] = mkTelChart(canvas, meta);
        });
    });
}

export function pushTelemetryPoint(ev) {
    const d = ev.details || {};
    const t = fmtTime(ev.time);
    const charts = state.telCharts || {};
    Object.keys(d).forEach(k => {
        if (k === 'type' || typeof d[k] !== 'number') return;
        if (charts[k]) addPoint(charts[k], t, d[k]);
    });
}

// Render the "current values" summary cards from the latest value of
// each metric (list is oldest-first).
export function renderTelCurrent(list) {
    const wrap = document.getElementById('tel-current');
    if (!wrap) return;
    wrap.innerHTML = '';
    const latest = {};
    list.forEach(ev => {
        const d = ev.details || {};
        Object.keys(d).forEach(k => {
            if (k === 'type' || typeof d[k] !== 'number') return;
            latest[k] = d[k];
        });
    });
    const byGroup = {};
    Object.keys(latest).sort().forEach(k => {
        const g = telMeta(k).group;
        (byGroup[g] = byGroup[g] || []).push(k);
    });
    TEL_GROUPS.forEach(g => {
        (byGroup[g] || []).forEach(k => {
            const meta = telMeta(k);
            const card = document.createElement('div');
            card.className = 'tel-card';
            card.style.borderTopColor = meta.color;
            const lbl = document.createElement('div');
            lbl.className = 'tel-card-label';
            lbl.textContent = meta.label;
            const val = document.createElement('div');
            val.className = 'tel-card-value';
            val.textContent = fmtTelValue(k, latest[k]);
            const unitText = (k === 'uptime_seconds') ? '' : meta.unit;
            if (unitText) {
                const u = document.createElement('span');
                u.className = 'tel-card-unit';
                u.textContent = unitText;
                val.appendChild(u);
            }
            card.appendChild(lbl);
            card.appendChild(val);
            wrap.appendChild(card);
        });
    });
}

export function fmtTelValue(key, v) {
    if (typeof v !== 'number') return String(v);
    if (key === 'uptime_seconds') return fmtUptime(v);
    if (Number.isInteger(v)) return v.toLocaleString();
    return v.toFixed(2);
}

export function addPoint(chart, label, value) {
    if (value === undefined || value === null) return;
    chart.data.labels.push(label);
    chart.data.datasets[0].data.push(value);
    if (chart.data.labels.length > 200) {
        chart.data.labels.shift();
        chart.data.datasets[0].data.shift();
    }
}
