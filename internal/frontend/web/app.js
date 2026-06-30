'use strict';

// ── Sound notifications ────────────────────────────────────────────────────
const sound = {
  enabled:    localStorage.getItem('snd_enabled')    !== 'false',
  volume:     parseFloat(localStorage.getItem('snd_volume')  ?? '0.4'),
  onlyHidden: localStorage.getItem('snd_onlyhidden') !== 'false',
};

// Sound files served from /sounds/. Falls back to Web Audio synth if missing.
const SOUND_FILES = {
  dm:      '/sounds/dm.mp3',
  mention: '/sounds/mention.mp3',
};

// Probed at startup: true = file exists, false = use synth fallback.
const soundAvailable = { dm: false, mention: false };

async function probeSoundFiles() {
  for (const [type, url] of Object.entries(SOUND_FILES)) {
    try {
      const res = await fetch(url, { method: 'HEAD' });
      soundAvailable[type] = res.ok;
    } catch {
      soundAvailable[type] = false;
    }
  }
}

function soundSave() {
  localStorage.setItem('snd_enabled',    sound.enabled);
  localStorage.setItem('snd_volume',     sound.volume);
  localStorage.setItem('snd_onlyhidden', sound.onlyHidden);
}

// ── Web Audio synth fallback ───────────────────────────────────────────────
let _audioCtx = null;
function audioCtx() {
  if (!_audioCtx || _audioCtx.state === 'closed') {
    _audioCtx = new (window.AudioContext || window.webkitAudioContext)();
  }
  if (_audioCtx.state === 'suspended') _audioCtx.resume();
  return _audioCtx;
}

function playTone(ctx, freq, startAt, duration, vol) {
  const osc  = ctx.createOscillator();
  const gain = ctx.createGain();
  osc.connect(gain);
  gain.connect(ctx.destination);
  osc.type = 'sine';
  osc.frequency.setValueAtTime(freq, ctx.currentTime + startAt);
  gain.gain.setValueAtTime(0,   ctx.currentTime + startAt);
  gain.gain.linearRampToValueAtTime(vol, ctx.currentTime + startAt + 0.01);
  gain.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + startAt + duration);
  osc.start(ctx.currentTime + startAt);
  osc.stop(ctx.currentTime  + startAt + duration + 0.02);
}

function playToneFallback(type) {
  const ctx = audioCtx();
  const v   = sound.volume;
  if (type === 'dm') {
    playTone(ctx, 880,  0,    0.18, v);
    playTone(ctx, 1100, 0.18, 0.18, v * 0.7);
  } else {
    playTone(ctx, 660,  0,    0.12, v);
    playTone(ctx, 880,  0.13, 0.12, v);
    playTone(ctx, 1320, 0.26, 0.22, v * 0.9);
  }
}

// ── Main notification entry point ──────────────────────────────────────────
// type: 'dm' | 'mention'
function playNotification(type) {
  if (!sound.enabled) return;
  if (sound.onlyHidden && !document.hidden) return;
  try {
    if (soundAvailable[type]) {
      const audio = new Audio(SOUND_FILES[type]);
      audio.volume = sound.volume;
      audio.play().catch(() => playToneFallback(type));
    } else {
      playToneFallback(type);
    }
  } catch (e) {
    console.warn('notification sound error', e);
  }
}

function isMentioned(body) {
  if (!body || !state.myJID) return false;
  const nick = localPart(state.myJID);
  return body.toLowerCase().includes('@' + nick.toLowerCase());
}

// ── State ──────────────────────────────────────────────────────────────────
const state = {
  ws:           null,
  myJID:        '',
  activeConv:   null,  // { type: 'dm'|'room', jid: string }
  roster:       {},    // jid → { jid, name, show, status }
  rooms:        {},    // jid → { jid, nick, occupants: {} }
  messages:     {},    // conv-key → [{ from, body, ts, nick }]
  unread:       {},    // conv-key → count
};

// ── Document title (unread indicator across tabs) ──────────────────────────
// Base browser-tab title. Overridden by the server's configured `brand` on
// connect (see handleConnected); the HTML default is just a fallback.
let baseTitle = document.title;

