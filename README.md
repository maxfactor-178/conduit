# Conduit

A web-based XMPP chat client that bridges browser WebSocket connections to an XMPP server. Users open a browser tab and chat; Conduit manages the XMPP sessions on their behalf.

Built with Go 1.24, [mellium.im/xmpp](https://mellium.im/xmpp), and a vanilla-JS frontend styled after Discord.

---

## Quickstart (dev)

You need **Go 1.24+** and **Docker**. From the repo root:

```bash
# 1. Start ejabberd (XMPP server) in Docker
docker compose up -d ejabberd

# 2. Create two test accounts (one-time, after ejabberd is healthy)
docker exec -it webchat-ejabberd-1 ejabberdctl register alice example.com password123
docker exec -it webchat-ejabberd-1 ejabberdctl register bob   example.com password456

# 3. Run Conduit in dev mode
go run ./cmd/conduit -config config/config.dev.yaml
```

Then open two browser tabs and chat between them:

- http://localhost:8080?user=alice
- http://localhost:8080?user=bob

That's it — no build step for the frontend (it's embedded). Edit Go or
`internal/frontend/web/*` files, stop the process, and re-run step 3 to pick up
changes. More detail in [Development](#development); architecture and internals
in [DEVELOPERS.md](DEVELOPERS.md).

---

## Table of Contents

- [Quickstart (dev)](#quickstart-dev)
- [Overview](#overview)
- [Architecture](#architecture)
- [Message Flow](#message-flow)
- [Configuration](#configuration)
- [Authentication](#authentication)
- [Deployment](#deployment)
- [Development](#development)
- [Protocol Reference](#protocol-reference)

---

## Overview

Conduit sits between your browser and an ejabberd (or other XMPP) server:

```
Browser  ←──WebSocket──→  Conduit  ←──XMPP/TCP──→  ejabberd
```

Key design decisions:

- **One XMPP connection per JID**, not per browser tab. Multiple tabs for the same account share a single XMPP session. Messages sent from any tab are echoed to all others.
- **Multi-user-to-one-JID mapping**: multiple HTTP users (e.g. several helpdesk agents) can be mapped to a single XMPP account. All agents share the connection and see all messages in real time.
- **Idle shutdown**: when a JID's last browser session closes, the XMPP connection is kept alive for a configurable idle period, then torn down gracefully.
- **Automatic reconnect**: a supervisor goroutine reconnects with exponential backoff if the XMPP server drops the connection.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                          Conduit process                        │
│                                                                 │
│  HTTP Server                                                    │
│  ├── /healthz, /readyz   (no auth)                             │
│  ├── /ws                 (auth middleware → WebSocket Handler)  │
│  └── /                   (auth middleware → static frontend)   │
│                                                                 │
│  WebSocket Handler (one goroutine pair per browser tab)         │
│  ├── readPump  ─── InboundMessage ──▶ Bridge                   │
│  └── writePump ◀── OutboundMessage ── Session.Send channel      │
│                                                                 │
│  Bridge (per session)                                           │
│  └── dispatches inbound messages to XMPPConn methods           │
│                                                                 │
│  User Manager                                                   │
│  └── JID → User (one per XMPP account)                        │
│       ├── XMPPConn  (one XMPP TCP session per JID)             │
│       ├── sessions  (map of active browser sessions)           │
│       ├── eventLoop (goroutine: XMPP events → Broadcast)       │
│       └── Broadcast (sends OutboundMessage to all sessions)    │
│                                                                 │
│  XMPPConn (internal/xmpp)                                      │
│  ├── connect()     TCP dial + XMPP negotiation                 │
│  ├── supervisor()  serves session, reconnects on drop          │
│  └── events chan   inbound XMPP events emitted here            │
└─────────────────────────────────────────────────────────────────┘
```

### Package layout

| Package | Responsibility |
|---|---|
| `cmd/conduit` | Entry point: wires all components, handles signals |
| `config` | YAML config loading and defaults |
| `internal/auth` | HTTP username → XMPP JID mapping, auth middleware |
| `internal/httpserver` | HTTP server, TLS, graceful shutdown, /healthz |
| `internal/websocket` | WebSocket upgrade, read/write pumps, ping/pong |
| `internal/bridge` | Translates JSON protocol messages ↔ XMPP operations |
| `internal/user` | User lifecycle, multi-session broadcast, idle shutdown |
| `internal/session` | One per browser WebSocket; holds the `Send` channel |
| `internal/xmpp` | All mellium usage; XMPP connection, handlers, MAM, MUC |
| `internal/history` | MAM query orchestration; delivers history batches |
| `internal/frontend` | Embeds static web assets (index.html, app.js, style.css) |
| `pkg/protocol` | Shared JSON message types for the WebSocket protocol |

---

## Message Flow

### Incoming chat message (XMPP → browser)

```
ejabberd
  └─▶ mellium TCP read
        └─▶ session.Serve dispatches <message> to mux handler
              └─▶ conn.handleChatMsg reads body + delay timestamp
                    └─▶ conn.events ← Event{Type: EventChat, From, Body, Time}
                          └─▶ user.eventLoop reads event
                                └─▶ eventToOutbound → OutboundMessage{type:"chat"}
                                      └─▶ user.Broadcast → every session.Send channel
                                            └─▶ writePump writes JSON to WebSocket
                                                  └─▶ browser app.js → handleChat()
```

### Outgoing chat message (browser → XMPP)

```
Browser sends JSON: {type:"chat", to:"alice@example.com", body:"hello"}
  └─▶ WebSocket readPump decodes InboundMessage
        └─▶ bridge.HandleInbound(TypeChat)
              ├─▶ conn.SendChat → mellium encodes <message> XML → ejabberd
              └─▶ user.Broadcast({type:"chat", from:ourJID, to:alice, body:...})
                    └─▶ all sessions (every open tab / agent) see the sent message
```

The broadcast echo on sent messages is what keeps multiple tabs and multiple agents mapped to the same JID in sync — no XMPP carbons (XEP-0280) required.

### Connection lifecycle

```
HTTP request with auth header (or ?user= in dev mode)
  └─▶ auth.Middleware maps username → bare JID
        └─▶ user.Manager.GetOrCreate(jid)
              ├── [existing user] return existing User, new session attached
              └── [new user]
                    ├─▶ xmpp.Dial: TCP connect + XMPP negotiation (outside lock)
                    ├─▶ supervisor goroutine starts:
                    │     ├─▶ session.Serve(mux) in goroutine (blocks until disconnect)
                    │     ├─▶ postConnect: send initial presence, fetch roster, rejoin rooms
                    │     └─▶ on disconnect: exponential backoff reconnect loop
                    └─▶ eventLoop goroutine: conn.Events() → user.Broadcast

WebSocket upgraded, session registered with User
  └─▶ writePump goroutine drains session.Send → WebSocket frames
  └─▶ readPump loop decodes frames → bridge.HandleInbound (blocks until close)

On WebSocket close:
  └─▶ user.RemoveSession
        └─▶ if last session: idle timer starts (default 60s)
              └─▶ on timeout: conn.Close(), user removed from manager
```

### MUC room flow

```
openRoom(jid) [browser]
  └─▶ send {type:"join_room", room:"chat@conference.example.com"}
        └─▶ bridge → conn.JoinRoom → mellium sends <presence to="room/nick">
              └─▶ ejabberd sends back presence stanzas for each occupant
                    └─▶ conn.handlePresence detects muc#user child
                          └─▶ Event{Type:EventRoomPresence, Nick, Show, Occupants}
                                └─▶ user.Broadcast({type:"room_occupants", ...})
                                      └─▶ browser handleOccupants() updates members panel

Incoming room message:
  └─▶ handleGroupChatMsg → EventRoomMessage
        └─▶ Broadcast({type:"room_message", room, nick, body})
              └─▶ browser handleRoomMsg() renders message
```

### MAM history

```
Browser sends {type:"history", conversation:"bob@example.com", limit:50}
  └─▶ bridge → history.LoadConversationHistory
        └─▶ conn.QueryMAM(MAMQuery{With:bob, Before:cursor, Max:50})
              └─▶ mellium sends disco#items IQ with RSM <set><before/><max>50</max></set>
                    └─▶ ejabberd streams back <message> result stanzas
                          └─▶ dispatchHistoryMessage → MAMResult → pending channel
                                └─▶ history.drainAndBroadcast collects batch
                                      └─▶ Broadcast({type:"history_batch", payload:[...]})
                                            └─▶ browser prependMessage() inserts before oldest
```

---

## Configuration

Config file path defaults to `config/config.yaml`; override with `-config /path/to/file.yaml`.

```yaml
brand: "Conduit"               # browser tab / page title shown in the UI

http:
  addr: ":8080"                # listen address
  read_timeout: 10s
  write_timeout: 10s
  shutdown_timeout: 15s
  allowed_origins:             # CORS; ["*"] allows all
    - "https://chat.example.com"

xmpp:
  host: "localhost"            # XMPP server host
  port: 5222
  domain: "example.com"       # XMPP domain (used to build JIDs)
  tls_mode: "starttls"        # "starttls" verifies the server cert. Any other
                               # value (e.g. "starttls-insecure", "none") skips
                               # verification — STARTTLS is always negotiated
                               # either way (ejabberd advertises it, so the
                               # handler must always be registered).
  resource: "conduit"         # XMPP resource appended to JID
  muc_host: "conference.example.com"   # primary conference host; defaults to conference.<domain>
  muc_hosts: []                        # extra conference hosts to browse (federated/remote servers)
  dial_timeout: 10s
  idle_shutdown: 60s           # close XMPP connection after last session disconnects
  reconnect_max_backoff: 30s

auth:
  username_header: "X-Remote-User"   # trusted reverse proxy header
  jid_mapping_file: "/etc/conduit/users.json"
  credentials_file: "/etc/conduit/credentials.json"

history:
  default_limit: 50
  max_limit: 200

log:
  level: "info"      # debug | info | warn | error
  format: "text"     # text | json | syslog (RFC 3164 lines, facility local0)
  file: ""           # app log path; empty = stdout
  audit_file: ""     # auth audit log path; empty = audit records go to the app log
```

### Logging & audit

- **`file`** sets where the application log goes. Empty means stdout (handy under
  systemd/journald, which captures stdout automatically).
- **`audit_file`** enables a dedicated audit log of authentication events:
  `session_open`, `session_close`, and `auth_rejected`, each with the JID (where
  known), client IP (honouring `X-Forwarded-For` behind the proxy), and session
  ID. If left empty, these records are written into the app log instead.
- **`format: syslog`** emits classic BSD syslog lines
  (`<PRI>timestamp host conduit[pid]: msg key=value …`) that standard collectors
  parse out of the box. `json` is best for structured ingestion; `text` is the
  human-friendly default.
- **Rotation** is left to the OS. Conduit opens log files append-only and never
  rotates them itself — point `logrotate` at them using `copytruncate` (a sample
  config ships as [`conduit.logrotate`](conduit.logrotate)).

---

## Authentication

Conduit does **not** handle login itself. It expects a trusted reverse proxy (nginx, Caddy, Authentik, etc.) to authenticate users and set a header.

> ⚠️ **Security-critical: the username header is fully trusted.** Conduit treats
> whoever the `X-Remote-User` header names as authenticated — there is no further
> verification. This means:
>
> - **Never expose Conduit's port directly.** Bind it to localhost (or an internal
>   network) and only reach it through the authenticating proxy.
> - **The proxy must overwrite the header on every request**, not just add it. If a
>   client can send their own `X-Remote-User`, they can impersonate any user.
>   In nginx, always set it explicitly (`proxy_set_header X-Remote-User $remote_user;`)
>   so any client-supplied value is replaced.
> - Keep `allowed_origins` set to your real hostname(s) in production, not `["*"]`.

### Header-based auth (production)

The reverse proxy sets `X-Remote-User: alice` on every request. Conduit maps this to a JID:

**`/etc/conduit/users.json`** — maps HTTP usernames to XMPP JIDs:
```json
{
  "alice":     "alice@example.com",
  "bob":       "bob@example.com",
  "support1":  "helpdesk@example.com",
  "support2":  "helpdesk@example.com"
}
```

If a username has no entry, Conduit falls back to `username@domain`.

**`/etc/conduit/credentials.json`** — XMPP passwords keyed by bare JID:
```json
{
  "alice@example.com":    "secret1",
  "bob@example.com":      "secret2",
  "helpdesk@example.com": "helpdesk-secret"
}
```

### Multi-user-to-one-JID (helpdesk mode)

Multiple HTTP users can map to the same XMPP JID (as shown above with `support1` and `support2`). They share a single XMPP connection and receive each other's sent messages in real time via server-side broadcast — no XMPP carbons required.

### Dev mode

Set `DEV_MODE=true` (environment variable or `dev.enabled: true` in config) to:
- Accept `?user=<username>` query parameter as the username
- Fall back to `dev.username` if no header or param is present
- Use `dev.password` for all XMPP connections that lack a credentials entry

**Never use dev mode in production** — it bypasses all authentication.

---

## Deployment

### Docker Compose (recommended for development)

```bash
# Start ejabberd + conduit
docker compose up -d

# Create XMPP accounts (first time only)
docker exec -it webchat-ejabberd-1 ejabberdctl register alice example.com password123
docker exec -it webchat-ejabberd-1 ejabberdctl register bob   example.com password456

# Open http://localhost:8080?user=alice in one tab
# Open http://localhost:8080?user=bob  in another
```

### Systemd (Linux production)

See [`conduit.service`](conduit.service) — a ready-to-use unit file.

```bash
# Build binary
go build -o /usr/local/bin/conduit ./cmd/conduit

# Install config
sudo mkdir -p /etc/conduit
sudo cp config/config.yaml   /etc/conduit/config.yaml
sudo cp config/users.json.example       /etc/conduit/users.json
sudo cp config/credentials.json.example /etc/conduit/credentials.json
sudo chmod 640 /etc/conduit/credentials.json

# Install and start service
sudo cp conduit.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now conduit
sudo journalctl -u conduit -f
```

### Nginx reverse proxy example

```nginx
server {
    listen 443 ssl;
    server_name chat.example.com;

    # Authentik / SSO sets this header after authentication
    auth_request /auth;
    auth_request_set $remote_user $upstream_http_x_remote_user;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header X-Remote-User $remote_user;

        # WebSocket support
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 3600s;
    }
}
```

---

## Development

### Prerequisites

- Go 1.24+
- Docker + Docker Compose
- An ejabberd instance (the provided `docker-compose.yml` handles this)

### Running locally

```bash
# 1. Start ejabberd
docker compose up -d ejabberd

# 2. Wait for ejabberd to be healthy, then create test accounts
docker exec -it webchat-ejabberd-1 ejabberdctl register alice example.com password123
docker exec -it webchat-ejabberd-1 ejabberdctl register bob   example.com password456

# 3. Run Conduit in dev mode
go run ./cmd/conduit -config config/config.dev.yaml

# 4. Open tabs
#   http://localhost:8080?user=alice
#   http://localhost:8080?user=bob
```

### Running tests

```bash
go test ./...
```

### Project conventions

- All mellium/XMPP imports are confined to `internal/xmpp`. Nothing outside that package imports mellium directly.
- The `XMPPConn` interface in `internal/xmpp/interfaces.go` is the only type the rest of the application sees.
- `session.Serve` from mellium **blocks** until the XMPP session ends — always call it in a goroutine.
- mellium replays the stanza start element into handler `TokenReadEncoder`s before calling the handler. Handlers must call `d.Token()` once to discard it before reading child elements.

---

## Protocol Reference

All WebSocket messages are JSON. The browser sends **inbound** messages; the server sends **outbound** messages.

### Inbound (browser → server)

| `type` | Fields | Description |
|---|---|---|
| `chat` | `to`, `body` | Send a 1:1 message |
| `room_message` | `room`, `body` | Send a MUC room message |
| `join_room` | `room` | Join a MUC room |
| `leave_room` | `room` | Leave a MUC room |
| `presence` | `show` | Update own presence (away/dnd/…) |
| `add_contact` | `to`, `name` | Add to roster + send subscription request |
| `remove_contact` | `to` | Remove from roster + unsubscribe |
| `accept_subscription` | `to` | Approve a contact request |
| `decline_subscription` | `to` | Decline a contact request |
| `history` | `conversation`, `before`, `limit` | Fetch MAM history (paginated backwards) |
| `discover_rooms` | — | Request list of public MUC rooms |

### Outbound (server → browser)

| `type` | Key fields | Description |
|---|---|---|
| `connected` | `from`, `brand` | Session ready; `from` is your bare JID, `brand` is the configured page title |
| `roster` | `payload: RosterItem[]` | Full roster on connect |
| `roster_update` | `payload: RosterItem` | Single roster item changed |
| `presence` | `from`, `body` (show value) | Contact presence change |
| `chat` | `from`, `to`, `body`, `timestamp` | 1:1 message (incoming or self-echo) |
| `room_message` | `room`, `from`, `nick`, `body`, `timestamp` | MUC message |
| `room_occupants` | `room`, `from`, `show`, `payload: Occupant[]` | Occupant join/leave |
| `history_batch` | `payload: OutboundMessage[]` | MAM history page |
| `room_list` | `payload: RoomInfo[]` | MUC room discovery results |
| `subscribe_request` | `from` | Someone requests to add you as a contact |
| `error` | `error` | Server-side error description |
