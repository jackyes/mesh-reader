// === Nodes tab ===
import { state } from './state.js';
import { api } from './api.js';
import { esc, nodeNameByNum, relativeTime, shortTypeName, getTypeColor } from './utils.js';
import { loadSignalSparkline } from './overview.js';

// ---- Nodes table ----
export function renderNodesTable() {
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

// Extracts the sort value for a given column key from a node or message row.
export function nodeSortVal(n, key) {
    switch (key) {
        // Sort by long_name only; empty ones sort to the bottom (use
        // "￿" as tail sentinel so they group together when
        // ascending, not interleaved with "A"-prefixed names).
        case 'name':       return (n.long_name || '￿').toLowerCase();
        case 'short_name': return (n.short_name || '￿').toLowerCase();
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

// Shared filter: matches long_name, short_name, id, hw_model, hex node_num, role.
export function nodeMatchesFilter(n, q) {
    if (!q) return true;
    const hay = [
        n.long_name, n.short_name, n.id, n.hw_model, n.role,
        `!${(n.node_num || 0).toString(16).padStart(8, '0')}`
    ].filter(Boolean).join(' ').toLowerCase();
    return q.split(/\s+/).every(term => hay.includes(term));
}

// Short label + CSS class for a Meshtastic device role. The enum string
// comes straight from the protobuf (User.Role.String()) so we switch on
// the exact values the firmware emits. Unknown roles fall back to a
// neutral gray badge so new firmware values still render.
export function roleBadge(role) {
    if (!role) return '';
    const map = {
        CLIENT:         { short: 'CB',  cls: 'role-client',   name: 'Client base' },
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
export function roleSortWeight(role) {
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
export function hopStartColor(v) {
    if (v >= 6) return 'var(--red)';
    if (v >= 4) return 'var(--yellow)';
    return 'var(--green)';
}

// Build the "Max hop" cell content for a node row: mode + optional peak.
// Empty when the node has no HopStart observations yet.
export function hopStartCellHTML(n) {
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
            <span class="hopstart-mode" style="color:${modeColor}">${mode}</span><span class="hopstart-peak" style="color:${maxColor}" title="peak ${max}">&nbsp;↑${max}</span></span>`;
    }
    return `<span class="hopstart-pill" title="${esc(tip)}"><span class="hopstart-mode" style="color:${modeColor}">${mode}</span></span>`;
}

export function sumPackets(pbt) {
    if (!pbt) return 0;
    return Object.values(pbt).reduce((s, v) => s + v, 0);
}

// Indicator arrows and click handlers for sortable tables.
export function updateSortIndicators(tableId, sortState) {
    const table = document.getElementById(tableId);
    if (!table) return;
    table.querySelectorAll('th[data-sort]').forEach(th => {
        th.classList.remove('sorted-asc', 'sorted-desc');
        if (th.dataset.sort === sortState.key) {
            th.classList.add(sortState.dir === 1 ? 'sorted-asc' : 'sorted-desc');
        }
    });
}
export function wireSortableTable(tableId, stateKey, rerender) {
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

export function makeNodeRow(n) {
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
