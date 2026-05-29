// Mesh Reader Dashboard v2 — API communication functions

import { state } from './state.js';

export async function api(path) {
    const r = await fetch(path);
    return r.json();
}

export async function loadSniffer() {
    let rows = [];
    try {
        rows = await api('/api/sniffer?' + snifferQuery());
    } catch (e) {
        console.error('sniffer load:', e);
        return;
    }
    const tbody = document.querySelector('#sniffer-table tbody');
    if (!tbody) return;
    tbody.innerHTML = '';
    (rows || []).forEach(r => renderSnifferRow(tbody, r));
    const cnt = document.getElementById('sniffer-count');
    if (cnt) cnt.textContent = `${(rows || []).length} packets`;
}

export function snifferQuery() {
    const params = new URLSearchParams();
    const cls   = document.getElementById('sniffer-class').value;
    const type  = document.getElementById('sniffer-type').value.trim();
    const from  = document.getElementById('sniffer-from').value.trim();
    const to    = document.getElementById('sniffer-to').value.trim();
    const ch    = document.getElementById('sniffer-channel').value.trim();
    const lim   = document.getElementById('sniffer-limit').value.trim();
    if (cls)  params.set('class', cls);
    if (type) params.set('type', type.toUpperCase());
    if (from) params.set('from', from.replace(/^!/, ''));
    if (to)   params.set('to', to.replace(/^!/, ''));
    if (ch)   params.set('channel', ch);
    if (lim)  params.set('limit', lim);
    return params.toString();
}
