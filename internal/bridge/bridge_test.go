package bridge_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"conduit/internal/bridge"
	"conduit/internal/session"
	"conduit/internal/user"
	"conduit/pkg/protocol"
	"github.com/stretchr/testify/assert"
)

func newTestSession() *session.Session {
	return session.NewManager().Create("alice@example.com")
}

func newTestLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

func TestBridge_SendChat(t *testing.T) {
	mc := bridge.NewMockConn()
	u := user.NewTestUser("alice@example.com", mc)
	sess := newTestSession()
	b := bridge.New(u, sess, nil, newTestLog())

	b.HandleInbound(context.Background(), protocol.InboundMessage{
		Type: protocol.TypeChat,
		To:   "bob@example.com",
		Body: "Hello, Bob!",
	})

	assert.Equal(t, "bob@example.com", mc.SentTo)
	assert.Equal(t, "Hello, Bob!", mc.SentBody)
}

func TestBridge_SendRoomMessage(t *testing.T) {
	mc := bridge.NewMockConn()
	u := user.NewTestUser("alice@example.com", mc)
	sess := newTestSession()
	b := bridge.New(u, sess, nil, newTestLog())

	b.HandleInbound(context.Background(), protocol.InboundMessage{
		Type: protocol.TypeRoomMessage,
		Room: "general@conference.example.com",
		Body: "Hi everyone",
	})

	assert.Equal(t, "general@conference.example.com", mc.SentRoom)
	assert.Equal(t, "Hi everyone", mc.SentBody)
}

func TestBridge_JoinRoom(t *testing.T) {
	mc := bridge.NewMockConn()
	u := user.NewTestUser("alice@example.com", mc)
	sess := newTestSession()
	b := bridge.New(u, sess, nil, newTestLog())

	b.HandleInbound(context.Background(), protocol.InboundMessage{
		Type: protocol.TypeJoinRoom,
		Room: "dev@conference.example.com",
	})

	assert.Equal(t, "dev@conference.example.com", mc.JoinedRoom)
}

func TestBridge_SetPresence(t *testing.T) {
	mc := bridge.NewMockConn()
	u := user.NewTestUser("alice@example.com", mc)
	sess := newTestSession()
	b := bridge.New(u, sess, nil, newTestLog())

	b.HandleInbound(context.Background(), protocol.InboundMessage{
		Type: protocol.TypePresence,
		Show: "away",
	})

	assert.Equal(t, "away", mc.SentShow)
}

func TestBridge_LeaveRoom(t *testing.T) {
	mc := bridge.NewMockConn()
	u := user.NewTestUser("alice@example.com", mc)
	sess := newTestSession()
	b := bridge.New(u, sess, nil, newTestLog())

	b.HandleInbound(context.Background(), protocol.InboundMessage{
		Type: protocol.TypeLeaveRoom,
		Room: "dev@conference.example.com",
	})

	assert.Equal(t, "dev@conference.example.com", mc.LeftRoom)
}