function updateTitle() {
  const total = Object.values(state.unread).reduce((sum, n) => sum + n, 0);
  document.title = total > 0 ? `(${total}) ${baseTitle}` : baseTitle;
}

// ── DOM refs ───────────────────────────────────────────────────────────────
const $ = id => document.getElementById(id);
const messagesEl      = $('messages');
const composeEl       = $('compose');
const btnSend         = $('btn-send');
const btnLoadHistory  = $('btn-load-history');
const dmListEl        = $('dm-list');
const roomListEl      = $('room-list');
const chatTitleEl     = $('chat-title');
const currentJIDEl    = $('current-jid');

// ── WebSocket ──────────────────────────────────────────────────────────────
function connect() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const userParam = new URLSearchParams(location.search).get('user');
  const qs  = userParam ? `?user=${encodeURIComponent(userParam)}` : '';
  const url = `${proto}://${location.host}/ws${qs}`;
  state.ws = new WebSocket(url);

  state.ws.addEventListener('open',    onOpen);
  state.ws.addEventListener('message', onMessage);
  state.ws.addEventListener('close',   onClose);
  state.ws.addEventListener('error',   onError);
}

function onOpen() {
  showToast('Connected');
}

function onClose() {
  showToast('Disconnected — reconnecting…', true);
  setTimeout(connect, 3000);
}

function onError(e) {
  console.error('ws error', e);
}

function send(obj) {
  console.log('[ws send]', obj, 'readyState:', state.ws?.readyState);
  if (state.ws && state.ws.readyState === WebSocket.OPEN) {
    state.ws.send(JSON.stringify(obj));
  }
}

// ── Inbound message dispatch ───────────────────────────────────────────────
function onMessage(ev) {
  let msg;
  try { msg = JSON.parse(ev.data); } catch { return; }

  switch (msg.type) {
    case 'connected':         handleConnected(msg);          break;
    case 'roster':            handleRoster(msg);             break;
    case 'roster_update':     handleRosterUpdate(msg);       break;
    case 'presence':          handlePresence(msg);           break;
    case 'chat':              handleChat(msg);               break;
    case 'room_message':      handleRoomMsg(msg);            break;
    case 'room_occupants':    handleOccupants(msg);          break;
    case 'room_list':         handleRoomList(msg);           break;
    case 'history_batch':     handleHistory(msg);            break;
    case 'subscribe_request': handleSubscribeRequest(msg);   break;
    case 'message_error':     handleMessageError(msg);       break;
    case 'error':             showToast(msg.error || 'Server error', true); break;
  }
}

function handleConnected(msg) {
  if (msg.from) {
    state.myJID = msg.from;
    if (currentJIDEl) currentJIDEl.textContent = msg.from;
  }
  if (msg.brand) {
    baseTitle = msg.brand;
    updateTitle();
  }
}

function handleRoster(msg) {
  const items = msg.payload || [];
  for (const item of items) {
    state.roster[item.jid] = { jid: item.jid, name: item.name || item.jid, show: 'offline', status: '' };
  }
  renderDMList();
}

function handleRosterUpdate(msg) {
  if (!msg.payload) return;
  const item = msg.payload;
  state.roster[item.jid] = { ...state.roster[item.jid], ...item };
  renderDMList();
}

function handlePresence(msg) {
  const jid = bareJID(msg.from);
  if (!state.roster[jid]) {
    state.roster[jid] = { jid, name: jid, show: 'offline', status: '' };
  }
  state.roster[jid].show   = msg.body || 'available';
  state.roster[jid].status = msg.status || '';
  renderDMList();
}

function handleChat(msg) {
  const from = bareJID(msg.from);
  const to   = bareJID(msg.to || '');
  const isSelf = from === state.myJID;
  const peer   = isSelf ? to : from;

  if (!state.roster[peer]) {
    state.roster[peer] = { jid: peer, name: localPart(peer), show: 'available', status: '' };
  }
  const key = convKey('dm', peer);
  pushMessage(key, { from, body: msg.body, ts: msg.timestamp });
  if (!state.activeConv || state.activeConv.jid !== peer) {
    bumpUnread(key);
    renderDMList();
  }
  if (!isSelf) playNotification('dm');
}

