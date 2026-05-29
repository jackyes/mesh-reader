// === Sniffer tab ===
import { state } from './state.js';
import { api } from './api.js';
import { esc } from './utils.js';
import { sniffer } from './state.js';

// Re-export so other modules can reference sniffer
export { sniffer };

export function initSniffer() {
    if (sniffer.rendered) {
        loadSniffer();
        return;
    }
    sniffer.rendered = true;
    document.getElementById('sniffer-refresh').addEventListener('click', loadSniffer);
    document.getElementById('sniffer-livetail').addEventListener('change', e => {
        if (e.target.checked) startSnifferTail(); else stopSnifferTail();
    });
    ['sniffer-class', 'sniffer-type', 'sniffer-from', 'sniffer-to', 'sniffer-channel', 'sniffer-limit']
        .forEach(id => {
            const el = document.getElementById(id);
            if (!el) return;
            el.addEventListener('keydown', ev => { if (ev.key === 'Enter') loadSniffer(); });
            el.addEventListener('change', loadSniffer);
        });
    loadSniffer();
}

export function startSnifferTail() {
    stopSnifferTail();
    // SSE handles real-time; slow poll is fallback reconciliation.
    sniffer.timer = setInterval(loadSniffer, 30000);
    console.log('[sniffer] live tail active — SSE appending new rows, poll fallback every 30s');
}
export function stopSnifferTail() {
    if (sniffer.timer) { clearInterval(sniffer.timer); sniffer.timer = null; }
}

function snifferQuery() {
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

async function loadSniffer() {
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

export function renderSnifferRow(tbody, r) {
    const tr = document.createElement('tr');
    const time = r.time ? new Date(r.time).toLocaleTimeString() : '';
    const cls = r.class || 'unknown';
    const fromCell = r.from_name ? `${esc(r.from_name)} <span class="th-hint">${esc(r.from || '')}</span>`
                                 : esc(r.from || '');
    const toCell   = r.to_name   ? `${esc(r.to_name)} <span class="th-hint">${esc(r.to || '')}</span>`
                                 : (r.to === '^all' ? '<span class="th-hint">^all</span>' : esc(r.to || ''));
    const hop = (r.hop_start ? `${r.hop_start - r.hop_limit}/${r.hop_start}` : (r.hop_limit ?? ''));
    const relay = r.relay_node || '';
    const ch = (typeof r.channel === 'number') ? r.channel : '';
    tr.innerHTML = `
        <td>${time}</td>
        <td><span class="class-badge ${esc(cls)}">${esc(cls)}</span></td>
        <td>${esc(r.type || '')}</td>
        <td>${fromCell}</td>
        <td>${toCell}</td>
        <td>${r.rssi ?? ''}</td>
        <td>${r.snr != null ? r.snr.toFixed(1) : ''}</td>
        <td>${hop}</td>
        <td>${ch}</td>
        <td>${esc(relay)}</td>
        <td><button class="expand-btn" type="button">+</button></td>
    `;
    const btn = tr.querySelector('.expand-btn');
    let detailTr = null;
    btn.addEventListener('click', () => {
        if (detailTr) {
            detailTr.remove();
            detailTr = null;
            btn.textContent = '+';
        } else {
            detailTr = document.createElement('tr');
            detailTr.className = 'detail-row';
            const payload = { ...r };
            // Strip already-visible cells from the JSON dump to reduce noise.
            ['time','class','type','from','from_name','from_num','to','to_name','rssi','snr','hop_limit','hop_start','channel','relay_node'].forEach(k => delete payload[k]);
            detailTr.innerHTML = `<td colspan="11"><pre>${esc(JSON.stringify(payload, null, 2))}</pre></td>`;
            tr.after(detailTr);
            btn.textContent = '−';
        }
    });
    tbody.appendChild(tr);
}
