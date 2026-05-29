// === Messages tab ===
import { state } from './state.js';
import { esc, fmtTime, nodeName, makeRelayTag } from './utils.js';
import { updateSortIndicators } from './nodes.js';

// ---- Messages ----
export function renderMessages() {
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

export function makeMessageRow(ev) {
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

export function msgSortVal(m, key) {
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
