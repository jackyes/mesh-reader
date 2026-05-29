// === Local Node tab ===
import { state } from './state.js';
import { api } from './api.js';
import { esc, fmtUptime, kvRows, boolBadge, relativeTime } from './utils.js';
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
    if (ln.noise_floor_dbm) {
        const ago = ln.noise_floor_at ? ` <span class="th-hint">(${relativeTime(ln.noise_floor_at)})</span>` : '';
        loraRows.push(['Noise floor', `${ln.noise_floor_dbm} dBm${ago}`, { raw: true }]);
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
}
