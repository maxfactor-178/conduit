package protocol_test

import (
	"encoding/json"
	"testing"

	"conduit/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInboundMessage_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		msg  protocol.InboundMessage
	}{
		{
			name: "chat",
			msg:  protocol.InboundMessage{Type: protocol.TypeChat, To: "bob@example.com", Body: "Hello"},
		},
		{
			name: "join_room",
			msg:  protocol.InboundMessage{Type: protocol.TypeJoinRoom, Room: "general@conference.example.com"},
		},
		{
			name: "history with cursor",
			msg: protocol.InboundMessage{
				Type:         protocol.TypeHistory,
				Conversation: "alice@example.com",
				Before:       "stanza-id-xyz",
				Limit:        25,
			},
		},
		{
			name: "presence",
			msg:  protocol.InboundMessage{Type: protocol.TypePresence, Show: "away"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.msg)
			require.NoError(t, err)

			var got protocol.InboundMessage
			require.NoError(t, json.Unmarshal(data, &got))
			assert.Equal(t, tc.msg, got)
		})
	}
}

func TestOutboundMessage_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		msg  protocol.OutboundMessage
	}{
		{
			name: "chat",
			msg: protocol.OutboundMessage{
				Type:      protocol.TypeChat,
				From:      "alice@example.com",
				Body:      "Hi",
				Timestamp: "2025-01-15T10:00:00Z",
			},
		},
		{
			name: "roster",
			msg: protocol.OutboundMessage{
				Type: protocol.TypeRoster,
				Payload: []protocol.RosterItem{
					{JID: "bob@example.com", Name: "Bob", Subscription: "both"},
				},
			},
		},
		{
			name: "error",
			msg:  protocol.OutboundMessage{Type: protocol.TypeError, Error: "something went wrong"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.msg)
			require.NoError(t, err)

			var got protocol.OutboundMessage
			require.NoError(t, json.Unmarshal(data, &got))
			assert.Equal(t, tc.msg.Type, got.Type)
			assert.Equal(t, tc.msg.From, got.From)
			assert.Equal(t, tc.msg.Body, got.Body)
			assert.Equal(t, tc.msg.Error, got.Error)
		})
	}
}

func TestMessageTypeConstants(t *testing.T) {
	// Verify constants are non-empty strings to catch accidental blanking.
	types := []string{
		protocol.TypeChat, protocol.TypeRoomMessage, protocol.TypeJoinRoom,
		protocol.TypeLeaveRoom, protocol.TypePresence, protocol.TypeHistory,
		protocol.TypeRoster, protocol.TypeRosterUpdate, protocol.TypeOccupants,
		protocol.TypeHistoryBatch, protocol.TypeError, protocol.TypeConnected,
	}
	seen := make(map[string]bool)
	for _, tp := range types {
		assert.NotEmpty(t, tp)
		assert.False(t, seen[tp], "duplicate type constant: %s", tp)
		seen[tp] = true
	}
}
