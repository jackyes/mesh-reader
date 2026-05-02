# Mesh Reader

**Sniffer passivo, logger e dashboard real-time per reti [Meshtastic](https://meshtastic.org).**

Si connette a un nodo Meshtastic via USB seriale o WiFi/TCP, decodifica tutti i pacchetti, li salva su file di log giornalieri e su un database SQLite, e serve una dashboard web locale con mappa, analisi radio-health, trend di degrado del segnale e molto altro.

![Go 1.24+](https://img.shields.io/badge/go-1.24+-00ADD8?logo=go)
![Platform](https://img.shields.io/badge/platform-windows%20%7C%20linux%20%7C%20macos-lightgrey)
![License: MIT](https://img.shields.io/badge/license-MIT-green)

---

## Features

- **Connessione USB o WiFi/TCP** con auto-detect della porta seriale e reconnect automatico
- **Decodifica completa** di tutti i tipi di pacchetto Meshtastic (TextMessage, Position, Telemetry, NodeInfo, Traceroute, Routing, NeighborInfo, **Store-and-Forward**, Encrypted, LogRecord, **DeviceMetadata**, **ModuleConfig.NeighborInfo**, **Config.LoRa**…)
- **Persistenza SQLite** con WAL, indici compositi e retention policy configurabile (incl. snapshot per-nodo dei NeighborInfo)
- **Log giornalieri** in formato testo tab-separated (grep-friendly) + raw JSONL opzionale
- **Compressione automatica** dei log vecchi (gzip)
- **Auto-filtro telemetria/posizione del nodo locale** (con opt-out via `--not-ignore-self`) per evitare doppi conteggi
- **Dashboard web** a singola pagina, vanilla JS, nessuna dipendenza runtime:

### Overview
- KPI aggregati (nodi totali, attivi, messaggi, eventi)
- Sparkline eventi/minuto
- **Hop analysis per tipo di pacchetto** — barra traveled/remaining/start, colorata per distanza
- **Max hop histogram** — distribuzione del TTL impostato dai mittenti (identifica nodi con TTL aggressivo)
- **Top relay** con breakdown per tipo di pacchetto (quali tipi ripete di più ciascun relay)
- **Best DX** — nodi più lontani ordinati per SNR decrescente
- **Fragile Nodes** — classificazione (weak / direct-only / SPOF / healthy)
- **Signal Degradation** — nodi con SNR in peggioramento (severe / significant / minor)
- **Channel Utilization** aggregata con alert di congestione
- **Auto-refresh** toggle a 15 secondi con countdown visibile

### Mappa
- Leaflet + OpenStreetMap con marker per ogni nodo con posizione nota
- **ChUtil Geo-Monitor** — cerchi per nodo colorati per channel utilization (scala fissa 0–40%+), con selettore metrica (current / avg / p95 / max) e finestra temporale
- Heat bloom opzionale (Leaflet.heat) per visualizzazione a gradiente
- **Popup ricco** al click su un nodo: nome / short name / ID / HW / role badge, RSSI/SNR, max hop, telemetria (battery, ChUtil, AirTx), breakdown pacchetti con badge colorati per tipo, sparkline ChUtil 24h, **lista Neighbors con SNR pill colorate**
- **Heatmap temporale** con modalità (giorno della settimana, ora) e drill-down per cella
- Ricerca nodo con fly-to e apertura automatica del popup

### Nodes
- Tabella ordinabile: **Name / Short / ID** come colonne separate, HW, **Role badge** (CB=Client base / RT=Router / RP=Repeater / TR=Tracker / SN=Sensor / TAK / CM=Client Mute / CH=Client Hidden …), Last Heard, Signal, **Max hop** (mode + peak con colori), Battery, Packets, Breakdown per tipo (incl. **S&F** per Store-and-Forward)
- **Click sul nome** → modal con tutti i dettagli del nodo (riusa il popup mappa)
- Filtro testo che matcha su nome, short, ID, HW, role
- Sparkline SNR per nodo (asincrona)
- Export CSV

### My Node *(nuovo)*
- Pagina dedicata al nodo Meshtastic a cui siamo connessi
- **Identity**: long/short name, ID, node num, role, hardware, seen-at
- **Firmware**: versione firmware, PlatformIO env, reboot count, NodeDB entries, device state version
- **LoRa Radio**: region, modem preset (o BW/SF/CR custom), hop limit, TX power, TX enabled, channel num
- **Capabilities**: Wi-Fi / Bluetooth / PKC / Can shutdown
- **NeighborInfo module status** — banner verde se attivo, **banner rosso con istruzioni meshtastic-cli** se disabilitato (caso comune in cui il firmware scarta silenziosamente i NeighborInfo OTA)

### Misbehaving *(nuovo)*
- Lista dei nodi che superano i limiti configurati di trasmissione, con auto-rimozione quando rientrano nelle soglie
- **4 metriche configurabili** (toggle ON/OFF + count + window indipendenti per ognuna):
  - NodeInfo / window — default `> 2 / 60min`
  - Telemetry / window — default `> 2 / 60min`
  - Position / window — default `> 15 / 60min`
  - Max hop (mode di hop_start) — default `> 5 / 60min`
- **Save as default** persistito su disco (`misbehave-defaults.json` accanto al DB)
- **Auto-notify (opt-in)** — invia DM educati ai nodi flagged invitandoli a controllare i settings:
  - **Dry-run di default** la prima volta (logga ma non trasmette)
  - **Cooldown per nodo** (default 24h) + **rate limit globale** (default 5 DM/h) + **min flag age** (default 30 min) per evitare knee-jerk
  - **Template configurabile** in italiano con placeholder `{short}`, `{long}`, `{id}`, `{issue}`, `{reasons}`, `{me}` — `{issue}` produce frasi amichevoli con suggerimenti concreti tipo `"Imposta lora.hop_limit=5 per non sovraccaricare la mesh"`
  - **Preview live** del messaggio con il primo nodo flagged
  - **Live status panel**: rate utilizzato, slot prossimi, cooldown attivi, ETA del prossimo nodo eligible
  - **Colonna "Next notify"** per riga con pill colorate (ready/cooldown/grace/rate-limit)
  - **Notify now** per inviare immediatamente a un singolo nodo (rispetta dry-run)
  - **Reset per nodo** (azzera cooldown + counters + flag streak) e **Clear log** globale
  - **Audit log** delle ultime notifiche (sent / dry-run / failed) persistito in SQLite

### Telemetry
- Grafici storici battery / voltage / channel-util / temperature per nodo

### Network
- Topologia dei link con SNR — **solo link single-hop verificati** (filtro `hop_limit == hop_start`), niente più ragnatela inferita falsa da broadcast multi-hop
- **Restore neighbor links dal DB al boot** (≤24h) — niente più "mappa vuota" dopo restart in attesa del prossimo broadcast
- **Toggle "Connections" ON di default** + counter "off-map" che dice quanti link sono nascosti per mancanza di GPS
- **Traceroute sidebar arricchita** — badge `REQUEST` / `REPLY` per ogni traceroute, badge `PARTIAL MAP` se nodi della catena sono off-map, **SNR pill per ogni segmento** anche dove la mappa non può disegnare
- **Salvataggio traceroute "snooped"** (di passaggio non destinati a noi) con disegno per-segmento (skip solo del segmento con endpoint senza GPS, non dell'intera catena)
- Scatter SNR vs distanza haversine

### Messages
- Ricerca, ordinamento, export CSV

### Radio Health
- Parsing del firmware debug log (richiede `--enable-debug-log`): dup rate, raw RX, MQTT rate
- Per-sender best relay ranking e **Router Candidates** (quale relay è "best for" il maggior numero di nodi)
- **Hops used histogram** — distribuzione dei salti effettuati (0–7)
- **Max hop histogram** — distribuzione dell'hop_start (TTL configurato al mittente)
- Channel hash histogram, TX frequency offset distribution
- Storico a sliding window (60 minuti)

---

## Quick start

### Binari precompilati
Scarica l'ultima release da [Releases](../../releases) ed estrai lo zip.

### Da sorgenti

```bash
git clone https://github.com/jackyes/mesh-reader.git
cd mesh-reader
go build -o mesh-reader ./...
./mesh-reader
```

Su Windows, per compilare con l'icona custom (opzionale):

```bash
# Una volta sola per rigenerare il resource file (richiede mingw-w64/TDM-GCC)
go generate
go build -o mesh-reader.exe .
```

### Primo avvio

```bash
# Auto-detect della porta USB + dashboard su :8111
./mesh-reader

# Apri il browser su:
#   http://localhost:8111
```

---

## CLI flags

| Flag | Default | Descrizione |
|---|---|---|
| `--port COM5` | *auto-detect* | Porta seriale (Windows: `COMx`, Linux: `/dev/ttyUSB0`) |
| `--host 192.168.1.42` | *disabled* | Indirizzo IP/host del nodo via WiFi/TCP (porta 4403 di default) |
| `--baud 115200` | `115200` | Baud rate seriale |
| `--web-port :8111` | `:8111` | Porta HTTP per la dashboard (`off` per disabilitare) |
| `--db mesh.db` | `mesh.db` | Path del database SQLite |
| `--db-retention-days N` | `30` | Elimina eventi/segnali più vecchi di N giorni (0 = mantieni tutto) |
| `--log-dir ./logs` | `./logs` | Cartella per i file di log |
| `--log-compress-days N` | `7` | Gzip dei file log più vecchi di N giorni (0 = disabilitato) |
| `--raw-log` | `false` | Abilita log raw JSONL (include bytes protobuf in hex) |
| `--enable-debug-log` | `false` | Chiede al nodo di trasmettere il firmware debug log (LogRecord) |
| `--disable-debug-log` | `false` | Disabilita il firmware debug log sul nodo ed esce |
| `--ignore-node MESA` | — | Short name di un nodo di cui ignorare la telemetria (per nodi *terzi*) |
| `--not-ignore-self` | `false` | Disabilita il filtro automatico delle telemetrie/posizioni del nodo locale (default: scarta — la stato del proprio nodo è già tracciato via MyInfo + NodeInfo) |
| `--verbose N` | `0` | Verbosità console: `0`=silenzioso, `1`=pacchetti, `2`=debug |

Esempi:

```bash
# USB esplicita + ignora telemetria di un nodo terzo
./mesh-reader --port COM5 --ignore-node MESA

# WiFi/TCP
./mesh-reader --host 192.168.1.42

# Solo logging, senza dashboard
./mesh-reader --web-port off

# Cattura anche il firmware debug log (utile per radio-health analysis)
./mesh-reader --enable-debug-log

# Conta anche le proprie telemetrie/posizioni (utile per testing del proprio nodo)
./mesh-reader --port COM5 --not-ignore-self

# Retention aggressiva (tieni solo 7 giorni) e log sempre gzippati dopo 2 giorni
./mesh-reader --db-retention-days 7 --log-compress-days 2
```

Al primo avvio vedrai in console una riga del tipo:

```
[mesh-reader] auto-ignoring own telemetry/position from "AU18" / "APUANIA 18 (jacky)" (!da75c480)
```

— il nome breve viene rilevato automaticamente dal `NodeInfo` del nodo locale che il firmware invia nel boot dump. Nessuna configurazione manuale richiesta.

### Modalità sviluppo (dev mode)

Per modificare la dashboard senza ricompilare ad ogni cambiamento, imposta la variabile d'ambiente `MESH_WEB_DEV`:

```bash
# Linux / macOS
MESH_WEB_DEV=1 ./mesh-reader --port /dev/ttyUSB0

# Windows PowerShell
$env:MESH_WEB_DEV=1; .\mesh-reader.exe --port COM5
```

In dev mode i file statici vengono letti **da disco** a ogni richiesta (dalla cartella `internal/web/static`) e viene iniettato `Cache-Control: no-store` — basta un semplice F5 nel browser per vedere le modifiche.

---

## Requisiti

- **Go 1.24+** per compilare da sorgenti
- **Un nodo Meshtastic** collegato via USB o raggiungibile su rete locale (WiFi abilitato + API TCP su 4403)
- Firmware Meshtastic **2.x** (testato su 2.3.x / 2.5.x / 2.6.x)
- Browser moderno per la dashboard (Chrome, Firefox, Edge, Safari)

Il build è **pure Go**, niente CGO, niente compilatori C richiesti (tranne `windres` se vuoi l'icona Windows custom).

---

## Architettura

```
main.go
├── internal/reader     — I/O seriale/TCP + framing Meshtastic
├── internal/decoder    — Decoding protobuf di tutti i tipi di packet
├── internal/fwparser   — Parser regex del firmware debug log
├── internal/logger     — File di log giornalieri + compressione gzip
├── internal/store      — Stato in-memory (ring buffer 10k + indici)
│   ├── radio.go        — Metriche radio-health pre-dedup
│   ├── diagnostics.go  — Availability + channel utilization
│   ├── isolation.go    — Classificazione fragile nodes
│   └── dx.go           — Best DX leaderboard (SNR-ranked)
├── internal/db         — Persistenza SQLite (WAL, indici compositi, retention)
│   ├── chutil.go       — ChUtil history e zone analytics
│   └── heatmap.go      — Temporal heatmap con drill-down
└── internal/web        — HTTP server + REST API + dashboard statica embedded
    └── static/         — index.html, app.js, style.css
```

**Niente dipendenze esterne runtime**: la dashboard è incorporata nel binario via `//go:embed` e serve direttamente file statici. Nessun Node.js, nessun bundler, nessun framework JS.

---

## API REST

Endpoint principali (tutti `GET`, rispondono JSON tranne gli export):

| Path | Descrizione |
|---|---|
| `/api/stats` | KPI aggregati (include hop stats, relay stats) |
| `/api/nodes` | Elenco nodi con stato (incl. role, hop_start_hist) |
| `/api/nodes/{id}` | Singolo nodo per ID hex |
| `/api/messages?limit=N` | Ultimi N text message |
| `/api/positions` | Nodi con posizione (per la mappa) |
| `/api/events?limit=N&type=X` | Eventi recenti (filtrabile per tipo) |
| `/api/events-per-minute?window=60` | Buckets per sparkline |
| `/api/telemetry/{id}` | Storico telemetria del nodo |
| `/api/traceroutes` | Lista traceroute |
| `/api/links` | Archi del grafo di rete con SNR/RSSI |
| `/api/radio-health` | Metriche radio-health correnti (incl. hop histogram) |
| `/api/radio-health/history` | Trend storico radio-health |
| `/api/signal/{id}` | Storico RSSI/SNR di un nodo |
| `/api/availability` | Snapshot online/offline recenti |
| `/api/availability/{id}` | Timeline di disponibilità per nodo |
| `/api/channel-util` | Aggregato channel utilization |
| `/api/channel-util/history` | Storico channel util |
| `/api/chutil-zones?window=24` | ChUtil per zona/nodo (per la mappa) |
| `/api/chutil-history?id=HEX&hours=N` | Campioni ChUtil di un nodo (sparkline) |
| `/api/heatmap-temporal?mode=X&days=N` | Heatmap temporale aggregata |
| `/api/heatmap-cell-detail?...` | Drill-down su una cella della heatmap |
| `/api/isolated-nodes` | Nodi fragili (classificazione) |
| `/api/snr-distance` | Scatter SNR vs distanza haversine |
| `/api/signal-trends?window_hours=24` | Nodi che peggiorano nel tempo |
| `/api/anomalies?limit=N` | Anomalie rilevate (GPS teleport, spammer, SNR jump) |
| `/api/dx-records` | Best DX leaderboard (ordinato per SNR) |
| `/api/packet-path` | Percorso inferito di un pacchetto |
| `/api/health` | Healthcheck 200/503 |
| `/api/local-node` | Info del nodo Meshtastic connesso (firmware, LoRa config, capabilities, NeighborInfo module status) |
| `/api/misbehaving` | Nodi che superano le soglie configurate (con `notify_status` per riga) |
| `/api/misbehaving/config` | Soglie attualmente attive (`GET`) o cambia (`POST`, `?save=1` per persistere) |
| `/api/misbehaving/defaults` | Valori built-in per il bottone Reset |
| `/api/misbehaving/notifications` | Audit log delle ultime notifiche (`GET`) o wipe (`DELETE` → resetta cooldown + rate limit) |
| `/api/misbehaving/notify-status` | Riassunto live: rate utilizzato, prossimo slot, cooldown attivi, ready, ETA |
| `/api/misbehaving/notify/{id}` | `POST`: invia DM immediato al nodo (rispetta dry-run, bypassa cooldown/rate) |
| `/api/misbehaving/reset/{id}` | `POST`: azzera rate buckets + flag streak + audit log per il nodo |
| `/api/export/nodes.csv` | Dump nodi in CSV |
| `/api/export/messages.csv` | Dump messaggi in CSV |
| `POST /api/traceroute/{id}` | Invia traceroute on-demand verso un nodo |

---

## File generati

| File | Contenuto |
|---|---|
| `mesh.db` | SQLite con eventi, nodi (incl. snapshot NeighborInfo per nodo), traceroute, segnali, ChUtil, snapshot, audit log notifiche misbehaving |
| `misbehave-defaults.json` | Soglie persistite della pagina Misbehaving (creato dal bottone "Save as default", accanto al DB). Eliminabile in qualunque momento — al prossimo avvio si torna ai default built-in |
| `logs/mesh-YYYY-MM-DD.log` | Log testo tab-separated (human-readable, grep-friendly) |
| `logs/mesh-raw-YYYY-MM-DD.jsonl` | Raw packet log JSONL (se `--raw-log`) |
| `logs/mesh-fwlog-YYYY-MM-DD.log` | Firmware debug log (se `--enable-debug-log`) |
| `logs/*.gz` | Log vecchi compressi automaticamente |

---

## Privacy

Mesh Reader **è un ricevitore quasi-passivo**: non invia pacchetti sulla rete Meshtastic salvo:

- L'handshake iniziale con il nodo locale (`WantConfig`) e i keepalive
- I traceroute on-demand richiesti via UI (`POST /api/traceroute/{id}`)
- Le DM dell'**Auto-notify** (disabilitato di default; quando abilitato la prima volta forza `dry-run` come safety net, e ha rate limit + cooldown configurabili)

Tutti i dati registrati sono pacchetti in chiaro trasmessi via RF sulla mesh pubblica.

**Attenzione** però a non pubblicare il contenuto dei tuoi `logs/` e `mesh.db`: registrano posizione GPS dei nodi, nickname, messaggi sul canale default (in chiaro), e la posizione del tuo ricevitore è inferibile dal dataset. Il `.gitignore` esclude questi file di default.

---

## Compatibilità testata

- **OS**: Windows 10/11, Ubuntu 22.04+, macOS 13+
- **Hardware**: Heltec V3, Heltec V3.2, T-Beam, RAK WisBlock
- **Firmware Meshtastic**: 2.3.x, 2.5.x, 2.6.x, 2.7.x

> **Nota**: per popolare il grafo dei vicini sulla pagina Network e il popup mappa serve che il modulo **NeighborInfo** sia abilitato sul nodo locale (è disabilitato di default nel firmware recente). Con il modulo OFF il firmware scarta tutti i `NEIGHBORINFO_APP` ricevuti via radio prima di inoltrarli al client. La pagina **My Node** mostra un banner rosso con le istruzioni `meshtastic` per abilitarlo.

---

## Contributing

Issue e PR sono benvenuti. Per favore:

- Allinea lo stile con `gofmt` / `goimports`
- Niente nuove dipendenze Go senza motivazione (il progetto tiene l'albero snello di proposito)
- Niente dipendenze JS runtime sulla dashboard (vanilla JS o CDN esistenti — Leaflet, Chart.js — sono OK)
- Test per ogni nuovo parser / classificatore

---

## License

[MIT](LICENSE) © 2025–2026

Meshtastic® è un marchio della Meshtastic LLC. Questo progetto non è affiliato, sponsorizzato o supportato da Meshtastic LLC.
