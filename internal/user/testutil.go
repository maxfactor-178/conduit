package user

import (
	"context"
	"log/slog"
	"os"
	"time"

	"conduit/internal/session"
	"conduit/internal/xmpp"
)

// NewTestUser creates a User for unit testing without going through Manager.
// Not intended for production use.
func NewTestUser(jid string, conn xmpp.XMPPConn) *User {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m := &Manager{
		users:       make(map[string]*User),
		idleTimeout: time.Hour,
		log:         log,
	}
	ctx, cancel := context.WithCancel(context.Background())
	u := &User{
		JID:          jid,
		XMPPConn:     conn,
		idleTimeout:  time.Hour,
		manager:      m,
		log:          log.With("jid", jid),
		sessions:     make(map[string]*session.Session),
		newSessionCh: make(chan struct{}, 1),
		ctx:          ctx,
		cancel:       cancel,
	}
	m.users[jid] = u
	return u
}
