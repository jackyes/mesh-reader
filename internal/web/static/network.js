// === Network / Traceroute Map tab ===
import { state } from './state.js';
import { api } from './api.js';
import { esc, parseNodeNum, nodeNameByNum, snrQualityColor, rssiColor, bearingDeg, perpOffset } from './utils.js';

export function initNetworkMap() {
    if (state.networkMap) { state.networkMap.invalidateSize(); return; }
    state.networkMap = L.map('networkMapContainer').setView([44, 11], 6);
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
        attribution: '&copy; OpenStreetMap', maxZoom: 18,
    }).addTo(state.networkMap);

    state.networkLayers.nodes  = L.layerGroup().addTo(state.networkMap);
    state.networkLayers.traces = L.layerGroup().addTo(state.networkMap);
    state.networkLayers.links  = L.layerGroup();

    const bounds = [];
    Object.values(state.nodes).forEach(n => {
        if (n.has_pos && n.lat && n.lon) {
            L.circleMarker([n.lat, n.lon], {
                radius: 7, fillColor: '#3b82f6', color: '#fff', weight: 1.5, fillOpacity: 0.85,
            }).bindTooltip(n.long_name || n.short_name || n.id || '?', { permanent: false })
              .addTo(state.networkLayers.nodes);
            bounds.push([n.lat, n.lon]);
        }
    });
    if (bounds.length > 0) state.networkMap.fitBounds(bounds, { padding: [30, 30] });

    const showLinksEl = document.getElementById('show-links');
    if (showLinksEl) {
        showLinksEl.addEventListener('change', async (e) => {
            if (e.target.checked) {
                if (state.networkLinksData.length === 0) {
                    state.networkLinksData = await api('/api/links') || [];
                }
                drawHeardLinks();
                state.networkMap.addLayer(state.networkLayers.links);
            } else {
                state.networkMap.removeLayer(state.networkLayers.links);
                updateLinksSummary(0, 0, 0); // hide while toggle is off
            }
        });
        // Auto-load and draw on first open if the toggle is ON. Without
        // this the user has to flip the checkbox to see anything, which
        // hid the entire neighbor graph behind a hidden interaction.
        if (showLinksEl.checked) {
            (async () => {
                if (state.networkLinksData.length === 0) {
                    state.networkLinksData = await api('/api/links') || [];
                }
                drawHeardLinks();
                state.networkMap.addLayer(state.networkLayers.links);
            })();
        }
    }

    renderTracerouteList();
}

export function drawHeardLinks() {
    if (!state.networkMap || !state.networkLayers.links) return;
    state.networkLayers.links.clearLayers();

    // Track how many links are hidden because at least one endpoint
    // either is unknown or has no GPS position. The counter is shown in
    // the toolbar so the user knows that the rendered graph is a
    // subset of the data — typical situation: a router (with GPS)
    // reports neighbors that are CLIENT nodes without a GPS fix, so
    // the line cannot be drawn even though we know it exists.
    let drawn = 0;
    let hidden = 0;
    let drawnNeighbor = 0;
    const offMap = new Set();

    (state.networkLinksData || []).forEach(link => {
        const nodeA = state.nodes[link.node_a];
        const nodeB = state.nodes[link.node_b];
        if (!nodeA || !nodeB || !nodeA.has_pos || !nodeB.has_pos) {
            hidden++;
            if (!nodeA || !nodeA.has_pos) offMap.add(link.node_a);
            if (!nodeB || !nodeB.has_pos) offMap.add(link.node_b);
            return;
        }
        drawn++;
        if (link.neighbor) drawnNeighbor++;

        const isNeighbor = !!link.neighbor;
        const color = isNeighbor ? snrQualityColor(link.snr) : rssiColor(link.rssi);
        const weight = isNeighbor
            ? Math.max(2, Math.min(5, 2 + link.count * 0.3))
            : Math.max(1.5, Math.min(4, 1.5 + link.count * 0.3));

        const line = L.polyline(
            [[nodeA.lat, nodeA.lon], [nodeB.lat, nodeB.lon]],
            { color, weight, opacity: isNeighbor ? 0.7 : 0.45, dashArray: isNeighbor ? null : '6, 4' }
        );

        let popupHtml = `<b>${nodeNameByNum(link.node_a)}</b> &harr; <b>${nodeNameByNum(link.node_b)}</b><br>`;
        if (isNeighbor) {
            popupHtml += `SNR: ${link.snr.toFixed(1)} dB<br>`;
            popupHtml += `<em style="color:#14b8a6">&#x2713; Direct neighbor</em><br>`;
        }
        if (link.rssi) popupHtml += `RSSI: ${link.rssi} dBm<br>`;
        popupHtml += `Packets: ${link.count}`;
        line.bindPopup(popupHtml);

        if (isNeighbor) {
            const mid = [(nodeA.lat + nodeB.lat) / 2, (nodeA.lon + nodeB.lon) / 2];
            L.marker(mid, {
                icon: L.divIcon({
                    className: 'snr-label',
                    html: `<span>${link.snr.toFixed(1)} dB</span>`,
                    iconSize: [56, 16], iconAnchor: [28, 8],
                }),
                interactive: false,
            }).addTo(state.networkLayers.links);
        }

        line.addTo(state.networkLayers.links);
    });

    updateLinksSummary(drawn, hidden, offMap.size, drawnNeighbor);
}

