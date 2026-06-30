package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

type contextKey string

const jidContextKey contextKey = "jid"

// Mapper resolves HTTP usernames to XMPP JIDs and looks up passwords.
type Mapper struct {
	jidMap      map[string]string // username → bare JID
	credentials map[string]string // bare JID → password
	devEnabled  bool
	devUsername string
	devPassword string
	header      string
}

// NewMapper loads JID and credential mappings from disk.
func NewMapper(header, jidFile, credFile string, devEnabled bool, devUser, devPass string) (*Mapper, error) {
	m := &Mapper{
		header:      header,
		devEnabled:  devEnabled,
		devUsername: devUser,
		devPassword: devPass,
		jidMap:      make(map[string]string),
		credentials: make(map[string]string),
	}
	if jidFile != "" {
		if err := m.loadJIDMapping(jidFile); err != nil {
			return nil, fmt.Errorf("load jid mapping: %w", err)
		}
	}
	if credFile != "" {
		if err := m.loadCredentials(credFile); err != nil {
			return nil, fmt.Errorf("load credentials: %w", err)
		}
	}
	return m, nil
}

func (m *Mapper) loadJIDMapping(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &m.jidMap)
}

func (m *Mapper) loadCredentials(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &m.credentials)
}

// ResolveJID maps an HTTP username to a bare XMPP JID.
// Falls back to "username@domain" if no explicit mapping exists.
func (m *Mapper) ResolveJID(username, domain string) string {
	if jid, ok := m.jidMap[username]; ok {
		return jid
	}
	return username + "@" + domain
}

// PasswordFor returns the XMPP password for a bare JID.
func (m *Mapper) PasswordFor(bareJID string) (string, bool) {
	pw, ok := m.credentials[bareJID]
	return pw, ok
}

// Middleware returns an http.Handler middleware that reads the trusted
// username header, maps it to a JID, and stores it in the request context.
// In dev mode it also accepts a ?user= query parameter.
func (m *Mapper) Middleware(domain string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username := r.Header.Get(m.header)

		if username == "" && m.devEnabled {
			username = r.URL.Query().Get("user")
		}
		if username == "" && m.devEnabled && m.devUsername != "" {
			username = m.devUsername
		}
		if username == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		username = sanitizeUsername(username)
		jid := m.ResolveJID(username, domain)
		ctx := context.WithValue(r.Context(), jidContextKey, jid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// JIDFromContext returns the authenticated JID stored in the context.
// Returns empty string if not set.
func JIDFromContext(ctx context.Context) string {
	jid, _ := ctx.Value(jidContextKey).(string)
	return jid
}

// sanitizeUsername strips dangerous characters from the username to prevent
// JID injection when it is concatenated with "@domain".
func sanitizeUsername(u string) string {
	u = strings.TrimSpace(u)
	// Remove characters that would break JID parsing.
	for _, ch := range []string{"@", "/", " ", "\"", "&", "'", "/", ":", "<", ">"} {
		u = strings.ReplaceAll(u, ch, "")
	}
	return u
}
