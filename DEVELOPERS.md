# Conduit — Developer Guide

This guide is for developers who want to understand, extend, or debug Conduit. For deployment and configuration see [README.md](README.md).

---

## Table of Contents

- [Getting Started](#getting-started)
- [Setting Up Test Data in ejabberd](#setting-up-test-data-in-ejabberd)
- [Project Structure](#project-structure)
- [Core Concepts](#core-concepts)
- [Data Flow Walkthrough](#data-flow-walkthrough)
- [Debugging](#debugging)
- [Testing](#testing)
- [How to Add a Feature](#how-to-add-a-feature)
- [Possible Future Features](#possible-future-features)
- [Known Gotchas](#known-gotchas)

---

## Getting Started

### Prerequisites

- Go 1.24+
- Docker + Docker Compose (for ejabberd)
- Any modern browser (Firefox or Chromium recommended for DevTools)

### First-time setup

```bash
# Clone and enter the repo
git clone <repo-url> conduit
cd conduit

# Start ejabberd
docker compose up -d ejabberd

# Wait ~10 seconds for ejabberd to fully start, then register test accounts
docker exec -it webchat-ejabberd-1 ejabberdctl register alice example.com password123
docker exec -it webchat-ejabberd-1 ejabberdctl register bob   example.com password456

# Start Conduit
go run ./cmd/conduit -config config/config.dev.yaml
```

Open two browser tabs:
- `http://localhost:8080?user=alice`
- `http://localhost:8080?user=bob`

You should see both users connect and appear in each other's roster after adding contacts.

### Dev mode conveniences

`config/config.dev.yaml` enables:
- `?user=<name>` query parameter instead of an auth header — easy to switch identities without a proxy
- `log.level: debug` — verbose structured log output for every XMPP event and bridge dispatch
- `tls_mode: starttls-insecure` — skips TLS certificate verification for the local ejabberd container

---

## Setting Up Test Data in ejabberd

Everything below runs inside the ejabberd container. The container name is `webchat-ejabberd-1` when started with `docker compose up`.

### User accounts

```bash
# Register accounts (do once after first `docker compose up`)
docker exec -it webchat-ejabberd-1 ejabberdctl register alice   example.com password123
docker exec -it webchat-ejabberd-1 ejabberdctl register bob     example.com password456
docker exec -it webchat-ejabberd-1 ejabberdctl register charlie example.com password789

# List all registered users
docker exec -it webchat-ejabberd-1 ejabberdctl registered_users example.com

# Change a password
docker exec -it webchat-ejabberd-1 ejabberdctl change_password alice example.com newpassword

# Delete a user
docker exec -it webchat-ejabberd-1 ejabberdctl unregister alice example.com
```

### MUC (chat rooms)

The MUC service host is `conference.example.com` in the dev config.

**Rooms are created automatically on first join.** The dev ejabberd config (`ejabberd.yml`) sets `persistent: true` and `public: true` as defaults, so a room created by joining stays alive after everyone leaves and shows up in the room browser.

To create a room:
1. Open Conduit in the browser
2. Click **+** next to Rooms in the sidebar
3. Type any room name (e.g. `general`) and click Join

The full JID will be `general@conference.example.com`.

```bash
# List rooms that are currently online (have at least one occupant)
docker exec -it webchat-ejabberd-1 ejabberdctl muc_online_rooms example.com

# List *all* registered (persistent) rooms, including empty ones
docker exec -it webchat-ejabberd-1 ejabberdctl muc_online_rooms_by_regex example.com ".*"

# Kick all users from a room and destroy it
docker exec -it webchat-ejabberd-1 ejabberdctl destroy_room general conference.example.com
```

> **Tip:** A room only appears in the Conduit room browser if it is marked `public: true`. The dev config sets this as the default. Password-protected rooms can be configured through the ejabberd admin panel after creation.

### Seeding message history

Send messages between users to populate the MAM archive for testing history load:

```bash
# Send a message from alice to bob (via ejabberdctl)
docker exec -it webchat-ejabberd-1 ejabberdctl send_message chat \
  alice@example.com bob@example.com "" "Hello from alice"

# Send multiple messages to build up history
for i in $(seq 1 10); do
  docker exec -it webchat-ejabberd-1 ejabberdctl send_message chat \
    alice@example.com bob@example.com "" "Message $i from alice"
done
```

### Checking the MAM archive

```bash
# Count archived messages for a user
docker exec -it webchat-ejabberd-1 ejabberdctl get_loglevel

# The easiest way: just send messages in the UI and then press "Load history"
# in Conduit — if MAM is working you'll see them after a browser refresh
```

### Admin web panel

`http://localhost:5280/admin` — username `admin`, password is set in `ejabberd.yml`. Useful for inspecting rooms, users, and connected sessions without using the CLI.

---

## Project Structure

```
cmd/conduit/          Entry point — wires all components, reads config, handles OS signals
config/                YAML config types + defaults
internal/
  audit/               Structured audit log of auth/session events (login/logout/rejected)
  auth/                Maps HTTP usernames to XMPP JIDs; HTTP auth middleware
  bridge/              Translates browser JSON messages ↔ XMPP operations (one per session)
  frontend/            Embeds static files from web/ into the binary
  history/             MAM query orchestration; collects results and broadcasts history batches
  httpserver/          HTTP server, graceful shutdown, /healthz, /readyz, security headers
  sanitize/            Strips HTML tags from user-supplied strings (XSS defense)
  session/             One per browser tab; holds a Send channel for outbound messages
  user/                User lifecycle, one XMPP connection per JID, multi-session broadcast
  websocket/           WebSocket upgrade, read/write pumps, JSON codec
  xmpp/                All mellium code lives here and ONLY here
    conn.go            Connection lifecycle, mux registration, stanza handlers
    interfaces.go      XMPPConn interface + all Event/Query types — start here
    mam.go             MAM (message archive) query builder and result dispatcher
    muc.go             Room join, leave, rejoin after reconnect
    roster.go          Roster fetch, add/remove, subscription accept/decline
    disco.go           MUC room discovery via disco#items + disco#info
pkg/protocol/          Shared JSON message types for the WebSocket protocol
web/
  index.html           Single-page shell
  app.js               All client-side logic (state machine, WebSocket, rendering)
  style.css            Discord-style dark theme
  sounds/              Drop dm.mp3 and mention.mp3 here for custom notification audio
```

**The most important file to read first:** [internal/xmpp/interfaces.go](internal/xmpp/interfaces.go) — it defines `XMPPConn`, all `EventType` constants, and every data structure that crosses the xmpp/bridge boundary.

---

## Core Concepts

### One XMPP connection per JID

The `user.Manager` keeps a `map[string]*User` keyed by bare JID. When a browser tab connects, `GetOrCreate` either reuses an existing `User` or dials a new XMPP connection. Every browser tab for the same JID shares that single connection.

```
alice (tab 1) ─┐
alice (tab 2) ─┤──▶  User{JID:"alice@..."} ──▶ XMPPConn ──▶ ejabberd
alice (tab 3) ─┘
```

Messages received from ejabberd are fanned out to all sessions via `user.Broadcast`.

### Sent messages are self-echoed

When a chat message is sent, the bridge calls `conn.SendChat` **and** immediately calls `user.Broadcast` with the outbound copy. This is how a message sent from tab 1 appears instantly in tab 2 — without waiting for XMPP carbons (XEP-0280).

### The XMPPConn interface is the seam

Everything outside `internal/xmpp` talks to XMPP through the `XMPPConn` interface. Tests replace it with `MockXMPPConn` in `bridge/bridge.go`. If you add a new XMPP capability, the process is always:

1. Add the method to `XMPPConn` in `interfaces.go`
2. Implement it in `conn.go` (or a new file in `internal/xmpp/`)
3. Add a stub to `MockXMPPConn` in `bridge/bridge.go`
4. Wire it in `bridge/bridge.go` under a new `protocol.Type*` constant

### mellium quirks you must know

- **`session.Serve` blocks** — it's always called inside a goroutine (`supervisor` in `conn.go`).
- **Stanza start element replay** — mellium calls each handler with a `TokenReadEncoder` whose first token is the stanza's opening element, replayed. Every handler must call `d.Token()` once at the top to discard it before reading child elements. Forgetting this silently skips the first child.
- **MAM pagination** — Use RSM (`Last:true` + `PageID`), not MAM:2#extended data form fields (`before-id`/`after-id`). ejabberd rejects the extended fields.

---

## Data Flow Walkthrough

### Receiving a 1:1 message

```
ejabberd sends <message type="chat"> XML
  ↓
mellium: session.Serve dispatches to mux handler registered for chat messages
  ↓
conn.handleChatMsg (conn.go)
  reads body text + optional <delay> timestamp
  → emits Event{Type:EventChat, From, Body, Time} onto conn.events channel
  ↓
user.eventLoop (user.go) — goroutine draining conn.Events()
  calls eventToOutbound(evt) → &OutboundMessage{type:"chat", from, body, timestamp}
  calls user.Broadcast(msg)
  ↓
user.Broadcast sends msg to every session.Send channel
  ↓
websocket.writePump (websocket/) — one goroutine per tab
  encodes OutboundMessage as JSON → WebSocket frame → browser
  ↓
app.js: ws.onmessage → dispatch(msg) → handleChat(msg)
  renders message in the chat window
```

### Sending a 1:1 message

```
User types a message, presses Enter
  ↓
app.js: sendMessage() → ws.send(JSON.stringify({type:"chat", to, body}))
  ↓
websocket.readPump → decodes InboundMessage
  ↓
bridge.HandleInbound (bridge.go) case TypeChat:
  sanitize.Text(body)                          // strip any HTML
  conn.SendChat(ctx, to, body)                 // sends XML to ejabberd
  user.Broadcast(OutboundMessage{type:"chat"}) // echo to all sessions of this JID
  ↓
All tabs for this JID receive the echo and render it
```

### Adding a contact (subscription flow)

```
User clicks "Add contact", enters bob@example.com
  ↓
{type:"add_contact", to:"bob@example.com"}
  ↓
bridge → conn.AddContact → mellium sends <iq> roster set + <presence type="subscribe">
  ↓
ejabberd delivers <presence type="subscribe"> to bob
  ↓
Bob's conn.handlePresence → EventSubscribeRequest{From:"alice@example.com"}
  ↓
Bob's browser: handleSubscribeRequest() shows accept/decline modal
  ↓
{type:"accept_subscription", to:"alice@example.com"}
  ↓
bridge → conn.AcceptSubscription → <presence type="subscribed">
```

---

## Debugging

### Structured log output

Run with `log.level: debug` in your config (already set in `config.dev.yaml`). Every log line is structured key=value. Useful filters:

```bash
# Show only XMPP events
go run ./cmd/conduit -config config/config.dev.yaml 2>&1 | grep "EventType\|inbound\|outbound"

# Watch MAM activity
go run ./cmd/conduit -config config/config.dev.yaml 2>&1 | grep -i mam

# Watch a specific JID
go run ./cmd/conduit -config config/config.dev.yaml 2>&1 | grep 'jid=alice'
```

Key log fields to look for:

| Field | Emitted by | Meaning |
|---|---|---|
| `jid=` | most packages | which XMPP account |
| `session_id=` | bridge, websocket | which browser tab |
| `type=` | bridge | inbound message type |
| `err=` | anywhere | error detail |
| `room=` | xmpp, bridge | MUC room JID |

### Browser DevTools

Open the **Network** tab → filter by **WS** → click the `/ws` connection → **Messages** panel. You can inspect every JSON frame in both directions in real time.

To send a raw message from the browser console:
```js
// Send a chat message manually
window._ws.send(JSON.stringify({type:"chat", to:"bob@example.com", body:"test"}))
```

`window._ws` is the WebSocket object exposed by `app.js`.

### ejabberd admin panel

`http://localhost:5280/admin` — default credentials are in `ejabberd.yml`. Useful for:
- Checking registered accounts
- Inspecting MUC rooms and their members
- Viewing the MAM archive

### Common problems

| Symptom | Likely cause | Fix |
|---|---|---|
| XMPP connect fails with `features advertised out of order` | `mx.StartTLS` missing from features list | Always register StartTLS even when not using it; ejabberd advertises it |
| Messages received with empty body | Stanza start element not discarded | Add `d.Token()` at top of the handler before reading child elements |
| Room join appears to succeed but messages don't arrive | Room JID wrong (must include MUC host) | Use `chat@conference.example.com` not `chat@example.com` |
| MAM returns error about `before-id` | ejabberd doesn't support MAM:2#extended fields | Use RSM pagination: `Last:true` + `PageID`, not `BeforeID`/`AfterID` |
| Second browser tab doesn't see messages | `user.Broadcast` not called | Make sure outbound events go through `user.Broadcast`, not directly to `session.Send` |

---

## Testing

```bash
# Run all tests
go test ./...

# Run with verbose output
go test -v ./...

# Run a single package
go test ./internal/bridge/...
```

### Test architecture

Tests avoid a real XMPP server. The seam is `XMPPConn`:

- `bridge.MockXMPPConn` — records what methods were called and with what arguments
- `user.NewTestUser` — creates a User with a MockXMPPConn, no goroutines
- `session.NewManager().Create(jid)` — creates a real Session with a buffered Send channel

A typical bridge test looks like:

```go
func TestBridge_SendChat(t *testing.T) {
    mc  := bridge.NewMockConn()
    u   := user.NewTestUser("alice@example.com", mc)
    sess := session.NewManager().Create("alice@example.com")
    b   := bridge.New(u, sess, nil, testLog())

    b.HandleInbound(ctx, protocol.InboundMessage{
        Type: protocol.TypeChat,
        To:   "bob@example.com",
        Body: "Hello",
    })

    assert.Equal(t, "bob@example.com", mc.SentTo)
}
```

### Adding tests for new features

When you add a new method to `XMPPConn`:
1. Add a stub to `MockXMPPConn` in `bridge/bridge.go` (returns `nil` or a zero value)
2. Add a field to `MockXMPPConn` to record what was called (e.g. `DiscoveredRooms bool`)
3. Write a test in `bridge/bridge_test.go` that asserts the field was set

---

## How to Add a Feature

This section walks through adding a new end-to-end feature. We'll use **typing indicators** (XEP-0085) as an example.

### Step 1 — Define the protocol message types

In [pkg/protocol/protocol.go](pkg/protocol/protocol.go):

```go
const (
    // inbound (browser → server)
    TypeTyping     = "typing"      // user started typing
    TypeTypingStop = "typing_stop" // user stopped typing

    // outbound (server → browser)
    TypeTypingNotification = "typing_notification"
)
```

### Step 2 — Add the method to XMPPConn

In [internal/xmpp/interfaces.go](internal/xmpp/interfaces.go):

```go
type XMPPConn interface {
    // ... existing methods ...
    SendTyping(ctx context.Context, to string, active bool) error
}
```

### Step 3 — Implement in internal/xmpp

Create `internal/xmpp/chatstates.go` (or add to `conn.go`):

```go
func (c *conn) SendTyping(ctx context.Context, to string, active bool) error {
    // encode <message><composing xmlns="..."/> or <paused/></message> via mellium
}
```

Also register an inbound handler in `conn.go`'s `buildMux` to receive typing notifications from other users and emit them as an `Event`.

### Step 4 — Add to MockXMPPConn

In [internal/bridge/bridge.go](internal/bridge/bridge.go):

```go
type MockXMPPConn struct {
    // ... existing fields ...
    TypingSentTo string
    TypingActive bool
}

func (m *MockXMPPConn) SendTyping(_ context.Context, to string, active bool) error {
    m.TypingSentTo, m.TypingActive = to, active
    return nil
}
```

### Step 5 — Wire into the bridge

In [internal/bridge/bridge.go](internal/bridge/bridge.go) `HandleInbound`:

```go
case protocol.TypeTyping:
    conn.SendTyping(ctx, msg.To, true)
case protocol.TypeTypingStop:
    conn.SendTyping(ctx, msg.To, false)
```

### Step 6 — Broadcast the inbound event

In [internal/user/user.go](internal/user/user.go) `eventToOutbound`:

```go
case xmpp.EventTyping:
    return &protocol.OutboundMessage{
        Type: protocol.TypeTypingNotification,
        From: evt.From,
        Body: evt.Body, // "composing" or "paused"
    }
```

### Step 7 — Handle in the frontend

In [internal/frontend/web/app.js](internal/frontend/web/app.js):

```js
// outbound
case 'typing_notification':
    showTypingIndicator(msg.from, msg.body === 'composing')
    break

// inbound — send when user types in compose box
compose.addEventListener('input', () => {
    ws.send(JSON.stringify({ type: state.activeChat.type === 'room'
        ? 'room_typing' : 'typing',
        to: state.activeChat.jid }))
})
```

### Step 8 — Write a test

```go
func TestBridge_Typing(t *testing.T) {
    mc   := bridge.NewMockConn()
    u    := user.NewTestUser("alice@example.com", mc)
    sess := session.NewManager().Create("alice@example.com")
    b    := bridge.New(u, sess, nil, testLog())

    b.HandleInbound(ctx, protocol.InboundMessage{
        Type: protocol.TypeTyping,
        To:   "bob@example.com",
    })

    assert.Equal(t, "bob@example.com", mc.TypingSentTo)
    assert.True(t, mc.TypingActive)
}
```

---

## Possible Future Features

Conduit is intentionally a **simple** XMPP webchat — the goal is a solid, working
1:1 + MUC chat that interoperates with standard XMPP clients, not a feature-complete
client. The items below are *optional* extensions that fit cleanly into the existing
architecture if a need arises; none are required, and most should stay out unless
explicitly asked for.

### 1. Typing indicators (XEP-0085)

Show a "Alice is typing…" indicator in the chat header. See the walkthrough above for the implementation path.

**Complexity:** Medium. The tricky part is debouncing the outgoing events (don't send one per keystroke) and clearing the indicator after a timeout if `paused` is never received.

**Files to touch:** `interfaces.go`, new `chatstates.go`, `bridge.go`, `user.go`, `app.js`, `index.html`, `style.css`.

---

### 2. Message delivery receipts (XEP-0184)

Show a ✓ or ✓✓ icon next to sent messages when the recipient's client acknowledges delivery.

**Complexity:** Low–Medium. Send a `<request>` element with each `<message>`. Handle inbound `<received>` stanzas and emit an event back to the browser. The frontend needs to match receipt IDs to rendered messages — each message row needs a `data-id` attribute.

**Files to touch:** `conn.go`, `interfaces.go`, `bridge.go`, `user.go`, `protocol.go`, `app.js`.

---

### 3. Browser push notifications (Web Notifications API)

Show a native OS notification when a message arrives and the tab is not focused.

**Complexity:** Low. Entirely frontend work. Request `Notification.permission` on first use, then call `new Notification(...)` inside `handleChat` and `handleRoomMsg` when `document.hidden === true`. No backend changes needed.

**Files to touch:** `app.js` only.

---

### 4. Reconnect feedback UI

Show a banner ("Reconnecting…" / "Connected") when the WebSocket disconnects.

**Complexity:** Low. The WebSocket `onclose` event is already handled in `app.js`. Add a CSS overlay or toast that appears on close and disappears when the next `connected` message arrives after reconnect.

**Files to touch:** `app.js`, `style.css`.

---

### 5. File transfer / image sharing (XEP-0363 HTTP Upload)

Allow users to send images or files by uploading to ejabberd's HTTP upload module and sharing the URL as a message.

**Complexity:** High. Requires implementing an IQ request to discover the upload slot URL, then a `PUT` from the browser to the provided URL, then sending the download URL as a chat message. The frontend needs a file picker and upload progress indicator.

**Files to touch:** `interfaces.go`, new `upload.go`, `bridge.go`, `protocol.go`, `app.js`, `index.html`, `style.css`.

---

## Known Gotchas

These are bugs that have already been fixed once. They are listed here so you don't re-introduce them.

| Issue | Root cause | Rule |
|---|---|---|
| Messages seen 3× | Auto-history on open raced with live messages; `renderMessages()` re-rendered state with duplicates | Always check `isDuplicate()` in `appendMessageToDOM`; don't trigger history on conversation open |
| Empty message bodies | mellium replays the stanza start element; handler was `Skip()`-ing it, consuming children | Every handler calls `d.Token()` once at the top before reading children |
| `join_room` never sent | `openRoom()` guard was always false because the caller pre-populated `state.rooms[jid]` | Only `openRoom()` initialises `state.rooms[jid]`; callers must not touch it first |
| MAM `before-id` error | ejabberd doesn't support MAM:2#extended data form fields | Use RSM `Last:true` + `PageID`; never use `BeforeID`/`AfterID` |
| Session B blocks while A connects | `GetOrCreate` held the write lock during XMPP negotiation | Always dial outside the lock; acquire write lock only to register the completed User |
