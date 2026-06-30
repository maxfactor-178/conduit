package bridge

import (
	"context"
	"log/slog"
	"time"

	"conduit/internal/history"
	"conduit/internal/sanitize"
	"conduit/internal/session"
	"conduit/internal/user"
	"conduit/internal/xmpp"
	"conduit/pkg/protocol"
)

// Bridge translates between JSON protocol messages and XMPP operations for
// one browser session.
type Bridge struct {
	user    *user.User
	session *session.Session
	history *history.Service
	log     *slog.Logger
}

// New creates a Bridge for the given user/session pair.
func New(u *user.User, sess *session.Session, hist *history.Service, log *slog.Logger) *Bridge {
	return &Bridge{
		user:    u,
		session: sess,
		history: hist,
		log:     log.With("jid", u.JID, "session_id", sess.ID),
	}
}

// HandleInbound processes a JSON message from the browser.
func (b *Bridge) HandleInbound(ctx context.Context, msg protocol.InboundMessage) {
	b.log.Debug("inbound", "type", msg.Type, "to", msg.To, "room", msg.Room, "body_len", len(msg.Body))
	conn := b.user.XMPPConn
	switch msg.Type {
	case protocol.TypeChat:
		body := sanitize.Text(msg.Body)
		if err := conn.SendChat(ctx, msg.To, body); err != nil {
			b.sendError(ctx, "send failed: "+err.Error())
			return
		}
		// Broadcast the sent message to ALL sessions for this JID so that
		// other tabs or agents mapped to the same account see it immediately.
		b.user.Broadcast(protocol.OutboundMessage{
			Type:      protocol.TypeChat,
			From:      b.user.JID,
			To:        msg.To,
			Body:      body,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})

	case protocol.TypeRoomMessage:
		if err := conn.SendRoomMessage(ctx, msg.Room, sanitize.Text(msg.Body)); err != nil {
			b.sendError(ctx, "send room message failed: "+err.Error())
		}

	case protocol.TypeJoinRoom:
		nick := b.user.Nick()
		if err := conn.JoinRoom(ctx, msg.Room, nick); err != nil {
			b.sendError(ctx, "join room failed: "+err.Error())
			return
		}
		if b.history != nil {
			go b.history.LoadRoomHistory(ctx, b.user, conn, msg.Room)
		}

	case protocol.TypeLeaveRoom:
		if err := conn.LeaveRoom(ctx, msg.Room); err != nil {
			b.sendError(ctx, "leave room failed: "+err.Error())
		}

	case protocol.TypePresence:
		if err := conn.SetPresence(ctx, msg.Show, ""); err != nil {
			b.sendError(ctx, "set presence failed: "+err.Error())
		}

	case protocol.TypeAddContact:
		if err := conn.AddContact(ctx, msg.To, msg.Name); err != nil {
			b.sendError(ctx, "add contact failed: "+err.Error())
		}

	case protocol.TypeRemoveContact:
		if err := conn.RemoveContact(ctx, msg.To); err != nil {
			b.sendError(ctx, "remove contact failed: "+err.Error())
		}

	case protocol.TypeAcceptSubscription:
		if err := conn.AcceptSubscription(ctx, msg.To); err != nil {
			b.sendError(ctx, "accept subscription failed: "+err.Error())
		}

	case protocol.TypeDeclineSubscription:
		if err := conn.DeclineSubscription(ctx, msg.To); err != nil {
			b.sendError(ctx, "decline subscription failed: "+err.Error())
		}

	case protocol.TypeDiscoverRooms:
		extraHosts := msg.Hosts
		go func() {
			rooms, hosts, err := conn.DiscoverRooms(ctx, extraHosts)
			if err != nil {
				b.sendError(ctx, "discover rooms: "+err.Error())
				return
			}
			list := make([]protocol.RoomInfo, 0, len(rooms))
			for _, r := range rooms {
				list = append(list, protocol.RoomInfo{
					JID:               r.JID,
					Name:              r.Name,
					PasswordProtected: r.PasswordProtected,
				})
			}
			select {
			case b.session.Send <- protocol.OutboundMessage{Type: protocol.TypeRoomList, Hosts: hosts, Payload: list}:
			case <-ctx.Done():
			}
		}()

	case protocol.TypeHistory:
		if b.history != nil {
			go b.history.LoadConversationHistory(ctx, b.user, conn, msg.Conversation, msg.Before, msg.Limit)
		}

	default:
		b.log.Warn("unknown inbound message type", "type", msg.Type)
	}
}

