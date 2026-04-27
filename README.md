# Mesh Reader

**Sniffer passivo, logger e dashboard real-time per reti [Meshtastic](https://meshtastic.org).**

Si connette a un nodo Meshtastic via USB seriale o WiFi/TCP, decodifica tutti i pacchetti, li salva su file di log giornalieri e su un database SQLite, e serve una dashboard web locale con mappa, analisi radio-health, trend di degrado del segnale e molto altro.

![Go 1.24+](https://img.shields.io/badge/go-1.24+-00ADD8?logo=go)
![Platform](https://img.shields.io/badge/platform-windows%20%7C%20linux%20%7C%20macos-lightgrey)
![License: MIT](https://img.shields.io/badge/license-MIT-green)

---

## Features

- **Connessione USB o WiFi/TCP** con auto-detect della porta seriale e reconnect automatico
- **Decodifica completa** di tutti i tipi di pacchetto Meshtastic (TextMessage, Position, Telemetry, NodeInfo, Traceroute, Routing, NeighborInfo, Encrypted, LogRecord…)
- **Persistenza SQLite** con WAL, indici compositi e retention policy configurabile
- **Log giornalieri** in formato testo tab-separated (grep-friendly) + raw JSONL opzionale
- **Compressione automatica** dei log vecchi (gzip)
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
- **Popup ricco** al click su un nodo: nome / short name / ID / HW / role badge, RSSI/SNR, max hop, telemetria (battery, ChUtil, AirTx), breakdown pacchetti con badge colorati per tipo, sparkline ChUtil 24h
- **Heatmap temporale** con modalità (giorno della settimana, ora) e drill-down per cella
- Ricerca nodo con fly-to e apertura automatica del popup

### Nodes
- Tabella ordinabile: **Name / Short / ID** come colonne separate, HW, **Role** (CLIENT / ROUTER / REPEATER / TRACKER / SENSOR / TAK …), Last Heard, Signal, **Max hop** (mode + peak con colori), Battery, Packets, Breakdown per tipo
- Filtro testo che matcha su nome, short, ID, HW, role
- Sparkline SNR per nodo (asincrona)
- Export CSV

### Telemetry
- Grafici storici battery / voltage / channel-util / temperature per nodo

### Network
- Topologia dei link con SNR, marker RSSI/distanza
- Sidebar traceroute con visualizzazione su mappa
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
| `--ignore-node MESA` | — | Short name di un nodo di cui ignorare la telemetria |
| `--verbose N` | `0` | Verbosità console: `0`=silenzioso, `1`=pacchetti, `2`=debug |

Esempi:

```bash
# USB esplicita + ignora telemetria del proprio nodo
./mesh-reader --port COM5 --ignore-node AU18

# WiFi/TCP
./mesh-reader --host 192.168.1.42

# Solo logging, senza dashboard
./mesh-reader --web-port off

# Cattura anche il firmware debug log (utile per radio-health analysis)
./mesh-reader --enable-debug-log

# Retention aggressiva (tieni solo 7 giorni) e log sempre gzippati dopo 2 giorni
./mesh-reader --db-retention-days 7 --log-compress-days 2
```

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
| `/api/export/nodes.csv` | Dump nodi in CSV |
| `/api/export/messages.csv` | Dump messaggi in CSV |
| `POST /api/traceroute/{id}` | Invia traceroute on-demand verso un nodo |

---

## File generati

| File | Contenuto |
|---|---|
| `mesh.db` | SQLite con eventi, nodi, traceroute, segnali, ChUtil, snapshot |
| `logs/mesh-YYYY-MM-DD.log` | Log testo tab-separated (human-readable, grep-friendly) |
| `logs/mesh-raw-YYYY-MM-DD.jsonl` | Raw packet log JSONL (se `--raw-log`) |
| `logs/mesh-fwlog-YYYY-MM-DD.log` | Firmware debug log (se `--enable-debug-log`) |
| `logs/*.gz` | Log vecchi compressi automaticamente |

---

## Privacy

Mesh Reader **è un ricevitore passivo**: non invia pacchetti sulla rete Meshtastic a parte l'handshake iniziale con il nodo locale e i keepalive. Tutti i dati registrati sono pacchetti già in chiaro trasmessi via RF sulla mesh pubblica.

**Attenzione** però a non pubblicare il contenuto dei tuoi `logs/` e `mesh.db`: registrano posizione GPS dei nodi, nickname, messaggi sul canale default (in chiaro), e la posizione del tuo ricevitore è inferibile dal dataset. Il `.gitignore` esclude questi file di default.

---

## Compatibilità testata

- **OS**: Windows 10/11, Ubuntu 22.04+, macOS 13+
- **Hardware**: Heltec V3, Heltec V3.2, T-Beam, RAK WisBlock
- **Firmware Meshtastic**: 2.3.x, 2.5.x, 2.6.x

---

## Contributing

Issue e PR sono benvenuti. Per favore:

- Allinea lo stile con `gofmt` / `goimports`
- Niente nuove dipendenze Go senza motivazione (il progetto tiene l'albero snello di proposito)
- Niente dipendenze JS runtime sulla dashboard (vanilla JS o CDN esistenti — Leaflet, Chart.js — sono OK)
- Test per ogni nuovo parser / classificatore

---

## License

[MIT](LICENSE) © 2025

Meshtastic® è un marchio della Meshtastic LLC. Questo progetto non è affiliato, sponsorizzato o supportato da Meshtastic LLC.