// updateLinksSummary populates the small status string next to the
// "Connections" toggle. It tells the user how many neighbor/inferred
// links are visible vs hidden, and why — typically: nodes without a
// known GPS position. Pass 0 for every counter to hide it (used when
// the toggle is off, since the summary is only meaningful when the
// links layer is showing).
export function updateLinksSummary(drawn, hidden, offMapCount, drawnNeighbor) {
    const el = document.getElementById('links-summary');
    if (!el) return;
    if (drawn === 0 && hidden === 0) {
        el.textContent = '';
        el.removeAttribute('title');
        return;
    }
    const neighborTxt = drawnNeighbor !== undefined && drawnNeighbor > 0
        ? ` (${drawnNeighbor} neighbor)`
        : '';
    if (hidden > 0) {
        el.innerHTML = `<span class="ls-drawn">${drawn} drawn${neighborTxt}</span>` +
            ` &middot; <span class="ls-hidden">${hidden} hidden</span>` +
            ` <span class="ls-sub">(${offMapCount} node${offMapCount !== 1 ? 's' : ''} without GPS)</span>`;
        el.title = 'Some links are not drawable on the map because at least one endpoint has no known GPS position. They are still listed in each node\'s detail view (click the node name in the Nodes table).';
    } else {
        el.innerHTML = `<span class="ls-drawn">${drawn} drawn${neighborTxt}</span>`;
        el.removeAttribute('title');
    }
}

export async function refreshHeardLinks() {
    state.networkLinksData = await api('/api/links') || [];
    drawHeardLinks();
}

// ---- Traceroute sidebar ----

// Render the Network-tab sidebar of traceroutes.
//
// Each card shows the full forward path (and the return path, when the
// packet is a reply) including:
//   - per-node "no GPS" badge if we don't have a position for that node
//     (the segment will not be drawable on the map but is shown here)
//   - per-hop SNR pill colored by quality (always known regardless of GPS)
//   - REQUEST / REPLY badge (REQUEST = in-transit observation without
//     a return path yet; REPLY = full forward+back).
//   - "PARTIAL MAP" badge if at least one node in the chain has no GPS
//     (the polyline on the map will skip the broken segment).
export function renderTracerouteList() {
    const container = document.getElementById('traceroute-list');
    if (!container) return;
    if (!state.traceroutes || state.traceroutes.length === 0) {
        container.innerHTML = '<p class="text-dim">No traceroute data yet</p>';
        return;
    }
    container.innerHTML = '';
    [...state.traceroutes].reverse().forEach((tr, idx) => {
        const card = document.createElement('div');
        card.className = 'tr-card';
        card.dataset.idx = state.traceroutes.length - 1 - idx;

        const fwdNums = [tr.from, ...((tr.route || []).map(parseNodeNum)), tr.to];
        const fwdSnr  = tr.snr_towards || [];
        const fwdHtml = renderTrChain(fwdNums, fwdSnr, false);

        const hasReturn = tr.route_back && tr.route_back.length > 0;
        let retHtml = '';
        if (hasReturn) {
            const retNums = [tr.to, ...tr.route_back.map(parseNodeNum), tr.from];
            const retSnr  = tr.snr_back || [];
            retHtml = `<div class="tr-ret-label">&hookleftarrow; return</div>${renderTrChain(retNums, retSnr, true)}`;
        }

        const time = new Date(tr.time * 1000).toLocaleString('it-IT', {
            hour: '2-digit', minute: '2-digit', second: '2-digit', day: '2-digit', month: '2-digit',
        });
        const hopCount = fwdNums.length - 1;
        const allNums = hasReturn ? [...fwdNums, ...[tr.to, ...tr.route_back.map(parseNodeNum), tr.from]] : fwdNums;
        const noGpsCount = allNums.filter(n => {
            const node = state.nodes[n];
            return !(node && node.has_pos);
        }).length;
        const isPartialMap = noGpsCount > 0;

        const typeBadge = hasReturn
            ? '<span class="tr-badge tr-badge-reply">REPLY</span>'
            : '<span class="tr-badge tr-badge-request">REQUEST</span>';
        const partialBadge = isPartialMap
            ? `<span class="tr-badge tr-badge-partial" title="${noGpsCount} node(s) without GPS — segment(s) not drawable on the map">PARTIAL MAP</span>`
            : '';

        card.innerHTML = `
            <div class="tr-head">
                <span class="tr-time">${time}</span>
                <span class="tr-hops">${hopCount} hop${hopCount !== 1 ? 's' : ''}</span>
                ${typeBadge}
                ${partialBadge}
            </div>
            <div class="tr-chain-label">forward</div>
            ${fwdHtml}
            ${retHtml}`;

        card.addEventListener('click', () => highlightTraceroute(parseInt(card.dataset.idx), card));
        container.appendChild(card);
    });
}

