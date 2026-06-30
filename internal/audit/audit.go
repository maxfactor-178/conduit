// Package audit emits structured, machine-parseable audit records for
// security-relevant events (authentication and session lifecycle). Records are
// written as JSON to a dedicated audit log so they can be shipped to a SIEM or
// reviewed independently of the noisy application log.
package audit

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// Logger writes audit records. A nil *Logger is valid and silently drops every
// record, so callers never need to nil-check before logging.
type Logger struct {
	l *slog.Logger
}

// New wraps an slog.Logger as an audit Logger. A nil slog.Logger yields a nil
// audit Logger (auditing disabled).
func New(l *slog.Logger) *Logger {
	if l == nil {
		return nil
	}
	return &Logger{l: l}
}

func (a *Logger) emit(event string, attrs ...any) {
	if a == nil || a.l == nil {
		return
	}
	a.l.Info(event, attrs...)
}

// SessionOpen records a browser session connecting and authenticating as jid.
func (a *Logger) SessionOpen(jid, remote, sessionID string) {
	a.emit("session_open", "jid", jid, "remote", remote, "session", sessionID)
}

// SessionClose records a browser session disconnecting.
func (a *Logger) SessionClose(jid, remote, sessionID string) {
	a.emit("session_close", "jid", jid, "remote", remote, "session", sessionID)
}

// AuthRejected records a request that failed authentication before a session
// was established.
func (a *Logger) AuthRejected(remote, reason string) {
	a.emit("auth_rejected", "remote", remote, "reason", reason)
}

// ClientIP extracts the best-effort client IP for an audit record. When Conduit
// runs behind a trusted reverse proxy the real client is in X-Forwarded-For;
// otherwise the direct connection address is used.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For is a comma-separated list; the first entry is the
		// originating client.
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
