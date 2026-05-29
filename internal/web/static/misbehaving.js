// === Misbehaving Nodes tab ===
import { state } from './state.js';
import { api } from './api.js';
import { esc } from './utils.js';
import { roleBadge, roleSortWeight } from './nodes.js';
import { MISB_METRICS } from './state.js';

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
export function ensureMisbSort() {
    if (!state.sort) state.sort = {};
    if (!state.sort.misbehaving) state.sort.misbehaving = { key: 'excess', dir: -1 };
}

// Cache of the most recent rendered Misbehaving report — used by the
// template preview and by the per-row "Notify now" action so the JS
// doesn't have to re-fetch.
state.lastMisbReport = null;

export function misbConfigToForm(cfg) {
    MISB_METRICS.forEach(m => {
        const tile = document.querySelector(`.misb-tile[data-metric="${m.ui}"]`);
        if (!tile) return;
        tile.querySelector('[data-field="enabled"]').checked   = !!cfg[m.enabled];
        tile.querySelector('[data-field="count"]').value       = cfg[m.count] ?? 0;
        tile.querySelector('[data-field="window_min"]').value  = Math.max(5, Math.round((cfg[m.win] ?? 3600) / 60));
        tile.classList.toggle('misb-tile-off', !cfg[m.enabled]);
    });
}