function handleRoomMsg(msg) {
  const key       = convKey('room', msg.room);
  const mentioned = isMentioned(msg.body);
  pushMessage(key, { from: msg.from, nick: msg.nick || nickOf(msg.from), body: msg.body, ts: msg.timestamp, mentioned });
  if (!state.activeConv || state.activeConv.jid !== msg.room) {
    bumpUnread(key);
    renderRoomList();
  }
  if (mentioned) playNotification('mention');
}

// ── Room discovery (MUC browse) ──────────────────────────────────────────────
// User-added conference hosts to also browse, persisted per browser.
function getExtraMucHosts() {
  try { return JSON.parse(localStorage.getItem('conduit.mucHosts') || '[]'); }
  catch { return []; }
}
function saveExtraMucHosts(hosts) {
  localStorage.setItem('conduit.mucHosts', JSON.stringify(hosts));
}
function addExtraMucHost(h) {
  const hosts = getExtraMucHosts();
  if (!hosts.includes(h)) { hosts.push(h); saveExtraMucHosts(hosts); }
}
function removeExtraMucHost(h) {
  saveExtraMucHosts(getExtraMucHosts().filter(x => x !== h));
}

// browseRooms asks the server to discover rooms across the configured hosts plus
// any servers the user has added in this browser.
function browseRooms() {
  const list = $('room-browser-list');
  if (list) {
    list.innerHTML = '<p id="room-browser-status" style="color:var(--text-muted);font-size:14px;padding:16px 0">Loading rooms…</p>';
  }
  send({ type: 'discover_rooms', hosts: getExtraMucHosts() });
}

function handleRoomList(msg) {
  const list = $('room-browser-list');
  if (!list) return;
  list.innerHTML = '';

  const rooms   = msg.payload || [];
  const extras  = getExtraMucHosts();

  // Group rooms by their conference host (the domain part of the room JID).
  const byHost = {};
  for (const r of rooms) {
    const h = domainOf(r.jid);
    (byHost[h] = byHost[h] || []).push(r);
  }

  // Server order: the hosts the server queried (config order) first, then any
  // host that appeared in results but wasn't explicitly listed.
  const hosts = [...(msg.hosts || [])];
  for (const h of Object.keys(byHost)) if (!hosts.includes(h)) hosts.push(h);

  if (hosts.length === 0) {
    const p = document.createElement('p');
    p.style.cssText = 'color:var(--text-muted);font-size:14px;padding:16px 0';
    p.textContent = 'No rooms found.';
    list.appendChild(p);
    return;
  }

  for (const host of hosts) {
    const header = document.createElement('div');
    header.className = 'room-browser-server';
    const label = document.createElement('span');
    label.textContent = host;
    header.appendChild(label);
    if (extras.includes(host)) {
      const rm = document.createElement('button');
      rm.className = 'room-browser-server-remove';
      rm.title = 'Stop browsing this server';
      rm.textContent = '×';
      rm.addEventListener('click', () => {
        removeExtraMucHost(host);
        browseRooms();
      });
      header.appendChild(rm);
    }
    list.appendChild(header);

    const hostRooms = (byHost[host] || []).sort((a, b) =>
      (a.name || a.jid).localeCompare(b.name || b.jid));
    if (hostRooms.length === 0) {
      const p = document.createElement('div');
      p.style.cssText = 'color:var(--text-muted);font-size:12px;padding:4px 0 8px';
      p.textContent = 'No public rooms.';
      list.appendChild(p);
      continue;
    }
    for (const room of hostRooms) list.appendChild(renderRoomBrowserRow(room));
  }
}

function renderRoomBrowserRow(room) {
  const row = document.createElement('div');
  row.className = 'room-browser-row';

  const info = document.createElement('div');
  info.style.cssText = 'flex:1;min-width:0';

  const name = document.createElement('div');
  name.style.cssText = 'font-size:14px;font-weight:600;color:var(--text-normal);display:flex;align-items:center;gap:6px';
  name.textContent = room.name || localPart(room.jid);
  if (room.password_protected) {
    const lock = document.createElement('span');
    lock.textContent = '🔒';
    lock.title = 'Password protected';
    lock.style.fontSize = '12px';
    name.appendChild(lock);
  }

  const jidEl = document.createElement('div');
  jidEl.style.cssText = 'font-size:11px;color:var(--text-muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap';
  jidEl.textContent = room.jid;

  info.appendChild(name);
  info.appendChild(jidEl);

  const btn = document.createElement('button');
  btn.className = 'button is-small is-primary';
  btn.textContent = 'Join';
  btn.addEventListener('click', () => {
    closeModal('modal-join-room');
    openRoom(room.jid);
  });

  row.appendChild(info);
  row.appendChild(btn);
  return row;
}

