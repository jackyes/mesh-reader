// === SSE real-time event stream ===
import { state } from './state.js';
import { api } from './api.js';
import { parseNodeNum, fmtNum, relativeTime, esc, nodeName } from './utils.js';
import { makeEventRow } from './overview.js';
import { makeMessageRow } from './messages.js';
import { renderSnifferRow } from './sniffer.js';
import { refreshTraceroutes, renderTracerouteList } from './network.js';

export function connectSSE() {
    if (state.eventSource) {
        state.eventSource.close();
    }

    var es = new EventSource('/api/events/stream');
    state.eventSource = es;

    es.onopen = function () {
        console.log('[SSE] connected — live events active');
        state.eventSourceReconnect = 0;
        var el = document.getElementById('sse-status');
        if (el) { el.className = 'sse-status connected'; el.title = 'SSE connected — receiving live events'; }
    };

    es.onmessage = function (e) {
        try {
            var ev = JSON.parse(e.data);
            // Update live counter badge
            state._sseCount = (state._sseCount || 0) + 1;
            var badge = document.getElementById('sse-status');
            if (badge && state._sseCount % 5 === 0) {
                badge.title = 'SSE: ' + state._sseCount + ' events received';
            }
            handleRealtimeEvent(ev);
        } catch (err) {
            console.warn('[SSE] parse error:', err);
        }
    };

    es.onerror = function () {
        console.warn('[SSE] error, will auto-reconnect');
        state.eventSourceReconnect++;
        var el = document.getElementById('sse-status');
        if (el) el.className = 'sse-status reconnecting';
        // EventSource si riconnette automaticamente, ma se troppi errori:
        if (state.eventSourceReconnect > 10) {
            es.close();
            state.eventSource = null;
            // Fallback al polling dopo 30 secondi
            setTimeout(function () {
                if (!state.eventSource) {
                    connectSSE();
                }
            }, 30000);
        }
    };
}

export function handleRealtimeEvent(ev) {
    try {
        _handleRealtimeEvent(ev);
    } catch (e) {
        console.error('[SSE] handleRealtimeEvent error:', e);
    }
}

export function _handleRealtimeEvent(ev) {
    // Aggiungi al ring buffer locale per lo sniffer
    if (state.snifferRing) {
        state.snifferRing.push(ev);
        if (state.snifferRing.length > 500) state.snifferRing.shift();
    }

    // Aggiorna il tab attivo
    if (state.activeTab === 'sniffer' && !state.snifferPaused) {
        appendSnifferRow(ev);
    }

    // Aggiorna la lista eventi nel tab Overview
    if (state.activeTab === 'overview') {
        prependOverviewEvent(ev);
    }

    // Aggiorna il tab Messages: solo messaggi di testo
    if (state.activeTab === 'messages' && ev.type === 'TEXT_MESSAGE') {
        appendMessageRow(ev);
    }

    // Aggiorna il tab Network: ricarica traceroute se arriva un TRACEROUTE
    if (state.activeTab === 'network' && ev.type === 'TRACEROUTE') {
        // Debounce: max one reload per second
        if (!state._lastTrReload || Date.now() - state._lastTrReload > 1000) {
            state._lastTrReload = Date.now();
            refreshTraceroutes().then(() => renderTracerouteList());
        }
    }

    // Aggiorna contatori/stats in modo incrementale

    // Aggiorna nodo se l'evento è relativo a un nodo
    if (ev.from_num) {
        updateNodeFromEvent(ev.from_num, ev);
    }
    // ev.to is a hex string like "!a1b2c3d4"; skip broadcast (^all)
    if (ev.to && ev.to !== '^all' && ev.from_num) {
        // parseNodeNum from hex string like "!a1b2c3d4" or "!ffffffff"
        var toNum = parseNodeNum(ev.to);
        if (toNum && toNum !== 4294967295) {
            updateNodeFromEvent(toNum, ev);
        }
    }

    // Aggiorna badge/live indicator
    updateLiveIndicator();
}

