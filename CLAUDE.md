# Conduit — project context for Claude

## What this is

**Conduit** is a web-based XMPP chat client written in Go. It bridges browser WebSocket connections to an ejabberd XMPP server. The binary is built from `cmd/conduit`.

Full architecture and flow documentation is in [README.md](README.md).

## Stack

- Go 1.24, module `conduit`
- [`mellium.im/xmpp v0.23.0`](https://mellium.im/xmpp) — XMPP library
- `github.com/gorilla/websocket` — WebSocket transport
- Vanilla JS frontend (no build step), embedded via `//go:embed`
- ejabberd in Docker for development (`docker-compose.yml`)

## Running locally

```bash
docker compose up -d ejabberd
# first time: register test accounts
docker exec -it webchat-ejabberd-1 ejabberdctl register alice example.com password123
docker exec -it webchat-ejabberd-1 ejabberdctl register bob   example.com password456

go run ./cmd/conduit -config config/config.dev.yaml
# http://localhost:8080?user=alice  and  http://localhost:8080?user=bob
```

## Key architectural facts

- **One XMPP connection per bare JID**, shared across all browser tabs and all HTTP users mapped to that JID (helpdesk-mode).
- **`session.Serve` blocks** — always called in a goroutine inside `serveSession`.
- **mellium replays the stanza start element** into handler `TokenReadEncoder`s before calling the handler. Every handler calls `d.Token()` once to discard it before reading children. Forgetting this causes the first child element to be silently skipped.
- **`GetOrCreate` connects outside the global lock** — XMPP negotiation is slow; holding the write lock blocks other users from connecting concurrently.
- **Sent chat messages are echoed via `user.Broadcast`** in the bridge (not via XMPP carbons) so all sessions for a JID see messages sent from any tab.
- **MAM uses RSM pagination** (`Last:true` + `PageID`) not MAM:2#extended data form fields (`before-id`/`after-id`) — ejabberd does not support the extended fields.

## Package layout (one-liner each)

| Package | Role |
|---|---|
| `cmd/conduit` | Entry point, wires everything |
| `config` | YAML config + defaults |
| `internal/auth` | Username → JID mapping, HTTP middleware |
| `internal/audit` | Structured audit log of auth/session events |
| `internal/httpserver` | HTTP server, /healthz, /readyz, graceful shutdown |
| `internal/websocket` | WebSocket upgrade, read/write pumps |
| `internal/bridge` | JSON protocol ↔ XMPP operations (per session) |
| `internal/user` | User lifecycle, multi-session Broadcast, idle shutdown |
| `internal/session` | One per browser tab; holds `Send chan OutboundMessage` |
| `internal/xmpp` | All mellium code lives here and only here |
| `internal/history` | MAM query orchestration |
| `internal/frontend` | Embeds `web/` static assets |
| `pkg/protocol` | Shared JSON message types (inbound + outbound) |

## Files worth knowing

- `internal/xmpp/interfaces.go` — `XMPPConn` interface + all event/query types. Start here when adding XMPP features.
- `internal/xmpp/conn.go` — Connection lifecycle, mux registration, stanza handlers, XML child readers.
- `internal/xmpp/muc.go` — Room join/leave/rejoin.
- `internal/xmpp/roster.go` — Roster add/remove, subscription accept/decline.
- `internal/xmpp/disco.go` — MUC room discovery via disco#items + disco#info. Queries multiple conference hosts (`muc_host` + `muc_hosts` + per-request extras), each bounded by `DialTimeout` so one dead remote server can't stall discovery.
- `internal/xmpp/mam.go` — MAM history queries.
- `pkg/protocol/protocol.go` — Every message type constant + struct. Check here before adding new message types.
- `internal/bridge/bridge.go` — Central dispatch for inbound messages. Also contains `MockXMPPConn` for tests — add a stub here whenever `XMPPConn` grows a new method.
- `internal/frontend/web/app.js` — All client-side logic (state, WebSocket handling, rendering).
- `config/config.dev.yaml` — Dev config (TLS insecure, credentials.json.example, ?user= param enabled).

## WebSocket protocol (quick ref)

Inbound (browser → server): `chat`, `room_message`, `join_room`, `leave_room`, `presence`, `add_contact`, `remove_contact`, `accept_subscription`, `decline_subscription`, `history`, `discover_rooms`

Outbound (server → browser): `connected`, `roster`, `roster_update`, `presence`, `chat`, `room_message`, `room_occupants`, `history_batch`, `room_list`, `subscribe_request`, `message_error`, `error`

Full field details in [README.md — Protocol Reference](README.md#protocol-reference).

## ejabberd notes

- Docker image: `ejabberd/ecs:latest`
- Config: `ejabberd.yml` (mounted read-only)
- Self-signed cert auto-generated at `/home/ejabberd/conf/server.pem`; referenced in `certfiles:` config
- Must always register `mx.StartTLS` in mellium feature list even when not using TLS — ejabberd advertises it and mellium errors with "features advertised out of order" if the handler is missing
- `mod_presence` does not exist (presence is built-in); `mod_announce` requires `mod_adhoc`
- MUC host: `conference.example.com`
- MAM enabled with `default: always` in `mod_mam`

## Known gotchas / past bugs

- **Bob blocking Alice**: `session.Serve` blocks. Was called synchronously while holding the user manager write lock. Fixed: `Dial` returns immediately; supervisor goroutine handles serving; `GetOrCreate` connects outside the lock.
- **Empty message bodies**: mellium replays the stanza start element. Handlers were `Skip()`-ing it as the first element, consuming all children. Fixed: `d.Token()` discard at top of `readMessageChildren` and `readPresenceChildren`.
- **Messages seen 3×**: auto-history on DM open raced with live messages + `renderMessages()` re-rendering state that had duplicates. Fixed: removed auto-history; added `isDuplicate()` check.
- **MAM `before-id` error**: ejabberd doesn't support MAM:2#extended data form fields. Fixed: use RSM `Last:true` + `PageID` instead of `BeforeID`/`AfterID`.
- **join_room never sent**: `btn-do-join-room` pre-set `state.rooms[jid]` before calling `openRoom`, so `openRoom`'s guard was always false. Fixed: let `openRoom` handle initialization.

## Current state / what's working

- 1:1 chat with message history (MAM, load-more button)
- MUC rooms: join/leave, message send/receive, occupant list panel, room discovery modal (groups results by server; users can browse/join rooms on other federated servers by adding a conference host)
- Roster: add/remove contacts, subscription request flow (accept/decline modal)
- Presence indicators (online/away/dnd/offline dots)
- Delivery-failure feedback: bounced messages (`<message type=error>`) surface as a warning line in the affected DM/room, with a toast fallback
- Multi-session: multiple tabs and multiple HTTP users mapped to the same JID all stay in sync
- Discord-style UI: message grouping by sender, compact rows, sender separator lines, unread badges
- Unread indicators: per-conversation badges **and** a `(N)` prefix on the browser-tab title (`updateTitle` in `app.js`)
- MUC join/leave notices: centered `.msg-system` lines ("X joined/left the room"). Computed purely client-side in `handleOccupants`; the initial occupant burst is suppressed until our own self-presence arrives (XEP-0045 sends it last)
- Configurable page title: `brand` config key → sent on the `connected` message → sets the tab title in `app.js`
- Notification sounds + settings modal (`#modal-sound-settings`)
- Dev mode: `?user=alice` / `?user=bob` param in URL

## Deliberately kept simple

This is a basic XMPP webchat for compatibility with standard XMPP clients — not a
feature-complete client. Prefer leaving these out unless explicitly requested:
typing indicators (XEP-0085), delivery receipts (XEP-0184), browser push
notifications, file/image sharing, OMEMO/encryption.