function handleSubscribeRequest(msg) {
  $('subscribe-request-jid').textContent = msg.from;
  openModal('modal-subscribe-request');
}

function handleOccupants(msg) {
  const room = msg.room;
  if (!state.rooms[room]) state.rooms[room] = { jid: room, nick: '', occupants: {}, synced: false };
  const r      = state.rooms[room];
  const nick   = nickOf(msg.from || '');
  const myNick = localPart(state.myJID);

  if (msg.show === 'unavailable') {
    const existed = !!r.occupants[nick];
    delete r.occupants[nick];
    // Only announce leaves once the initial occupant burst is done, and never
    // for ourselves.
    if (r.synced && existed && nick !== myNick) {
      pushSystemMessage(room, `${nick} left the room`);
    }
  } else {
    const isNew = !r.occupants[nick];
    const occ   = (msg.payload || [])[0];
    r.occupants[nick] = { nick, jid: occ?.jid || '', role: occ?.role || 'participant' };
    if (nick === myNick) {
      // Our own (self-)presence is sent last in the join burst (XEP-0045);
      // seeing it means every pre-existing occupant has already arrived, so
      // subsequent joins are real and worth announcing.
      r.synced = true;
    } else if (r.synced && isNew) {
      pushSystemMessage(room, `${nick} joined the room`);
    }
  }
  if (state.activeConv?.type === 'room' && state.activeConv?.jid === room) {
    renderMembersPanel();
  }
}

function pushSystemLine(key, text, warn = false) {
  // Collapse consecutive identical system lines (e.g. servers that send a
  // bounce twice, or repeated join/leave churn).
  const arr = state.messages[key];
  if (arr && arr.length) {
    const last = arr[arr.length - 1];
    if (last.system && last.body === text) return;
  }
  pushMessage(key, { system: true, warn, body: text, ts: new Date().toISOString() });
}

function pushSystemMessage(room, text) {
  pushSystemLine(convKey('room', room), text);
}

// handleMessageError surfaces a bounced (undeliverable) message as a warning
// line in the affected conversation, with a toast fallback if it isn't open.
function handleMessageError(msg) {
  let key, label;
  if (msg.room) { key = convKey('room', msg.room); label = '#' + localPart(msg.room); }
  else if (msg.to) { key = convKey('dm', msg.to); label = msg.to; }
  else return;

  const reason = msg.body || 'message could not be delivered';
  pushSystemLine(key, `⚠ Couldn't deliver to ${label}: ${reason}`, true);

  const active = state.activeConv && convKey(state.activeConv.type, state.activeConv.jid) === key;
  if (!active) showToast(`Undelivered (${label}): ${reason}`, true);
}

function handleHistory(msg) {
  const batch = msg.payload || [];
  for (const m of batch) {
    if (m.type === 'chat') {
      // Determine the peer: if the message was sent by us, the peer is m.to.
      const fromBare = bareJID(m.from);
      const peer = fromBare === state.myJID ? bareJID(m.to) : fromBare;
      if (!peer) continue;
      const key = convKey('dm', peer);
      prependMessage(key, { from: fromBare, body: m.body, ts: m.timestamp });
    } else if (m.type === 'room_message') {
      const key = convKey('room', m.room);
      prependMessage(key, { from: m.from, nick: m.nick || nickOf(m.from), body: m.body, ts: m.timestamp });
    }
  }
  if (state.activeConv) renderMessages();
}

// ── Message store ──────────────────────────────────────────────────────────
function isDuplicate(key, m) {
  return (state.messages[key] || []).some(
    e => e.from === m.from && e.body === m.body && e.ts === m.ts
  );
}