export function appendSnifferRow(ev) {
    var tbody = document.querySelector('#sniffer-table tbody');
    if (!tbody) return;

    // Apply active filters
    var cls = document.getElementById('sniffer-class').value;
    if (cls && ev.class !== cls) return;
    var typeFilter = document.getElementById('sniffer-type').value.trim().toUpperCase();
    if (typeFilter && (ev.type || '').toUpperCase() !== typeFilter) return;
    var fromFilter = document.getElementById('sniffer-from').value.trim().replace(/^!/, '');
    if (fromFilter && ev.from !== fromFilter && ev.from_num !== parseInt(fromFilter, 16)) return;
    var toFilter = document.getElementById('sniffer-to').value.trim().replace(/^!/, '');
    if (toFilter && ev.to !== toFilter) return;

    renderSnifferRow(tbody, ev);

    // Flash animation on newly arrived SSE row
    var lastRow = tbody.lastElementChild;
    if (lastRow) {
        lastRow.classList.add('new-row');
        setTimeout(function() { if (lastRow) lastRow.classList.remove('new-row'); }, 1500);
    }

    // Keep the table from growing unbounded
    while (tbody.children.length > 500) {
        if (tbody.firstChild) tbody.removeChild(tbody.firstChild);
    }

    var cnt = document.getElementById('sniffer-count');
    if (cnt) cnt.textContent = tbody.children.length + ' packets';
}

// prependOverviewEvent inserts a new event row at the top of the Overview
// "Recent Events" table, keeping the total capped.
export function prependOverviewEvent(ev) {
    var tbody = document.querySelector('#recent-events tbody');
    if (!tbody) return;
    var row = makeEventRow(ev);
    row.classList.add('new-row');
    tbody.insertBefore(row, tbody.firstChild);
    setTimeout(function() { row.classList.remove('new-row'); }, 1500);
    // Cap at 100 rows in the Overview table
    while (tbody.children.length > 100) {
        if (tbody.lastChild) tbody.removeChild(tbody.lastChild);
    }
    // Lightweight: update only the total-events counter and class strip
    var el = document.getElementById('stat-events');
    if (el && state.stats) {
        state.stats.total_events = (state.stats.total_events || 0) + 1;
        el.textContent = fmtNum(state.stats.total_events);
    }
    // Update class count for this event's class
    if (ev.class && state.stats && state.stats.class_counts) {
        state.stats.class_counts[ev.class] = (state.stats.class_counts[ev.class] || 0) + 1;
    }
}

// appendMessageRow inserts a new text message at the top of the Messages
// table and pushes it into state.messages so it survives re-renders.
export function appendMessageRow(ev) {
    var tbody = document.querySelector('#messages-table tbody');
    if (!tbody) return;
    // Push into state so sort/refresh sees it
    state.messages = state.messages || [];
    state.messages.unshift(ev);
    if (state.messages.length > 500) state.messages.pop();
    // Insert row at top
    var row = makeMessageRow(ev);
    row.classList.add('new-row');
    tbody.insertBefore(row, tbody.firstChild);
    setTimeout(function() { row.classList.remove('new-row'); }, 1500);
    while (tbody.children.length > 200) {
        if (tbody.lastChild) tbody.removeChild(tbody.lastChild);
    }
}

export function updateNodeFromEvent(nodeNum, ev) {
    if (!nodeNum || !state.nodes[nodeNum]) return;
    var n = state.nodes[nodeNum];
    // Update last_heard
    if (ev.time) {
        var t = new Date(ev.time).getTime() / 1000;
        if (t > (n.last_heard || 0)) n.last_heard = t;
    }
    // Update signal values
    if (ev.rssi != null) n.rssi = ev.rssi;
    if (ev.snr != null) n.snr = ev.snr;

    // If nodes tab is active, update the specific row incrementally
    if (state.activeTab === 'nodes') {
        var rows = document.querySelectorAll('#nodes-table tbody tr');
        for (var i = 0; i < rows.length; i++) {
            var link = rows[i].querySelector('.node-name-link');
            if (link && parseInt(link.dataset.nodeNum, 10) === nodeNum) {
                // Update signal cell (column 6, 0-indexed)
                var cells = rows[i].querySelectorAll('td');
                if (cells.length > 6) {
                    var sigCell = cells[6];
                    if (sigCell) {
                        var rssiStr = (ev.rssi != null) ? ev.rssi + ' dBm' : '';
                        var snrStr = (ev.snr != null) ? ev.snr.toFixed(1) + ' dB' : '';
                        sigCell.innerHTML = '<span class="signal-val rssi">' + rssiStr + '</span> / <span class="signal-val snr">' + snrStr + '</span>';
                    }
                }
                // Update last_heard cell (column 5, 0-indexed)
                if (cells.length > 5 && ev.time) {
                    cells[5].textContent = relativeTime(n.last_heard);
                }
                break;
            }
        }
    }
}

export function updateLiveIndicator() {
    var el = document.getElementById('sse-status');
    if (!el) return;
    if (state.eventSource && state.eventSource.readyState === EventSource.OPEN) {
        el.className = 'sse-status connected';
    } else if (state.eventSource && state.eventSource.readyState === EventSource.CONNECTING) {
        el.className = 'sse-status reconnecting';
    } else {
        el.className = 'sse-status disconnected';
    }
}
