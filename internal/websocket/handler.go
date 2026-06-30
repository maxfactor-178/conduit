package websocket

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	gorillaws "github.com/gorilla/websocket"

	"conduit/internal/audit"
	"conduit/internal/auth"
	"conduit/internal/bridge"
	"conduit/internal/history"
	"conduit/internal/session"
	"conduit/internal/user"
	"conduit/pkg/protocol"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 64 * 1024
)

// Handler upgrades HTTP connections to WebSocket and manages the read/write pumps.
type Handler struct {
	upgrader gorillaws.Upgrader
	users    *user.Manager
	sessions *session.Manager
	history  *history.Service
	brand    string
	audit    *audit.Logger
	log      *slog.Logger
}

// NewHandler creates a WebSocket handler.
func NewHandler(
	users *user.Manager,
	sessions *session.Manager,
	hist *history.Service,
	allowedOrigins []string,
	brand string,
	auditLog *audit.Logger,
	log *slog.Logger,
) *Handler {
	originCheck := func(r *http.Request) bool {
		if len(allowedOrigins) == 0 {
			return true
		}
		origin := r.Header.Get("Origin")
		for _, o := range allowedOrigins {
			if o == "*" || o == origin {
				return true
			}
		}
		return false
	}

	return &Handler{
		upgrader: gorillaws.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     originCheck,
		},
		users:    users,
		sessions: sessions,
		history:  hist,
		brand:    brand,
		audit:    auditLog,
		log:      log,
	}
}

// ServeHTTP handles a single WebSocket connection.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	jid := auth.JIDFromContext(r.Context())
	if jid == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	u, err := h.users.GetOrCreate(r.Context(), jid)
	if err != nil {
		h.log.Error("xmpp unavailable", "jid", jid, "err", err)
		http.Error(w, "xmpp unavailable", http.StatusServiceUnavailable)
		return
	}

	wsConn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Warn("websocket upgrade failed", "err", err)
		return
	}

	sess := h.sessions.Create(jid)
	u.AddSession(sess)
	remote := audit.ClientIP(r)
	h.audit.SessionOpen(jid, remote, sess.ID)
	defer func() {
		u.RemoveSession(sess.ID)
		h.sessions.Remove(sess.ID)
		wsConn.Close()
		h.audit.SessionClose(jid, remote, sess.ID)
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Tell the client their own JID immediately so the UI can label sent messages.
	select {
	case sess.Send <- protocol.OutboundMessage{Type: protocol.TypeConnected, From: jid, Brand: h.brand}:
	default:
	}

	go bridge.SendInitialState(ctx, u, sess, h.log)
	go h.writePump(ctx, wsConn, sess)
	h.readPump(ctx, wsConn, u, sess)
}

// readPump decodes inbound JSON frames and dispatches them via the bridge.
func (h *Handler) readPump(ctx context.Context, wsConn *gorillaws.Conn, u *user.User, sess *session.Session) {
	wsConn.SetReadLimit(maxMessageSize)
	wsConn.SetReadDeadline(time.Now().Add(pongWait))
	wsConn.SetPongHandler(func(string) error {
		wsConn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	b := bridge.New(u, sess, h.history, h.log)
	for {
		var msg protocol.InboundMessage
		if err := wsConn.ReadJSON(&msg); err != nil {
			if gorillaws.IsUnexpectedCloseError(err, gorillaws.CloseGoingAway, gorillaws.CloseAbnormalClosure) {
				h.log.Warn("websocket read error", "jid", u.JID, "err", err)
			}
			return
		}
		b.HandleInbound(ctx, msg)
	}
}

// writePump drains the session's send channel into the WebSocket connection.
func (h *Handler) writePump(ctx context.Context, wsConn *gorillaws.Conn, sess *session.Session) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-sess.Send:
			wsConn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				wsConn.WriteMessage(gorillaws.CloseMessage, []byte{})
				return
			}
			if err := wsConn.WriteJSON(msg); err != nil {
				h.log.Warn("websocket write error", "session_id", sess.ID, "err", err)
				return
			}
		case <-ticker.C:
			wsConn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := wsConn.WriteMessage(gorillaws.PingMessage, nil); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
