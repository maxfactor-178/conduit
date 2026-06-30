package xmpp

import (
	"context"
	"time"
)

// EventType identifies the kind of XMPP event dispatched to the application layer.
type EventType int

const (
	EventChat             EventType = iota // one-to-one chat message
	EventRoomMessage                       // MUC groupchat message
	EventPresence                          // contact presence change
	EventRoomPresence                      // MUC occupant presence change
	EventRoster                            // full roster push
	EventRosterUpdate                      // single roster item delta
	EventConnected                         // session fully negotiated
	EventDisconnected                      // session lost
	EventSubscribeRequest                  // someone wants to subscribe to our presence
)

// RosterItem is a single entry in the XMPP roster.
type RosterItem struct {
	JID          string
	Name         string
	Subscription string
	Groups       []string
}

// Occupant is a user currently present in a MUC room.
type Occupant struct {
	Nick string
	JID  string // real JID if disclosed
	Role string
}

// Event is an inbound XMPP event dispatched to the bridge.
type Event struct {
	Type      EventType
	From      string
	To        string
	Room      string
	Nick      string
	Body      string
	Show      string // presence show value
	Status    string // presence status text
	Time      time.Time
	Roster    []RosterItem
	Occupants []Occupant
}

// RoomInfo describes a discoverable MUC room.
type RoomInfo struct {
	JID               string
	Name              string
	PasswordProtected bool
}

// MAMQuery specifies parameters for a Message Archive Management query.
type MAMQuery struct {
	With   string // bare JID or room JID to filter by
	Before string // result ID for backwards pagination
	After  string // result ID for forward pagination
	Max    int    // maximum results to return
}

// MAMResult is a single message returned by a MAM query.
type MAMResult struct {
	ID        string
	From      string
	To        string
	Body      string
	Timestamp time.Time
	IsRoom    bool
	Room      string
}

// XMPPConn is the only type the rest of the application knows about.
// All mellium imports live exclusively inside internal/xmpp.
type XMPPConn interface {
	// Messaging
	SendChat(ctx context.Context, to, body string) error
	SendRoomMessage(ctx context.Context, room, body string) error

	// MUC
	JoinRoom(ctx context.Context, room, nick string) error
	LeaveRoom(ctx context.Context, room string) error
	DiscoverRooms(ctx context.Context) ([]RoomInfo, error)

	// Presence
	SetPresence(ctx context.Context, show, status string) error

	// Roster
	FetchRoster(ctx context.Context) ([]RosterItem, error)
	AddContact(ctx context.Context, jid, name string) error
	RemoveContact(ctx context.Context, jid string) error
	AcceptSubscription(ctx context.Context, jid string) error
	DeclineSubscription(ctx context.Context, jid string) error

	// History
	QueryMAM(ctx context.Context, q MAMQuery) (<-chan MAMResult, error)

	// Events
	Events() <-chan Event

	// Lifecycle
	Close() error
	Done() <-chan struct{}
}
