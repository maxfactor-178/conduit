package session

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"conduit/pkg/protocol"
)

const sendBuffer = 32

// Session represents one browser WebSocket connection for an authenticated user.
type Session struct {
	ID      string
	UserJID string
	Send    chan protocol.OutboundMessage

	ctx    context.Context
	cancel context.CancelFunc
}

// Context returns the session's context, cancelled when the session closes.
func (s *Session) Context() context.Context { return s.ctx }

// Close signals the session to shut down.
func (s *Session) Close() {
	s.cancel()
}

// Manager tracks all active sessions.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewManager creates an empty session manager.
func NewManager() *Manager {
	return &Manager{sessions: make(map[string]*Session)}
}

// Create allocates a new session for the given JID and registers it.
func (m *Manager) Create(userJID string) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:      uuid.NewString(),
		UserJID: userJID,
		Send:    make(chan protocol.OutboundMessage, sendBuffer),
		ctx:     ctx,
		cancel:  cancel,
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	return s
}

// Remove deregisters and closes the session with the given ID.
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if ok {
		s.cancel()
		close(s.Send)
	}
}

// Get retrieves a session by ID.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	return s, ok
}

// Count returns the total number of active sessions.
func (m *Manager) Count() int {
	m.mu.RLock()
	n := len(m.sessions)
	m.mu.RUnlock()
	return n
}
