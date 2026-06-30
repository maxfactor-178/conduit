package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"conduit/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestMapper(t *testing.T, devEnabled bool, devUser string) *auth.Mapper {
	t.Helper()
	m, err := auth.NewMapper("X-Remote-User", "", "", devEnabled, devUser, "testpass")
	require.NoError(t, err)
	return m
}

func TestMiddleware_HeaderAuth(t *testing.T) {
	m := newTestMapper(t, false, "")
	var capturedJID string

	handler := m.Middleware("example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedJID = auth.JIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Remote-User", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "alice@example.com", capturedJID)
}

func TestMiddleware_NoHeader_Returns401(t *testing.T) {
	m := newTestMapper(t, false, "")
	handler := m.Middleware("example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMiddleware_DevMode_QueryParam(t *testing.T) {
	m := newTestMapper(t, true, "")
	var capturedJID string

	handler := m.Middleware("example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedJID = auth.JIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/?user=bob", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "bob@example.com", capturedJID)
}

func TestMiddleware_DevMode_DefaultUser(t *testing.T) {
	m := newTestMapper(t, true, "charlie")
	var capturedJID string

	handler := m.Middleware("example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedJID = auth.JIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "charlie@example.com", capturedJID)
}

func TestResolveJID_NoMapping(t *testing.T) {
	m := newTestMapper(t, false, "")
	jid := m.ResolveJID("alice", "example.com")
	assert.Equal(t, "alice@example.com", jid)
}

func TestMiddleware_JIDInjection_Sanitized(t *testing.T) {
	m := newTestMapper(t, false, "")
	var capturedJID string

	handler := m.Middleware("example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedJID = auth.JIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// A malicious header with an @ would let someone impersonate another domain.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Remote-User", "alice@evil.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The @ should be stripped, resulting in "aliceevil.com@example.com"
	// rather than "alice@evil.com".
	assert.NotEqual(t, "alice@evil.com", capturedJID)
	assert.Contains(t, capturedJID, "@example.com")
}