function pushMessage(key, m) {
  if (!state.messages[key]) state.messages[key] = [];
  if (isDuplicate(key, m)) return;
  state.messages[key].push(m);
  if (state.activeConv && convKey(state.activeConv.type, state.activeConv.jid) === key) {
    appendMessageToDOM(m);
    scrollToBottom();
  }
}

function prependMessage(key, m) {
  if (!state.messages[key]) state.messages[key] = [];
  if (isDuplicate(key, m)) return;
  state.messages[key].unshift(m);
}

// ── UI rendering ───────────────────────────────────────────────────────────
function renderDMList() {
  dmListEl.innerHTML = '';
  const sorted = Object.values(state.roster).sort((a, b) =>
    (a.name || a.jid).localeCompare(b.name || b.jid)
  );
  for (const contact of sorted) {
    const key   = convKey('dm', contact.jid);
    const li    = document.createElement('li');
    li.className = 'channel-item' + (isActiveConv('dm', contact.jid) ? ' active' : '');
    li.dataset.jid = contact.jid;

    const avatar = document.createElement('div');
    avatar.className = 'avatar';
    avatar.style.background = jidColor(contact.jid);
    avatar.textContent = (contact.name || contact.jid)[0].toUpperCase();

    const dot = document.createElement('span');
    dot.className = `presence-dot ${showClass(contact.show)}`;

    const name = document.createElement('span');
    name.textContent = contact.name || contact.jid;

    const avatarWrap = document.createElement('div');
    avatarWrap.style.position = 'relative';
    avatarWrap.appendChild(avatar);
    avatarWrap.appendChild(dot);

    li.appendChild(avatarWrap);
    li.appendChild(name);

    const ub = state.unread[key];
    if (ub) {
      const badge = document.createElement('span');
      badge.className = 'unread-badge';
      badge.textContent = ub > 99 ? '99+' : ub;
      li.appendChild(badge);
    }

    const removeBtn = document.createElement('button');
    removeBtn.className = 'contact-remove-btn';
    removeBtn.title = 'Remove contact';
    removeBtn.textContent = '×';
    removeBtn.addEventListener('click', e => {
      e.stopPropagation();
      if (!confirm(`Remove ${contact.name || contact.jid} from contacts?`)) return;
      send({ type: 'remove_contact', to: contact.jid });
      delete state.roster[contact.jid];
      renderDMList();
    });
    li.appendChild(removeBtn);

    li.addEventListener('click', () => openDM(contact.jid));
    dmListEl.appendChild(li);
  }
}

function renderRoomList() {
  roomListEl.innerHTML = '';
  for (const room of Object.values(state.rooms)) {
    const key = convKey('room', room.jid);
    const li  = document.createElement('li');
    li.className = 'channel-item' + (isActiveConv('room', room.jid) ? ' active' : '');
    li.dataset.jid = room.jid;

    const prefix = document.createElement('span');
    prefix.className = 'channel-prefix';
    prefix.textContent = '#';

    const name = document.createElement('span');
    name.textContent = localPart(room.jid);

    li.appendChild(prefix);
    li.appendChild(name);

    const ub = state.unread[key];
    if (ub) {
      const badge = document.createElement('span');
      badge.className = 'unread-badge';
      badge.textContent = ub;
      li.appendChild(badge);
    }

    li.addEventListener('click', () => openRoom(room.jid));
    roomListEl.appendChild(li);
  }
}

function renderMessages() {
  messagesEl.innerHTML = '';
  if (!state.activeConv) return;
  const key = convKey(state.activeConv.type, state.activeConv.jid);
  const msgs = state.messages[key] || [];
  let lastFrom = null;

  for (const m of msgs) {
    appendMessageToDOM(m, lastFrom);
    lastFrom = senderKey(m);
  }
  scrollToBottom();
}

