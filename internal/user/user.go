package user

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"conduit/internal/sanitize"
	"conduit/internal/session"
	"conduit/internal/xmpp"
	"conduit/pkg/protocol"
)

// User represents one authenticated XMPP user.
// All browser sessions for the same JID share a single XMPPConn.
type User struct {
	JID         string
	XMPPConn    xmpp.XMPPConn
	idleTimeout time.Duration
	manager     *Manager
	log         *slog.Logger

	mu           sync.RWMutex
	sessions     map[string]*session.Session
	newSessionCh chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
}

// Nick derives a display nickname from the user's bare JID (the local part).
func (u *User) Nick() string {
	if idx := strings.Index(u.JID, "@"); idx > 0 {
		return u.JID[:idx]
	}
	return u.JID
}

// AddSession registers a new browser session with this user and cancels any
// pending idle-shutdown timer.
func (u *User) AddSession(s *session.Session) {
	u.mu.Lock()
	u.sessions[s.ID] = s
	u.mu.Unlock()
	// Signal that a new session arrived so the idle timer can be cancelled.
	select {
	case u.newSessionCh <- struct{}{}:
	default:
	}
}

// RemoveSession deregisters a browser session. If no sessions remain, an idle
// shutdown timer is started.
func (u *User) RemoveSession(id string) {
	u.mu.Lock()
	delete(u.sessions, id)
	count := len(u.sessions)
	u.mu.Unlock()

	if count == 0 {
		go u.scheduleIdleShutdown()
	}
}

func (u *User) scheduleIdleShutdown() {
	select {
	case <-time.After(u.idleTimeout):
		u.log.Info("idle timeout, closing xmpp connection")
		u.manager.Remove(u.JID)
		u.cancel()
	case <-u.ctx.Done():
	case <-u.newSessionCh:
	}
}

// Broadcast delivers msg to every active browser session for this user.
// Slow consumers are silently dropped to protect the event loop.
func (u *User) Broadcast(msg protocol.OutboundMessage) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	for _, s := range u.sessions {
		select {
		case s.Send <- msg:
		default:
			u.log.Warn("session send buffer full, dropping message", "session_id", s.ID)
		}
	}
}

// SessionCount returns the number of active browser sessions.
func (u *User) SessionCount() int {
	u.mu.RLock()
	n := len(u.sessions)
	u.mu.RUnlock()
	return n
}

// Done returns a channel that is closed when the user's context is cancelled.
func (u *User) Done() <-chan struct{} {
	return u.ctx.Done()
}

// Manager owns all active User instances and their XMPP connections.
type Manager struct {
	mu    sync.RWMutex
	users map[string]*User // keyed by bare JID

	newXMPP     func(ctx context.Context, jid string) (xmpp.XMPPConn, error)
	idleTimeout time.Duration
	log         *slog.Logger
}

// NewManager creates a user manager.
// newXMPP is called to create an XMPPConn when a new user first connects.
func NewManager(
	newXMPP func(ctx context.Context, jid string) (xmpp.XMPPConn, error),
	idleTimeout time.Duration,
	log *slog.Logger,
) *Manager {
	return &Manager{
		users:       make(map[string]*User),
		newXMPP:     newXMPP,
		idleTimeout: idleTimeout,
		log:         log,
	}
}