// renderTrChain builds the inline HTML for a sequence of nodes connected
// by SNR-labeled arrows. nums is the full chain (including endpoints);
// snrRaw[i] is the SNR for hop nums[i] -> nums[i+1] (in protobuf raw
// units = dB * 4). Nodes without GPS get a small "no GPS" tag so the
// user can see exactly which segment will be missing on the map.
export function renderTrChain(nums, snrRaw, isReturn) {
    const parts = [];
    for (let i = 0; i < nums.length; i++) {
        const num = nums[i];
        const node = state.nodes[num];
        const name = nodeNameByNum(num) || `!${(num >>> 0).toString(16).padStart(8, '0')}`;
        const hasGps = node && node.has_pos;
        const gpsTag = hasGps
            ? ''
            : '<span class="tr-no-gps" title="No known GPS position — this segment cannot be drawn on the map">no GPS</span>';
        parts.push(`<span class="tr-hop ${hasGps ? '' : 'tr-hop-nogps'}">${esc(name)}${gpsTag}</span>`);
        if (i < nums.length - 1) {
            const raw = snrRaw[i];
            const hasSnr = raw !== undefined && raw !== null;
            const snrDb = hasSnr ? (raw / 4) : null;
            if (hasSnr) {
                const color = snrQualityColor(snrDb);
                parts.push(`<span class="tr-arrow tr-arrow-snr" style="--snr-color:${color}" title="SNR ${snrDb.toFixed(2)} dB">&rarr;<span class="tr-snr-pill" style="background:${color}">${snrDb.toFixed(1)}</span></span>`);
            } else {
                parts.push('<span class="tr-arrow">&rarr;</span>');
            }
        }
    }
    return `<div class="tr-chain ${isReturn ? 'tr-chain-ret' : ''}">${parts.join('')}</div>`;
}

export function highlightTraceroute(idx, card) {
    document.querySelectorAll('.tr-card').forEach(c => c.classList.remove('active'));
    card.classList.add('active');

    state.networkLayers.traces.clearLayers();

    const tr = state.traceroutes[idx];
    if (!tr) return;

    const fwdNums = [tr.from, ...((tr.route || []).map(parseNodeNum)), tr.to];
    const snrFwd  = tr.snr_towards || [];
    drawTraceChain(fwdNums, snrFwd, false, 'Forward');

    let retNums = [];
    if (tr.route_back && tr.route_back.length > 0) {
        retNums = [tr.to, ...tr.route_back.map(parseNodeNum), tr.from];
        drawTraceChain(retNums, tr.snr_back || [], true, 'Return');
    }

    // Endpoint markers: green = source, red = destination. Drawn only when
    // we actually know their GPS position; otherwise they live in the
    // sidebar with a "no GPS" badge.
    const srcNode = state.nodes[tr.from];
    if (srcNode && srcNode.has_pos) {
        L.circleMarker([srcNode.lat, srcNode.lon], {
            radius: 11, fillColor: '#22c55e', color: '#fff', weight: 2, fillOpacity: 0.9,
        }).bindTooltip('Start: ' + nodeNameByNum(tr.from), { permanent: false })
          .addTo(state.networkLayers.traces);
    }
    const dstNode = state.nodes[tr.to];
    if (dstNode && dstNode.has_pos) {
        L.circleMarker([dstNode.lat, dstNode.lon], {
            radius: 11, fillColor: '#ef4444', color: '#fff', weight: 2, fillOpacity: 0.9,
        }).bindTooltip('End: ' + nodeNameByNum(tr.to), { permanent: false })
          .addTo(state.networkLayers.traces);
    }

    // Intermediate hops with GPS get a small grey marker so the user can
    // visually walk the chain, hop by hop.
    const allNums = retNums.length > 0 ? [...fwdNums, ...retNums] : fwdNums;
    const seen = new Set([tr.from, tr.to]);
    allNums.forEach(num => {
        if (seen.has(num)) return;
        seen.add(num);
        const n = state.nodes[num];
        if (n && n.has_pos) {
            L.circleMarker([n.lat, n.lon], {
                radius: 6, fillColor: '#94a3b8', color: '#fff', weight: 1.5, fillOpacity: 0.85,
            }).bindTooltip(nodeNameByNum(num), { permanent: false })
              .addTo(state.networkLayers.traces);
        }
    });

    // Fit bounds to whatever positions we have.
    const bounds = [];
    allNums.forEach(num => {
        const n = state.nodes[num];
        if (n && n.has_pos) bounds.push([n.lat, n.lon]);
    });
    if (bounds.length > 0) {
        state.networkMap.fitBounds(bounds, { padding: [50, 50] });
    }
}