function appendMessageToDOM(m, lastFrom) {
  if (m.system) {
    const el = document.createElement('div');
    el.className = 'msg-system' + (m.warn ? ' msg-system-warn' : '');
    el.textContent = m.body;
    messagesEl.appendChild(el);
    return;
  }
  const sender   = senderKey(m);
  const isCompact = sender === lastFrom && lastFrom !== undefined;
  // When called from pushMessage, lastFrom is unknown; determine from DOM.
  const prevGroup = messagesEl.lastElementChild;
  const compact   = prevGroup && prevGroup.dataset.sender === sender;

  const group = document.createElement('div');
  group.className = 'msg-group' + (compact ? ' compact' : '');
  group.dataset.sender = sender;

  const avatarEl = document.createElement('div');
  avatarEl.className = 'group-avatar' + (compact ? ' empty' : '');
  if (!compact) {
    avatarEl.style.background = jidColor(m.from);
    avatarEl.textContent = displayName(m)[0].toUpperCase();
  }

  const body = document.createElement('div');
  body.className = 'msg-group-body';

  if (!compact) {
    const header = document.createElement('div');
    header.className = 'msg-group-header';

    const nameEl = document.createElement('span');
    nameEl.className = 'msg-sender';
    nameEl.textContent = displayName(m);

    const tsEl = document.createElement('span');
    tsEl.className = 'msg-timestamp';
    tsEl.textContent = formatTime(m.ts);

    header.appendChild(nameEl);
    header.appendChild(tsEl);
    body.appendChild(header);
  } else {
    const compactTime = document.createElement('span');
    compactTime.className = 'compact-time';
    compactTime.textContent = formatShortTime(m.ts);
    group.appendChild(compactTime);
  }

  const line = document.createElement('div');
  line.className = 'msg-line';
  line.textContent = m.body;
  body.appendChild(line);

  if (m.mentioned) group.classList.add('msg-mention');

  group.appendChild(avatarEl);
  group.appendChild(body);
  messagesEl.appendChild(group);
}

// ── Conversation management ────────────────────────────────────────────────
function openDM(jid) {
  state.activeConv = { type: 'dm', jid };
  state.unread[convKey('dm', jid)] = 0;
  updateTitle();
  chatTitleEl.textContent = state.roster[jid]?.name || jid;
  setComposeEnabled(true);
  renderMessages();
  renderDMList();
  renderRoomList();
  renderMembersPanel();
}

function openRoom(jid) {
  if (!state.rooms[jid]) {
    state.rooms[jid] = { jid, nick: '', occupants: {} };
    send({ type: 'join_room', room: jid });
  }
  state.activeConv = { type: 'room', jid };
  state.unread[convKey('room', jid)] = 0;
  updateTitle();
  chatTitleEl.textContent = '#' + localPart(jid);
  setComposeEnabled(true);
  renderMessages();
  renderDMList();
  renderRoomList();
  renderMembersPanel();
}

// ── Sending messages ───────────────────────────────────────────────────────
function sendMessage() {
  const body = composeEl.value.trim();
  if (!body || !state.activeConv) return;
  composeEl.value = '';
  autoResizeCompose();

  if (state.activeConv.type === 'dm') {
    send({ type: 'chat', to: state.activeConv.jid, body });
  } else {
    send({ type: 'room_message', room: state.activeConv.jid, body });
  }
}

// ── Helpers ────────────────────────────────────────────────────────────────
function bareJID(jid) {
  if (!jid) return '';
  const slash = jid.lastIndexOf('/');
  return slash > 0 ? jid.substring(0, slash) : jid;
}

function localPart(jid) {
  const at = jid.indexOf('@');
  return at > 0 ? jid.substring(0, at) : jid;
}

// domainOf returns the domain part of a JID (everything after '@', resource
// stripped). For a room JID room@conference.host this is the conference host.
function domainOf(jid) {
  const bare = bareJID(jid);
  const at = bare.indexOf('@');
  return at >= 0 ? bare.substring(at + 1) : bare;
}

function nickOf(fullJID) {
  const slash = fullJID.lastIndexOf('/');
  return slash >= 0 ? fullJID.substring(slash + 1) : fullJID;
}

function convKey(type, jid) { return `${type}:${jid}`; }

function isActiveConv(type, jid) {
  return state.activeConv?.type === type && state.activeConv?.jid === jid;
}

function bumpUnread(key) {
  state.unread[key] = (state.unread[key] || 0) + 1;
  updateTitle();
}

function senderKey(m) { return m.from || ''; }