export function misbFormToConfig() {
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

export function setMisbStatus(text, kind) {
    const el = document.getElementById('misb-status');
    if (!el) return;
    el.textContent = text || '';
    el.className = 'misb-status' + (kind ? ' misb-status-' + kind : '');
    if (text) {
        clearTimeout(el._t);
        el._t = setTimeout(() => { el.textContent = ''; el.className = 'misb-status'; }, 4000);
    }
}

export async function applyMisbConfig(persist) {
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

export async function resetMisbForm() {
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
        tbody.innerHTML = '<tr><td colspan="12" style="padding:1rem;color:var(--red)">Unable to load misbehaving nodes.</td></tr>';
        card.style.display = '';
        empty.style.display = 'none';
        return;
    }
    const cfg   = (rep && rep.config) || {};
    const nodes = (rep && rep.nodes)  || [];
    state.lastMisbReport = rep;
    // Also push the active config into the notify panel form so the user
    // sees notify settings in sync with the threshold sliders above.
    misbConfigToNotifyForm(cfg);
    misbUpdateNotifyPreview();

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
            <td class="misb-num misb-notif">${formatNotifCount(n.notifications_sent | 0)}</td>
            <td class="misb-next">${formatNextNotify(n, now)}</td>
            <td class="misb-actions-cell">
                <button class="misb-btn misb-btn-notify" data-node-num="${n.node_num}" title="Send a one-off DM to this node now (honors dry-run flag)">Notify</button>
                <button class="misb-btn misb-btn-reset" data-node-num="${n.node_num}" title="Clear cooldown + rate buckets + flag streak for this node — it will disappear from the list until fresh packets push it back over a threshold">Reset</button>
            </td>
        </tr>`;
    }).join('');
}

// misbSortVal — total "excess" = sum of (count - threshold) for each
// breached metric. Higher = more egregious offender.
export function misbSortVal(n, key, cfg) {
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
        case 'notifications_sent': return n.notifications_sent | 0;
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

// ---- Misbehaving auto-notify panel ----
// The panel mirrors the active config. Apply / Save-as-default reuse
// the same /api/misbehaving/config endpoint as the threshold tiles, so
// the four notify fields are persisted in the same JSON file. The
// recent-notifications table reads from /api/misbehaving/notifications
// (DB-backed audit log).
export function misbConfigToNotifyForm(cfg) {
    const set = (id, val) => {
        const el = document.getElementById(id);
        if (el) {
            if (el.type === 'checkbox') el.checked = !!val;
            else el.value = val ?? '';
        }
    };
    set('misb-notify-enabled',  cfg.notify_enabled);
    set('misb-notify-dryrun',   cfg.notify_dry_run);
    set('misb-notify-cooldown', cfg.notify_cooldown_hours);
    set('misb-notify-rate',     cfg.notify_max_per_hour);
    set('misb-notify-mafa',     cfg.notify_min_flag_age_sec);
    set('misb-notify-channel',  cfg.notify_channel);
    set('misb-notify-hops',     cfg.notify_hop_limit);
    const tplEl = document.getElementById('misb-notify-template');
    if (tplEl && cfg.notify_template !== undefined) tplEl.value = cfg.notify_template;
}

export function misbNotifyFormToPartialCfg() {
    const get = id => document.getElementById(id);
    const num = (id, def) => {
        const el = get(id); if (!el) return def;
        const v = parseInt(el.value, 10); return isNaN(v) ? def : v;
    };
    return {
        notify_enabled:           !!get('misb-notify-enabled')?.checked,
        notify_dry_run:           !!get('misb-notify-dryrun')?.checked,
        notify_cooldown_hours:    num('misb-notify-cooldown', 24),
        notify_max_per_hour:      num('misb-notify-rate', 5),
        notify_min_flag_age_sec:  num('misb-notify-mafa', 1800),
        notify_channel:           num('misb-notify-channel', 0),
        notify_hop_limit:         num('misb-notify-hops', 3),
        notify_template:          get('misb-notify-template')?.value || '',
    };
}

// buildIssueText mirrors the server-side store.BuildIssueText() so the
// preview shows exactly what will be sent. Uses the form values for the
// active thresholds (so editing a tile updates the preview live).
export function buildIssueText(sample, cfg) {
    const parts = [];
    if (cfg.node_info_enabled && sample.node_info_count > cfg.node_info_count) {
        parts.push(`sending too many NodeInfo packets (${sample.node_info_count} in ${Math.round(cfg.node_info_window_sec/60)}min). Increase nodeinfo.broadcast_secs.`);
    }
    if (cfg.telemetry_enabled && sample.telemetry_count > cfg.telemetry_count) {
        parts.push(`sending too many telemetry packets (${sample.telemetry_count} in ${Math.round(cfg.telemetry_window_sec/60)}min). Increase telemetry.device_update_interval.`);
    }
    if (cfg.position_enabled && sample.position_count > cfg.position_count) {
        parts.push(`sending too many position updates (${sample.position_count} in ${Math.round(cfg.position_window_sec/60)}min). Increase position.broadcast_secs or broadcast_smart_minimum_distance.`);
    }
    if (cfg.max_hop_enabled && (sample.hop_start_mode|0) > cfg.max_hop_value) {
        parts.push(`using an excessive hop limit (hop_limit=${sample.hop_start_mode}, recommended ${cfg.max_hop_value}). Set lora.hop_limit=${cfg.max_hop_value} to reduce mesh airtime.`);
    }
    if (parts.length === 0) return (sample.reasons || []).join('; ');
    return parts.join(' ');
}

// misbUpdateNotifyPreview replaces placeholders in the template with the
// values of the first flagged node so the user sees what will actually
// be sent. Falls back to a synthetic example when the report is empty.
export function misbUpdateNotifyPreview() {
    const el = document.getElementById('misb-notify-preview');
    const tpl = (document.getElementById('misb-notify-template')?.value) || '';
    if (!el) return;
    const rep = state.lastMisbReport;
    const sample = (rep && rep.nodes && rep.nodes[0]) || {
        short_name: 'XYZ', long_name: 'Esempio Nodo', id: '!12345678',
        reasons: ['Hop mode 7 / 60m (>5)'],
        node_info_count: 0, telemetry_count: 0, position_count: 0,
        hop_start_mode: 7,
    };
    const me = state.localNode || {};
    const reasons = (sample.reasons || []).join('; ');
    // Build the active config from BOTH the threshold tiles and the
    // notify form so preview matches exactly what backend will compute.
    const cfg = Object.assign(
        misbFormToConfig ? misbFormToConfig() : {},
        misbNotifyFormToPartialCfg()
    );
    // For the synthetic example, fall back to defaults so {issue} renders
    // even before the user clicked Apply on the threshold tiles.
    if (!('max_hop_enabled' in cfg))  { cfg.max_hop_enabled = true; cfg.max_hop_value = 5; cfg.max_hop_window_sec = 3600; }
    if (!('node_info_enabled' in cfg)) { cfg.node_info_enabled = true; cfg.node_info_count = 2; cfg.node_info_window_sec = 3600; }
    if (!('telemetry_enabled' in cfg)) { cfg.telemetry_enabled = true; cfg.telemetry_count = 2; cfg.telemetry_window_sec = 3600; }
    if (!('position_enabled' in cfg))  { cfg.position_enabled = true;  cfg.position_count = 15;  cfg.position_window_sec = 3600; }
    const issue = buildIssueText(sample, cfg);
    const out = tpl
        .replaceAll('{short}',   sample.short_name || sample.id || '')
        .replaceAll('{long}',    sample.long_name  || sample.id || '')
        .replaceAll('{id}',      sample.id || '')
        .replaceAll('{issue}',   issue)
        .replaceAll('{reasons}', reasons)
        .replaceAll('{me}',      me.short_name || '')
        .replaceAll('{me_long}', me.long_name  || '');
    const truncated = out.length > 200 ? out.slice(0, 199) + '…' : out;
    el.textContent = truncated;
    el.classList.toggle('misb-notify-preview-truncated', out.length > 200);
}

export async function applyMisbNotifyConfig(persist) {
    // Merge notify fields into the current threshold config so the
    // POST is a complete record (the backend Sanitize() call on the
    // server side preserves whatever else is already in the cfg).
    let cfg;
    try { cfg = await api('/api/misbehaving/config'); }
    catch (e) { cfg = {}; }
    Object.assign(cfg, misbNotifyFormToPartialCfg());
    try {
        const url = '/api/misbehaving/config' + (persist ? '?save=1' : '');
        const r = await fetch(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(cfg),
        });
        const body = await r.json();
        if (body && body.config) misbConfigToNotifyForm(body.config);
        const statusEl = document.getElementById('misb-notify-status');
        if (statusEl) {
            if (persist) {
                if (body.saved) {
                    statusEl.textContent = 'Saved as default ✓';
                    statusEl.className = 'misb-status misb-status-ok';
                } else {
                    statusEl.textContent = 'Applied (save failed: ' + (body.save_error || 'unknown') + ')';
                    statusEl.className = 'misb-status misb-status-warn';
                }
            } else {
                statusEl.textContent = 'Applied ✓';
                statusEl.className = 'misb-status misb-status-ok';
            }
            clearTimeout(statusEl._t);
            statusEl._t = setTimeout(() => { statusEl.textContent = ''; statusEl.className = 'misb-status'; }, 4000);
        }
    } catch (e) {
        console.error('notify cfg apply:', e);
    }
}

export async function notifyNowForNode(nodeNum, btn) {
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
            refreshMisbNotifyLog();
        }
    } catch (e) {
        console.error('notify-now:', e);
        btn.textContent = '!';
        btn.title = e.message;
    }
    setTimeout(() => { btn.textContent = orig; btn.disabled = false; btn.title = ''; }, 4000);
}

export async function refreshMisbNotifyLog() {
    const el = document.getElementById('misb-notify-log');
    if (!el) return;
    try {
        const log = await api('/api/misbehaving/notifications?limit=20');
        if (!log || log.length === 0) {
            el.innerHTML = '<div class="text-dim" style="padding:0.5rem 0">No notifications yet.</div>';
            return;
        }
        el.innerHTML = log.map(row => {
            const t = new Date(row.time * 1000).toLocaleString('it-IT', {
                day: '2-digit', month: '2-digit', year: '2-digit',
                hour: '2-digit', minute: '2-digit',
            });
            const statusCls = row.status === 'sent' ? 'misb-log-sent'
                : row.status === 'dry-run' ? 'misb-log-dry'
                : row.status.startsWith('failed') ? 'misb-log-fail'
                : row.status.startsWith('nack') ? 'misb-log-nack'
                : 'misb-log-skip';
            const statusLabel = row.status.length > 24 ? row.status.slice(0, 24) + '…' : row.status;
            const node = state.nodes[row.node_num];
            const name = (node && (node.long_name || node.short_name)) || row.id || `!${(row.node_num >>> 0).toString(16).padStart(8, '0')}`;
            return `<div class="misb-log-row">
                <span class="misb-log-time">${esc(t)}</span>
                <span class="misb-log-status ${statusCls}">${esc(statusLabel)}</span>
                <span class="misb-log-name" title="${esc(row.id || '')}">${esc(name)}</span>
                <span class="misb-log-text" title="${esc(row.text || '')}">${esc(row.text || '')}</span>
            </div>`;
        }).join('');
    } catch (e) {
        console.error('notify log:', e);
    }
}

export async function resetNodeMisbehave(nodeNum, btn) {
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
            // Re-render the table — the node should disappear (or show
            // status=ready if other thresholds still apply).
            renderMisbehaving({ skipConfigFetch: true });
        }
    } catch (e) {
        console.error('reset node:', e);
        btn.textContent = '!';
        btn.title = e.message;
    }
    setTimeout(() => { btn.textContent = orig; btn.disabled = false; btn.title = ''; }, 2500);
}

// formatNotifCount renders the per-row "Notif" lifetime counter as a
// muted dash when zero (so the column is easy to scan for nodes that
// have actually been notified) and a colored pill when > 0. Color
// band: 1-2 cyan (informational), 3-5 yellow (repeat offender), 6+
// orange (chronic — your messages aren't moving the needle).
export function formatNotifCount(n) {
    if (!n) return '<span class="ln-dash">—</span>';
    let cls = 'ntf-low';
    if (n >= 6) cls = 'ntf-high';
    else if (n >= 3) cls = 'ntf-mid';
    return `<span class="ntf ${cls}" title="${n} notification${n !== 1 ? 's' : ''} sent or dry-run">${n}</span>`;
}

// formatNextNotify renders the per-row "Next notify" cell. The pill
// color encodes the scheduler state so the user can scan the column at
// a glance: green=ready, blue=cooldown, yellow=grace, orange=rate-hit,
// grey=disabled.
export function formatNextNotify(n, now) {
    const status = n.notify_status || '';
    if (!status || status === 'disabled') {
        return '<span class="nn nn-off" title="Auto-notify disabled">off</span>';
    }
    if (status === 'ready') {
        return '<span class="nn nn-ready" title="Eligible at the next scheduler tick (≤60s)">ready</span>';
    }
    const eta = Math.max(0, (n.next_eligible_at | 0) - now);
    const etaTxt = formatDurationShort(eta);
    if (status === 'cooldown') {
        return `<span class="nn nn-cool" title="In cooldown after a previous notification">cooldown ${etaTxt}</span>`;
    }
    if (status === 'grace') {
        return `<span class="nn nn-grace" title="Min flag age not yet reached — avoiding knee-jerk on transient spikes">grace ${etaTxt}</span>`;
    }
    if (status === 'rate-limit') {
        return `<span class="nn nn-rate" title="Global per-hour rate limit reached; waits for an old slot to roll out">rate-limit ${etaTxt}</span>`;
    }
    return `<span class="nn">${esc(status)} ${etaTxt}</span>`;
}

// formatDurationShort: "12s", "5m", "1h23m", "2d3h" — compact for table cells.
export function formatDurationShort(sec) {
    sec = Math.max(0, sec | 0);
    if (sec < 60) return sec + 's';
    const m = Math.floor(sec / 60);
    if (m < 60) return m + 'm';
    const h = Math.floor(m / 60);
    const remM = m % 60;
    if (h < 24) return remM > 0 ? `${h}h${remM}m` : `${h}h`;
    const d = Math.floor(h / 24);
    const remH = h % 24;
    return remH > 0 ? `${d}d${remH}h` : `${d}d`;
}

// refreshNotifyLiveStatus polls /api/misbehaving/notify-status and
// populates the live status line. Called on render + every 10s while
// the Misbehaving tab is open.
let _notifyStatusTimer = null;
export async function refreshNotifyLiveStatus() {
    const el = document.getElementById('misb-notify-livestatus');
    if (!el) return;
    try {
        const st = await api('/api/misbehaving/notify-status');
        if (!st || !st.enabled) {
            el.style.display = 'none';
            return;
        }
        const rateBadge = st.sent_last_hour >= st.max_per_hour
            ? `<span class="ls-bad">${st.sent_last_hour}/${st.max_per_hour}</span>`
            : st.sent_last_hour > st.max_per_hour * 0.7
            ? `<span class="ls-warn">${st.sent_last_hour}/${st.max_per_hour}</span>`
            : `<span class="ls-good">${st.sent_last_hour}/${st.max_per_hour}</span>`;
        const dryBadge = st.dry_run ? '<span class="ls-dry">DRY-RUN</span>' : '';
        const slotTxt = st.next_slot_in_sec > 0
            ? ` · next slot in <b>${formatDurationShort(st.next_slot_in_sec)}</b>`
            : '';
        const cooldownTxt = st.cooldown_active > 0 ? ` · cooldown <b>${st.cooldown_active}</b>` : '';
        const graceTxt = st.grace_active > 0 ? ` · grace <b>${st.grace_active}</b>` : '';
        const readyTxt = st.ready_now > 0 ? ` · ready <b>${st.ready_now}</b>` : '';
        const nextTxt = (st.next_eligible_sec > 0 && st.next_eligible_node)
            ? ` · next <b>${esc(st.next_eligible_node)}</b> in <b>${formatDurationShort(st.next_eligible_sec)}</b>`
            : '';
        el.innerHTML = `🔔 Rate ${rateBadge} sent in last hour${slotTxt}${cooldownTxt}${graceTxt}${readyTxt}${nextTxt} ${dryBadge}`;
        el.style.display = '';
    } catch (e) {
        el.style.display = 'none';
    }
    // Schedule next poll only while the Misbehaving tab is active.
    clearTimeout(_notifyStatusTimer);
    if (state.activeTab === 'misbehaving') {
        _notifyStatusTimer = setTimeout(refreshNotifyLiveStatus, 10000);
    }
}

// The original renderMisbehaving reference — needed for the monkey-patch
// pattern where renderMisbehaving is wrapped to also call
// refreshMisbNotifyLog and refreshNotifyLiveStatus after the main render.
export const _origRenderMisbehaving = renderMisbehaving;

// Wrapped version: after rendering the misbehaving table, also refresh
// the notification log and live status.
export async function _wrappedRenderMisbehaving(opts) {
    await _origRenderMisbehaving.call(this, opts);
    refreshMisbNotifyLog();
    refreshNotifyLiveStatus();
}

// Alias so app.js can call the wrapped version directly.
export { _wrappedRenderMisbehaving as renderMisbehaving };