// drawTraceChain draws each hop of a traceroute path independently. For
// every consecutive (i, i+1) pair we draw the segment ONLY if both
// endpoints have a known GPS position; otherwise we silently skip just
// that segment (the sidebar still shows the SNR for it). This means a
// chain of e.g. 6 hops where one intermediate node has no GPS will
// render the 5 drawable segments correctly, instead of joining the two
// GPS-known endpoints with a misleading straight line.
export function drawTraceChain(nums, snrValues, isReturn, label) {
    for (let i = 0; i < nums.length - 1; i++) {
        const aNum = nums[i], bNum = nums[i + 1];
        const a = state.nodes[aNum];
        const b = state.nodes[bNum];
        if (!a || !a.has_pos || !b || !b.has_pos) continue; // gap — skip just this segment

        const snrRaw = snrValues[i];
        const hasSnr = snrRaw !== undefined && snrRaw !== null;
        const snrDb = hasSnr ? (snrRaw / 4) : null;
        const color = hasSnr ? snrQualityColor(snrDb) : (isReturn ? '#7dd3fc' : '#3b82f6');

        let p1 = [a.lat, a.lon];
        let p2 = [b.lat, b.lon];
        if (isReturn) {
            const off = perpOffset(a.lat, a.lon, b.lat, b.lon, 80);
            p1 = [a.lat + off.dlat, a.lon + off.dlon];
            p2 = [b.lat + off.dlat, b.lon + off.dlon];
        }

        const line = L.polyline([p1, p2], {
            color,
            weight: isReturn ? 3.5 : 4.5,
            opacity: 0.85,
            dashArray: isReturn ? '8, 6' : null,
        });
        const fromName = nodeNameByNum(aNum);
        const toName   = nodeNameByNum(bNum);
        let popupText  = `<b>${label} hop ${i + 1}</b><br>${fromName} &rarr; ${toName}`;
        if (hasSnr) popupText += `<br>SNR: ${snrDb.toFixed(2)} dB`;
        line.bindPopup(popupText);
        line.addTo(state.networkLayers.traces);

        const mid = [(p1[0] + p2[0]) / 2, (p1[1] + p2[1]) / 2];
        const angle = bearingDeg(p1[0], p1[1], p2[0], p2[1]);
        L.marker(mid, {
            icon: L.divIcon({
                className: '',
                html: `<div style="transform:rotate(${angle - 90}deg);color:${color};font-size:16px;text-shadow:0 0 3px #000,0 0 3px #000;line-height:1">&#9654;</div>`,
                iconSize: [16, 16], iconAnchor: [8, 8],
            }),
            interactive: false,
        }).addTo(state.networkLayers.traces);

        if (hasSnr) {
            const lblOff = perpOffset(p1[0], p1[1], p2[0], p2[1], isReturn ? -35 : 35);
            const lblPos = [mid[0] + lblOff.dlat, mid[1] + lblOff.dlon];
            L.marker(lblPos, {
                icon: L.divIcon({
                    className: '',
                    html: `<div class="tr-snr-label" style="background:${color}">${snrDb.toFixed(1)} dB</div>`,
                    iconSize: [50, 18], iconAnchor: [25, 9],
                }),
                interactive: false,
            }).addTo(state.networkLayers.traces);
        }
    }
}

// refreshTraceroutes pulls the latest traceroute list and re-renders the
// Network sidebar. Used on Network-tab activation so newly-arrived TRs
// (including those triggered via the on-demand TR button) show up without
// requiring a full Refresh.
export async function refreshTraceroutes() {
    try {
        const trs = await api('/api/traceroutes');
        state.traceroutes = trs || [];
        renderTracerouteList();
    } catch (e) {
        console.error('refreshTraceroutes:', e);
    }
}