function displayName(m) {
  if (m.nick) return m.nick;
  const jid = bareJID(m.from);
  return state.roster[jid]?.name || localPart(jid) || m.from || '?';
}

function showClass(show) {
  if (!show || show === 'available' || show === '') return 'online';
  if (show === 'unavailable' || show === 'offline') return 'offline';
  if (show === 'away' || show === 'xa') return 'idle';
  if (show === 'dnd') return 'dnd';
  return 'online';
}

function formatTime(iso) {
  if (!iso) return '';
  try {
    const d = new Date(iso);
    const now = new Date();
    const isToday = d.toDateString() === now.toDateString();
    if (isToday) {
      return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
    }
    return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  } catch { return iso; }
}

function formatShortTime(iso) {
  if (!iso) return '';
  try {
    return new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
  } catch { return ''; }
}

/**
 * Deterministic avatar colour from JID string.
 * Hash the string into a hue value in HSL space.
 */
function jidColor(jid) {
  let hash = 0;
  for (let i = 0; i < jid.length; i++) {
    hash = (hash << 5) - hash + jid.charCodeAt(i);
    hash |= 0;
  }
  const hue = Math.abs(hash) % 360;
  return `hsl(${hue}, 60%, 45%)`;
}

function renderMembersPanel() {
  const panel = $('members-panel');
  if (!panel) return;
  if (!state.activeConv || state.activeConv.type !== 'room') {
    panel.hidden = true;
    return;
  }
  panel.hidden = false;
  const room = state.rooms[state.activeConv.jid];
  const occs = room ? Object.values(room.occupants).sort((a, b) => a.nick.localeCompare(b.nick)) : [];
  const list = $('members-list');
  list.innerHTML = '';
  for (const occ of occs) {
    const li = document.createElement('li');
    li.className = 'member-item';
    const av = document.createElement('div');
    av.className = 'avatar';
    av.style.cssText = `width:24px;height:24px;font-size:11px;background:${jidColor(occ.nick)}`;
    av.textContent = occ.nick[0].toUpperCase();
    const name = document.createElement('span');
    name.textContent = occ.nick;
    li.appendChild(av);
    li.appendChild(name);
    list.appendChild(li);
  }
  $('members-count').textContent = occs.length ? `— ${occs.length}` : '';
}

function scrollToBottom() {
  const area = $('message-area');
  area.scrollTop = area.scrollHeight;
}

function setComposeEnabled(enabled) {
  composeEl.disabled       = !enabled;
  btnSend.disabled         = !enabled;
  btnLoadHistory.disabled  = !enabled;
}

function loadHistory() {
  if (!state.activeConv) return;
  const key  = convKey(state.activeConv.type, state.activeConv.jid);
  const msgs = state.messages[key] || [];
  // Use the timestamp of the oldest known message as the before-cursor.
  const before = msgs.length > 0 ? msgs[0].ts : undefined;
  const target = state.activeConv.type === 'room'
    ? state.activeConv.jid
    : state.activeConv.jid;
  send({ type: 'history', conversation: target, before, limit: 50 });
}

function autoResizeCompose() {
  composeEl.style.height = 'auto';
  composeEl.style.height = Math.min(composeEl.scrollHeight, 200) + 'px';
}

function showToast(message, isError = false) {
  const el = document.createElement('div');
  el.className = 'toast' + (isError ? ' error' : '');
  el.textContent = message;
  $('toast-container').appendChild(el);
  setTimeout(() => el.remove(), 4000);
}

// ── Modal helpers ──────────────────────────────────────────────────────────
function openModal(id)  { $(id).classList.add('is-active');    }
function closeModal(id) { $(id).classList.remove('is-active'); }

// ── Event bindings ─────────────────────────────────────────────────────────
btnSend.addEventListener('click', sendMessage);
btnLoadHistory.addEventListener('click', loadHistory);

composeEl.addEventListener('keydown', e => {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    sendMessage();
  }
});
composeEl.addEventListener('input', autoResizeCompose);

$('btn-join-room').addEventListener('click', () => {
  openModal('modal-join-room');
  browseRooms();
});
$('btn-new-dm').addEventListener('click',    () => openModal('modal-new-dm'));

