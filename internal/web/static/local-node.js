// === Local Node tab ===
import { state } from './state.js';
import { api } from './api.js';
import { esc, fmtUptime, fmtBytes, kvRows, boolBadge, relativeTime, sparkline } from './utils.js';
import { roleBadge } from './nodes.js';
import { renderNeighborsSection } from './map.js';

// ---- Local node page ----
// The "My Node" tab shows information about the Meshtastic device we're
// physically connected to over serial. The data is assembled by the Go
// side from several boot-time packets (MY_INFO, NODE_INFO for own id,
// METADATA, CONFIG LoRa) into the /api/local-node endpoint. Fields can
// appear incrementally — we render whatever is present and leave the
// rest as "-" so the page is useful even during the first seconds.
export async function renderLocalNode() {
    const idEl   = document.getElementById('ln-identity');
    const fwEl   = document.getElementById('ln-firmware');
    const loraEl = document.getElementById('ln-lora');
    const capsEl = document.getElementById('ln-caps');
    const titleName = document.getElementById('ln-title-name');
    const titleRole = document.getElementById('ln-title-role');
    const subtitle  = document.getElementById('ln-subtitle');
    if (!idEl || !fwEl || !loraEl || !capsEl) return;

    let ln, hist = {}, hopscale = {};
    try {
        // History (sparklines) and hop-scale (debug-log) are best-effort —
        // never let them block the page.
        [ln, hist, hopscale] = await Promise.all([
            api('/api/local-node'),
            api('/api/local-node/history').catch(() => ({})),
            api('/api/local-node/hopscale').catch(() => ({})),
        ]);
    } catch (e) {
        console.error('local-node fetch:', e);
        idEl.innerHTML = '<div class="ln-empty">Unable to load local node info.</div>';
        fwEl.innerHTML = '';
        loraEl.innerHTML = '';
        capsEl.innerHTML = '';
        return;
    }

    // spark(field, color) → inline sparkline SVG for a captured-metric series,
    // or '' when there are too few samples yet.
    const spark = (field, color) => {
        const svg = sparkline((hist[field] || []).map(p => p.v), { color });
        return svg ? ` ${svg}` : '';
    };

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
    if (ln.noise_floor_dbm) {
        const ago = ln.noise_floor_at ? ` <span class="th-hint">(${relativeTime(ln.noise_floor_at)})</span>` : '';
        loraRows.push(['Noise floor', `${ln.noise_floor_dbm} dBm${ago}${spark('noise_floor_dbm', '#f59e0b')}`, { raw: true }]);
    }
    loraEl.innerHTML = kvRows(loraRows);

    // Capabilities
    capsEl.innerHTML = kvRows([
        ['Wi-Fi',       boolBadge(ln.has_wifi)],
        ['Bluetooth',   boolBadge(ln.has_bluetooth)],
        ['PKC',         boolBadge(ln.has_pkc)],
        ['Can shutdown', boolBadge(ln.can_shutdown)],
    ], { raw: true });

    // NeighborInfo module — render the full block including a warning
    // banner when the module is disabled (in which case the firmware
    // never forwards NeighborInfo packets to the serial client).
    const niBody = document.getElementById('ln-neighbor-info-body');
    const niCard = document.getElementById('ln-neighbor-info-card');
    if (niBody && niCard) {
        if (!ln.neighbor_info_module_known) {
            niCard.classList.remove('ln-warn-banner', 'ln-ok-banner');
            niBody.innerHTML = '<div class="ln-empty">Waiting for ModuleConfig.NeighborInfo from the device…</div>';
        } else if (ln.neighbor_info_enabled) {
            niCard.classList.add('ln-ok-banner');
            niCard.classList.remove('ln-warn-banner');
            niBody.innerHTML = `
                <div class="ln-banner ln-banner-ok">
                    <span class="ln-banner-icon">&#10003;</span>
                    <span><b>Module enabled</b> — NeighborInfo packets received over the air are forwarded to the dashboard.</span>
                </div>
                <div class="ln-kv" style="margin-top:0.6rem">
                    ${kvRows([
                        ['Update interval', ln.neighbor_info_update_interval_sec ? `${ln.neighbor_info_update_interval_sec} s (${Math.round(ln.neighbor_info_update_interval_sec / 60)} min)` : ''],
                        ['Transmit over LoRa', boolBadge(ln.neighbor_info_transmit_over_lora)],
                    ], { raw: true })}
                </div>`;
        } else {
            niCard.classList.add('ln-warn-banner');
            niCard.classList.remove('ln-ok-banner');
            niBody.innerHTML = `
                <div class="ln-banner ln-banner-warn">
                    <span class="ln-banner-icon">&#9888;</span>
                    <div>
                        <b>Module DISABLED on this device.</b>
                        The Meshtastic firmware silently drops every <code>NEIGHBORINFO_APP</code> packet
                        it receives over the air, so the dashboard cannot collect any neighbor data
                        even if other nodes broadcast it.<br>
                        <span class="ln-hint">Enable it in the Meshtastic app/CLI:
                            <code>meshtastic --set neighbor_info.enabled true</code>
                            (set <code>update_interval</code> to <code>0</code> if you only want to
                            <i>receive</i> without transmitting your own).
                        </span>
                    </div>
                </div>`;
        }
    }

    // Hop Scaling & Traffic Management — two sources merged into one card:
    //  • live state parsed from the firmware [HOPSCALE]/[TM] debug log
    //    (/api/local-node/hopscale) — present on builds that run the modules
    //  • cumulative counters from TrafficManagementStats telemetry — only if
    //    the node actually emits that telemetry variant (many builds don't)
    // Hidden only when neither source has produced any data.
    const tmCard = document.getElementById('ln-traffic-card');
    const tmBody = document.getElementById('ln-traffic-body');
    const tmAge  = document.getElementById('ln-traffic-age');
    if (tmCard && tmBody) {
        const hq = hopscale || {};
        const hasLiveHop  = !!hq.updated_at;
        const hasTMCache  = !!hq.tm_cache_at;
        const hasCounters = !!ln.traffic_stats_at;
        if (!hasLiveHop && !hasTMCache && !hasCounters) {
            tmCard.style.display = 'none';
        } else {
            tmCard.style.display = '';
            const newest = Math.max(hq.updated_at || 0, hq.tm_cache_at || 0, ln.traffic_stats_at || 0);
            if (tmAge) tmAge.innerHTML = newest ? `· updated ${relativeTime(newest)}` : '';
            // Plain Number() coercion (not |0): counters are uint32 and can
            // exceed 2^31, which would wrap with a 32-bit bitwise op.
            const num  = (v) => (Number(v) || 0).toLocaleString('it-IT');
            const frac = (n, d) => `${Number(n) || 0}/${Number(d) || 0}`;
            const perHop = (arr) => (Array.isArray(arr) && arr.length)
                ? `<span class="ln-perhop">${arr.map(v => `<b>${Number(v) || 0}</b>`).join('')}</span>` : '';
            const tmColor = '#3b82f6', hsColor = '#a78bfa';
            const cnt = (label, val, field, color) =>
                [label, `${num(val)}${spark(field, color)}`, { raw: true }];
            let html = '';

            // ── Hop scaling: live state (log) + cumulative counters (telemetry)
            const hopRows = [];
            if (hasLiveHop) {
                hopRows.push(
                    ['Hop limit', String(hq.hop)],
                    ['Fill',      `${hq.fill_pct}%`],
                    ['Samples',   frac(hq.samp_num, hq.samp_den)],
                    ['Filtered',  frac(hq.filt_num, hq.filt_den)],
                    ['Entries',   num(hq.entries)],
                    ['Polite',    frac(hq.polite_num, hq.polite_den)],
                    ['Next roll', `${hq.next_roll_min} min`],
                );
                if (perHop(hq.nodes_per_hop))  hopRows.push(['Nodes per hop',  perHop(hq.nodes_per_hop),  { raw: true }]);
                if (perHop(hq.scaled_per_hop)) hopRows.push(['Scaled per hop', perHop(hq.scaled_per_hop), { raw: true }]);
            }
            if (hasCounters) {
                hopRows.push(
                    cnt('Hop-exhausted packets', ln.traffic_hop_exhausted_packets, 'hop_exhausted_packets', hsColor),
                    cnt('Router hops preserved', ln.traffic_router_hops_preserved, 'router_hops_preserved', hsColor),
                );
            }
            if (hopRows.length) {
                const tag = hasLiveHop ? ' <span class="th-hint">(live)</span>' : '';
                html += `<div class="ln-subhead">Hop scaling${tag}</div><div class="ln-kv">${kvRows(hopRows, { raw: true })}</div>`;
            }

            // ── Traffic management: NodeInfo cache (log) + counters (telemetry)
            const tmRows = [];
            if (hasTMCache) {
                const pct = hq.tm_cache_target ? Math.round(100 * hq.tm_cache_used / hq.tm_cache_target) : 0;
                tmRows.push(['NodeInfo cache', `${num(hq.tm_cache_used)} / ${num(hq.tm_cache_target)} <span class="th-hint">(${pct}%)</span>`, { raw: true }]);
            }
            if (hasCounters) {
                tmRows.push(
                    cnt('Packets inspected',    ln.traffic_packets_inspected,    'packets_inspected',    tmColor),
                    cnt('Position dedup drops', ln.traffic_position_dedup_drops, 'position_dedup_drops', tmColor),
                    cnt('NodeInfo cache hits',  ln.traffic_nodeinfo_cache_hits,  'nodeinfo_cache_hits',  tmColor),
                    cnt('Rate-limit drops',     ln.traffic_rate_limit_drops,     'rate_limit_drops',     tmColor),
                    cnt('Unknown packet drops', ln.traffic_unknown_packet_drops, 'unknown_packet_drops', tmColor),
                );
            }
            if (tmRows.length) {
                const mt = hopRows.length ? ' style="margin-top:0.7rem"' : '';
                html += `<div class="ln-subhead"${mt}>Traffic management</div><div class="ln-kv">${kvRows(tmRows, { raw: true })}</div>`;
            }

            tmBody.innerHTML = html;
        }
    }

    // Host system — HostMetrics telemetry (variant 8), sent only by
    // host-based Meshtastic builds (meshtasticd on Linux). Hidden until the
    // device reports it, so MCU nodes never show an empty card.
    const hsCard = document.getElementById('ln-host-card');
    const hsBody = document.getElementById('ln-host-body');
    const hsAge  = document.getElementById('ln-host-age');
    if (hsCard && hsBody) {
        if (!ln.host_metrics_at) {
            hsCard.style.display = 'none';
        } else {
            hsCard.style.display = '';
            if (hsAge) hsAge.innerHTML = `· updated ${relativeTime(ln.host_metrics_at)}`;
            // Load averages: show as "1m / 5m / 15m" when any is present.
            const fl = (v) => (v === undefined || v === null) ? null : Number(v).toFixed(2);
            const loads = [fl(ln.host_load1), fl(ln.host_load5), fl(ln.host_load15)];
            const loadStr = loads.some(v => v !== null)
                ? loads.map(v => v === null ? '—' : v).join(' / ') : '';
            const memColor = '#14b8a6', loadColor = '#f59e0b';
            const rows = [
                ['Host uptime', ln.host_uptime_seconds ? esc(fmtUptime(ln.host_uptime_seconds)) : '', { raw: true }],
                ['Free memory', `${esc(fmtBytes(ln.host_freemem_bytes))}${spark('freemem_bytes', memColor)}`, { raw: true }],
                ['Disk free /', `${esc(fmtBytes(ln.host_diskfree1_bytes))}${spark('diskfree1_bytes', memColor)}`, { raw: true }],
            ];
            if (ln.host_diskfree2_bytes) rows.push(['Disk free (2)', `${esc(fmtBytes(ln.host_diskfree2_bytes))}${spark('diskfree2_bytes', memColor)}`, { raw: true }]);
            if (ln.host_diskfree3_bytes) rows.push(['Disk free (3)', `${esc(fmtBytes(ln.host_diskfree3_bytes))}${spark('diskfree3_bytes', memColor)}`, { raw: true }]);
            rows.push(['Load avg (1/5/15m)', `${esc(loadStr)}${spark('load_1m', loadColor)}`, { raw: true }]);
            if (ln.host_user_string) rows.push(['Info', ln.host_user_string]);
            hsBody.innerHTML = `<div class="ln-kv">${kvRows(rows)}</div>`;
        }
    }
}
