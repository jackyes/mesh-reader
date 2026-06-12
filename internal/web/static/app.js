// Mesh Reader Dashboard v2 — ES module entry point
'use strict';

// ====================================================================
//  Foundation modules
// ====================================================================
import { state, AUTO_REFRESH_INTERVAL } from './state.js';
import { esc, fmtTime, fmtNum, nodeName, parseNodeNum, relativeTime, shortTypeName, eventInfo, makeHopPill, makeRelayTag, rssiColor, snrQualityColor } from './utils.js';
import { api } from './api.js';
import { connectSSE } from './sse.js';

// ====================================================================
//  Tab modules
// ====================================================================
import { renderOverview, renderSNRDistanceChart } from './overview.js';
import { initSniffer, startSnifferTail } from './sniffer.js';
import { renderMessages } from './messages.js';
import { renderNodesTable } from './nodes.js';
import { initMap, nodePopup, refreshChUtilLayer } from './map.js';
import { initNetworkMap, renderTracerouteList, refreshHeardLinks, refreshTraceroutes } from './network.js';
import { initCharts, loadTelemetryData, populateNodeSelect } from './telemetry.js';
import { renderMisbehaving, initMisbehavingButtons } from './misbehaving.js';
import { renderLocalNode } from './local-node.js';

// ====================================================================
//  Tab navigation
// ====================================================================
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
        if (tab === 'misbehaving') { initMisbehavingButtons(); renderMisbehaving(); }
        if (tab === 'sniffer') { initSniffer(); document.getElementById('sniffer-livetail').checked = true; startSnifferTail(); }
    });
});

// ====================================================================
//  Initial load
// ====================================================================
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
        connectSSE();
    } catch (e) {
        console.error('init failed:', e);
    }
    document.getElementById('status').textContent = 'OK';
    document.getElementById('status').className = 'connected';
}

// ====================================================================
//  Refresh all (full reconciliation from REST API)
// ====================================================================
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

// ====================================================================
//  Refresh button
// ====================================================================
document.getElementById('refresh-btn')?.addEventListener('click', refreshAll);

// ====================================================================
//  Auto-refresh toggle
// ====================================================================
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

// Restore last auto-refresh state from localStorage (default OFF)
try {
    if (localStorage.getItem('auto-refresh') === '1') {
        setTimeout(startAutoRefresh, 500);
    }
} catch (e) { /* ignore */ }

// ====================================================================
//  Path-tracing modal
// ====================================================================
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
            let hops = '?';
            if (ev.hop_start && ev.hop_start > 0) {
                const remaining = ev.hop_limit || 0;
                const used = ev.hop_start >= remaining ? ev.hop_start - remaining : 0;
                hops = `${used}/${ev.hop_start}`;
            } else if (ev.hop_limit) {
                hops = `?/${ev.hop_limit}+`;
            }
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

// ====================================================================
//  Node detail modal
// ====================================================================
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

// ====================================================================
//  On-demand traceroute (TR button on Nodes table)
// ====================================================================
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

// ====================================================================
//  Misbehaving table inline action buttons (delegated handlers)
// ====================================================================
async function notifyNowForNode(nodeNum, btn) {
    if (!nodeNum) return;
    const orig = btn.textContent;
    btn.disabled = true;
    btn.textContent = '…';
    try {
        const hex = (nodeNum >>> 0).toString(16).padStart(8, '0');
        const r = await fetch(`/api/misbehaving/notify/${hex}`, { method: 'POST' });
        if (!r.ok) {
            const txt = await r.text();
            btn.textContent = '!';
            btn.title = txt;
            console.warn('notify-now:', txt);
        } else {
            const body = await r.json();
            btn.textContent = body.status === 'sent' ? '✓ sent'
                : body.status === 'dry-run' ? '✓ dry'
                : '✗';
            btn.title = body.status + ': ' + (body.text || '');
        }
    } catch (e) {
        console.error('notify-now:', e);
        btn.textContent = '!';
        btn.title = e.message;
    }
    setTimeout(() => { btn.textContent = orig; btn.disabled = false; btn.title = ''; }, 4000);
}

async function resetNodeMisbehave(nodeNum, btn) {
    if (!confirm('Reset this node? This clears its rate counters, flag streak and notification history (cooldown).')) return;
    const orig = btn.textContent;
    btn.disabled = true;
    btn.textContent = '…';
    try {
        const hex = (nodeNum >>> 0).toString(16).padStart(8, '0');
        const r = await fetch(`/api/misbehaving/reset/${hex}`, { method: 'POST' });
        if (!r.ok) {
            btn.textContent = '!';
            btn.title = await r.text();
        } else {
            btn.textContent = '✓';
            renderMisbehaving({ skipConfigFetch: true });
        }
    } catch (e) {
        console.error('reset node:', e);
        btn.textContent = '!';
        btn.title = e.message;
    }
    setTimeout(() => { btn.textContent = orig; btn.disabled = false; btn.title = ''; }, 2500);
}

document.querySelector('#misbehaving-table tbody')?.addEventListener('click', (e) => {
    const notifyBtn = e.target.closest('.misb-btn-notify');
    if (notifyBtn) {
        const num = parseInt(notifyBtn.dataset.nodeNum, 10);
        if (num) notifyNowForNode(num, notifyBtn);
        return;
    }
    const resetBtn = e.target.closest('.misb-btn-reset');
    if (resetBtn) {
        const num = parseInt(resetBtn.dataset.nodeNum, 10);
        if (num) resetNodeMisbehave(num, resetBtn);
    }
});

// ====================================================================
//  Heatmap modal close (cells are rendered by the overview module —
//  the event listeners on individual SVG rects are attached there;
//  only the static backdrop close is wired here).
// ====================================================================
document.getElementById('heatmap-modal-close')?.addEventListener('click', () => {
    document.getElementById('heatmap-modal').style.display = 'none';
});
document.getElementById('heatmap-modal')?.addEventListener('click', (e) => {
    if (e.target.id === 'heatmap-modal') e.target.style.display = 'none';
});

// ====================================================================
//  Expose commonly-used functions on window for inline HTML onclick
//  handlers and cross-module access (ES modules do not leak to window).
// ====================================================================
window.esc = esc;
window.nodeName = nodeName;
window.fmtTime = fmtTime;
window.fmtNum = fmtNum;
window.relativeTime = relativeTime;
window.shortTypeName = shortTypeName;
window.parseNodeNum = parseNodeNum;
window.openPacketPath = openPacketPath;
window.openNodeModal = openNodeModal;
window.sendTracerouteToNode = sendTracerouteToNode;
window.toggleAutoRefresh = toggleAutoRefresh;

// ====================================================================
//  Boot
// ====================================================================
init();