function submitBrowseHost() {
  const host = $('input-browse-host').value.trim().toLowerCase();
  if (!host) return;
  // Expect a conference host (e.g. conference.other.org), not a JID.
  if (host.includes('@') || host.includes(' ') || !host.includes('.')) {
    showToast('Enter a conference host like conference.other.org', true);
    return;
  }
  addExtraMucHost(host);
  $('input-browse-host').value = '';
  browseRooms();
}
$('btn-add-browse-host').addEventListener('click', submitBrowseHost);
$('input-browse-host').addEventListener('keydown', e => {
  if (e.key === 'Enter') { e.preventDefault(); submitBrowseHost(); }
});

$('btn-do-join-room').addEventListener('click', () => {
  const jid = $('input-room-jid').value.trim();
  if (!jid) return;
  closeModal('modal-join-room');
  $('input-room-jid').value = '';
  openRoom(jid);
});

$('btn-do-new-dm').addEventListener('click', () => {
  const jid  = $('input-dm-jid').value.trim();
  const name = $('input-dm-name').value.trim();
  const subscribe = $('input-dm-subscribe').checked;
  if (!jid) return;
  closeModal('modal-new-dm');
  $('input-dm-jid').value  = '';
  $('input-dm-name').value = '';
  if (subscribe) {
    send({ type: 'add_contact', to: jid, name });
  }
  if (!state.roster[jid]) state.roster[jid] = { jid, name: name || localPart(jid), show: 'offline', status: '' };
  renderDMList();
  openDM(jid);
});

$('btn-accept-subscription').addEventListener('click', () => {
  const jid = $('subscribe-request-jid').textContent;
  send({ type: 'accept_subscription', to: jid });
  closeModal('modal-subscribe-request');
  if (!state.roster[jid]) state.roster[jid] = { jid, name: localPart(jid), show: 'offline', status: '' };
  renderDMList();
});

$('btn-decline-subscription').addEventListener('click', () => {
  const jid = $('subscribe-request-jid').textContent;
  send({ type: 'decline_subscription', to: jid });
  closeModal('modal-subscribe-request');
});

document.querySelectorAll('[data-close-modal]').forEach(btn => {
  btn.addEventListener('click', () => closeModal(btn.dataset.closeModal));
});

// Close modal when clicking background.
document.querySelectorAll('.modal-background').forEach(bg => {
  bg.addEventListener('click', () => {
    document.querySelectorAll('.modal.is-active').forEach(m => m.classList.remove('is-active'));
  });
});

// ── Sound settings panel ───────────────────────────────────────────────────
function initSoundSettings() {
  const panel      = $('sound-settings');
  const btnToggle  = $('btn-sound-settings');
  const chkEnabled = $('snd-enabled');
  const rngVolume  = $('snd-volume');
  const chkHidden  = $('snd-only-hidden');
  const btnTestDM  = $('snd-test-dm');
  const btnTestMen = $('snd-test-mention');

  if (!panel) return;

  chkEnabled.checked = sound.enabled;
  rngVolume.value    = Math.round(sound.volume * 100);
  chkHidden.checked  = sound.onlyHidden;

  btnToggle.addEventListener('click', () => openModal('modal-sound-settings'));

  chkEnabled.addEventListener('change', () => { sound.enabled    = chkEnabled.checked; soundSave(); });
  chkHidden.addEventListener('change',  () => { sound.onlyHidden = chkHidden.checked;  soundSave(); });
  rngVolume.addEventListener('input',   () => { sound.volume = parseInt(rngVolume.value, 10) / 100; soundSave(); });

  btnTestDM.addEventListener('click', () => {
    const was = sound.onlyHidden; sound.onlyHidden = false;
    playNotification('dm');
    sound.onlyHidden = was;
  });
  btnTestMen.addEventListener('click', () => {
    const was = sound.onlyHidden; sound.onlyHidden = false;
    playNotification('mention');
    sound.onlyHidden = was;
  });

  document.addEventListener('click', e => {
    if (!panel.hidden && !panel.contains(e.target) && e.target !== btnToggle) {
      panel.hidden = true;
    }
  });
}

// ── Boot ───────────────────────────────────────────────────────────────────
initSoundSettings();
probeSoundFiles(); // async; completes well before any notification could fire
connect();