func (b *Bridge) sendError(ctx context.Context, msg string) {
	select {
	case b.session.Send <- protocol.OutboundMessage{Type: protocol.TypeError, Error: msg}:
	case <-ctx.Done():
	}
}

// SendInitialState pushes roster and presence to a newly connected session.
func SendInitialState(ctx context.Context, u *user.User, sess *session.Session, log *slog.Logger) {
	conn := u.XMPPConn
	if conn == nil {
		return
	}
	items, err := conn.FetchRoster(ctx)
	if err != nil {
		log.Warn("initial roster fetch failed", "jid", u.JID, "err", err)
		return
	}

	rosterItems := make([]protocol.RosterItem, 0, len(items))
	for _, ri := range items {
		rosterItems = append(rosterItems, protocol.RosterItem{
			JID:          ri.JID,
			Name:         ri.Name,
			Subscription: ri.Subscription,
			Groups:       ri.Groups,
		})
	}

	select {
	case sess.Send <- protocol.OutboundMessage{
		Type:    protocol.TypeRoster,
		Payload: rosterItems,
	}:
	case <-ctx.Done():
	}
}

// MockXMPPConn is a test double for XMPPConn used in unit tests.
type MockXMPPConn struct {
	SentTo     string
	SentBody   string
	SentRoom   string
	SentShow   string
	JoinedRoom string
	LeftRoom   string

	events chan xmpp.Event
}

func NewMockConn() *MockXMPPConn {
	return &MockXMPPConn{events: make(chan xmpp.Event, 16)}
}

func (m *MockXMPPConn) SendChat(_ context.Context, to, body string) error {
	m.SentTo, m.SentBody = to, body
	return nil
}
func (m *MockXMPPConn) SendRoomMessage(_ context.Context, room, body string) error {
	m.SentRoom, m.SentBody = room, body
	return nil
}
func (m *MockXMPPConn) JoinRoom(_ context.Context, room, _ string) error {
	m.JoinedRoom = room
	return nil
}
func (m *MockXMPPConn) LeaveRoom(_ context.Context, room string) error {
	m.LeftRoom = room
	return nil
}
func (m *MockXMPPConn) SetPresence(_ context.Context, show, _ string) error {
	m.SentShow = show
	return nil
}
func (m *MockXMPPConn) FetchRoster(_ context.Context) ([]xmpp.RosterItem, error) {
	return nil, nil
}
func (m *MockXMPPConn) DiscoverRooms(_ context.Context, _ []string) ([]xmpp.RoomInfo, []string, error) {
	return nil, nil, nil
}
func (m *MockXMPPConn) AddContact(_ context.Context, _, _ string) error       { return nil }
func (m *MockXMPPConn) RemoveContact(_ context.Context, _ string) error       { return nil }
func (m *MockXMPPConn) AcceptSubscription(_ context.Context, _ string) error  { return nil }
func (m *MockXMPPConn) DeclineSubscription(_ context.Context, _ string) error { return nil }
func (m *MockXMPPConn) QueryMAM(_ context.Context, _ xmpp.MAMQuery) (<-chan xmpp.MAMResult, error) {
	ch := make(chan xmpp.MAMResult)
	close(ch)
	return ch, nil
}
func (m *MockXMPPConn) Events() <-chan xmpp.Event { return m.events }
func (m *MockXMPPConn) Done() <-chan struct{}     { return make(chan struct{}) }
func (m *MockXMPPConn) Close() error              { return nil }
