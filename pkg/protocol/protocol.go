package protocol

const (
	TypeChat                = "chat"
	TypeRoomMessage         = "room_message"
	TypeJoinRoom            = "join_room"
	TypeLeaveRoom           = "leave_room"
	TypePresence            = "presence"
	TypeHistory             = "history"
	TypeRoster              = "roster"
	TypeRosterUpdate        = "roster_update"
	TypeOccupants           = "room_occupants"
	TypeHistoryBatch        = "history_batch"
	TypeError               = "error"
	TypeConnected           = "connected"
	TypeAddContact          = "add_contact"
	TypeRemoveContact       = "remove_contact"
	TypeAcceptSubscription  = "accept_subscription"
	TypeDeclineSubscription = "decline_subscription"
	TypeSubscribeRequest    = "subscribe_request"
	TypeDiscoverRooms       = "discover_rooms"
	TypeRoomList            = "room_list"
)

// InboundMessage is a message received from the browser over WebSocket.
type InboundMessage struct {
	Type         string `json:"type"`
	To           string `json:"to,omitempty"`
	Name         string `json:"name,omitempty"`         // display name for add_contact
	Room         string `json:"room,omitempty"`
	Body         string `json:"body,omitempty"`
	Show         string `json:"show,omitempty"`         // presence
	Conversation string `json:"conversation,omitempty"` // history target
	Before       string `json:"before,omitempty"`       // MAM pagination cursor
	Limit        int    `json:"limit,omitempty"`
}

// OutboundMessage is a message sent from the server to the browser over WebSocket.
type OutboundMessage struct {
	Type      string      `json:"type"`
	From      string      `json:"from,omitempty"`
	To        string      `json:"to,omitempty"`
	Room      string      `json:"room,omitempty"`
	Nick      string      `json:"nick,omitempty"`
	Body      string      `json:"body,omitempty"`
	Show      string      `json:"show,omitempty"`      // presence show value
	Timestamp string      `json:"timestamp,omitempty"` // RFC3339
	Brand     string      `json:"brand,omitempty"`     // UI/page title, sent on "connected"
	Payload   interface{} `json:"payload,omitempty"`
	Error     string      `json:"error,omitempty"`
}

// RosterItem represents a single entry in the user's contact list.
type RosterItem struct {
	JID          string `json:"jid"`
	Name         string `json:"name,omitempty"`
	Subscription string `json:"subscription,omitempty"`
	Groups       []string `json:"groups,omitempty"`
}

// RoomInfo describes a discoverable MUC room.
type RoomInfo struct {
	JID               string `json:"jid"`
	Name              string `json:"name,omitempty"`
	PasswordProtected bool   `json:"password_protected,omitempty"`
}

// Occupant represents a user present in a MUC room.
type Occupant struct {
	Nick string `json:"nick"`
	JID  string `json:"jid,omitempty"`
	Role string `json:"role,omitempty"`
}