// GetOrCreate returns the User for jid, creating one (and an XMPP connection)
// if this is the first session for that JID.
//
// The XMPP connection is established WITHOUT holding the global lock so that
// concurrent users can connect in parallel.
func (m *Manager) GetOrCreate(ctx context.Context, jid string) (*User, error) {
	// Fast path: user already exists.
	m.mu.RLock()
	u, ok := m.users[jid]
	m.mu.RUnlock()
	if ok {
		return u, nil
	}

	// Connect outside the lock — XMPP negotiation is slow and must not block
	// other users from being looked up or created concurrently.
	conn, err := m.newXMPP(ctx, jid)
	if err != nil {
		return nil, err
	}

	// Acquire write lock to register the user.
	m.mu.Lock()
	defer m.mu.Unlock()

	// Another goroutine may have created this user while we were connecting.
	if u, ok = m.users[jid]; ok {
		conn.Close()
		return u, nil
	}

	uCtx, uCancel := context.WithCancel(context.Background())
	u = &User{
		JID:          jid,
		XMPPConn:     conn,
		idleTimeout:  m.idleTimeout,
		manager:      m,
		log:          m.log.With("jid", jid),
		sessions:     make(map[string]*session.Session),
		newSessionCh: make(chan struct{}, 1),
		ctx:          uCtx,
		cancel:       uCancel,
	}
	m.users[jid] = u

	go m.eventLoop(u)
	return u, nil
}

// Remove tears down the user's XMPP connection and removes it from the map.
func (m *Manager) Remove(jid string) {
	m.mu.Lock()
	u, ok := m.users[jid]
	delete(m.users, jid)
	m.mu.Unlock()

	if ok {
		u.cancel()
		u.XMPPConn.Close()
	}
}

// UserCount returns the number of currently managed users.
func (m *Manager) UserCount() int {
	m.mu.RLock()
	n := len(m.users)
	m.mu.RUnlock()
	return n
}

// eventLoop reads events from the user's XMPPConn and broadcasts them.
func (m *Manager) eventLoop(u *User) {
	events := u.XMPPConn.Events()
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				return
			}
			msg := eventToOutbound(evt)
			if msg == nil {
				continue
			}
			u.Broadcast(*msg)
		case <-u.ctx.Done():
			return
		}
	}
}

func eventToOutbound(evt xmpp.Event) *protocol.OutboundMessage {
	ts := evt.Time.UTC().Format(time.RFC3339)
	switch evt.Type {
	case xmpp.EventConnected:
		return &protocol.OutboundMessage{Type: protocol.TypeConnected}
	case xmpp.EventSubscribeRequest:
		return &protocol.OutboundMessage{Type: protocol.TypeSubscribeRequest, From: evt.From}
	case xmpp.EventChat:
		return &protocol.OutboundMessage{
			Type:      protocol.TypeChat,
			From:      evt.From,
			Body:      sanitize.Text(evt.Body),
			Timestamp: ts,
		}
	case xmpp.EventRoomMessage:
		return &protocol.OutboundMessage{
			Type:      protocol.TypeRoomMessage,
			Room:      evt.Room,
			From:      evt.From,
			Nick:      sanitize.Text(evt.Nick),
			Body:      sanitize.Text(evt.Body),
			Timestamp: ts,
		}
	case xmpp.EventPresence:
		return &protocol.OutboundMessage{
			Type: protocol.TypePresence,
			From: evt.From,
			Body: evt.Show,
		}
	case xmpp.EventRoster:
		items := make([]protocol.RosterItem, 0, len(evt.Roster))
		for _, ri := range evt.Roster {
			items = append(items, protocol.RosterItem{
				JID:          ri.JID,
				Name:         sanitize.Text(ri.Name),
				Subscription: ri.Subscription,
				Groups:       ri.Groups,
			})
		}
		return &protocol.OutboundMessage{
			Type:    protocol.TypeRoster,
			Payload: items,
		}
	case xmpp.EventRoomPresence:
		occs := make([]protocol.Occupant, 0, len(evt.Occupants))
		for _, o := range evt.Occupants {
			occs = append(occs, protocol.Occupant{Nick: o.Nick, JID: o.JID, Role: o.Role})
		}
		return &protocol.OutboundMessage{
			Type:    protocol.TypeOccupants,
			From:    evt.From, // full JID: room@conf/nick
			Room:    evt.Room,
			Show:    evt.Show, // "available" or "unavailable"
			Payload: occs,
		}
	}
	return nil
}
