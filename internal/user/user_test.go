package user_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"conduit/internal/bridge"
	"conduit/internal/session"
	"conduit/internal/user"
	"conduit/internal/xmpp"
	"conduit/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

func TestBroadcast_DeliverToAllSessions(t *testing.T) {
	mc := bridge.NewMockConn()
	u := user.NewTestUser("alice@example.com", mc)

	s1 := session.NewManager().Create("alice@example.com")
	s2 := session.NewManager().Create("alice@example.com")
	u.AddSession(s1)
	u.AddSession(s2)

	msg := protocol.OutboundMessage{Type: protocol.TypeChat, Body: "hello"}
	u.Broadcast(msg)

	assert.Equal(t, msg, <-s1.Send)
	assert.Equal(t, msg, <-s2.Send)
}

func TestBroadcast_SlowConsumerDropped(t *testing.T) {
	mc := bridge.NewMockConn()
	u := user.NewTestUser("alice@example.com", mc)

	// A session with zero-capacity channel simulates a permanently full buffer.
	mgr := session.NewManager()
	s := mgr.Create("alice@example.com")
	// Drain the channel so it looks full from the outside by creating one with 0 cap.
	// We recreate it manually since session.Create gives capacity 32.
	// Instead just fill the buffer completely.
	for i := 0; i < cap(s.Send); i++ {
		s.Send <- protocol.OutboundMessage{}
	}
	u.AddSession(s)

	done := make(chan struct{})
	go func() {
		u.Broadcast(protocol.OutboundMessage{Type: protocol.TypeChat, Body: "dropped"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Broadcast blocked on full send buffer")
	}
}

func TestUser_Nick(t *testing.T) {
	mc := bridge.NewMockConn()
	u := user.NewTestUser("alice@example.com", mc)
	assert.Equal(t, "alice", u.Nick())
}

func TestUserManager_GetOrCreate_ReturnsSameUser(t *testing.T) {
	log := newTestLog()
	mgr := user.NewManager(
		func(_ context.Context, _ string) (xmpp.XMPPConn, error) {
			return bridge.NewMockConn(), nil
		},
		5*time.Second,
		log,
	)

	u1, err := mgr.GetOrCreate(context.Background(), "alice@example.com")
	require.NoError(t, err)
	require.NotNil(t, u1)

	u2, err := mgr.GetOrCreate(context.Background(), "alice@example.com")
	require.NoError(t, err)
	assert.Same(t, u1, u2)
	assert.Equal(t, 1, mgr.UserCount())
}

func TestUserManager_GetOrCreate_DifferentUsers(t *testing.T) {
	log := newTestLog()
	mgr := user.NewManager(
		func(_ context.Context, _ string) (xmpp.XMPPConn, error) {
			return bridge.NewMockConn(), nil
		},
		5*time.Second,
		log,
	)

	u1, err := mgr.GetOrCreate(context.Background(), "alice@example.com")
	require.NoError(t, err)

	u2, err := mgr.GetOrCreate(context.Background(), "bob@example.com")
	require.NoError(t, err)

	assert.NotSame(t, u1, u2)
	assert.Equal(t, 2, mgr.UserCount())
}
