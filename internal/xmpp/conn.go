package xmpp

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"conduit/config"
	"mellium.im/sasl"
	"mellium.im/xmlstream"
	mx "mellium.im/xmpp"
	mxhistory "mellium.im/xmpp/history"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/mux"
	"mellium.im/xmpp/roster"
	"mellium.im/xmpp/stanza"
)

const eventBuffer = 64

type conn struct {
	cfg      config.XMPPConfig
	userJID  string
	password string
	log      *slog.Logger

	mu      sync.RWMutex
	session *mx.Session

	events chan Event
	done   chan struct{}

	joinedRooms map[string]string
	roomsMu     sync.RWMutex

	histHandler *mxhistory.Handler

	mamMu      sync.Mutex
	mamPending map[string]chan MAMResult
}

// Dial creates a new XMPPConn. It performs the initial TCP+XMPP connection
// synchronously and returns once connected. The serving loop and reconnect
// logic run in background goroutines.
func Dial(ctx context.Context, cfg config.XMPPConfig, userJID, password string, log *slog.Logger) (XMPPConn, error) {
	c := &conn{
		cfg:         cfg,
		userJID:     userJID,
		password:    password,
		log:         log.With("jid", userJID),
		events:      make(chan Event, eventBuffer),
		done:        make(chan struct{}),
		joinedRooms: make(map[string]string),
		mamPending:  make(map[string]chan MAMResult),
	}
	c.histHandler = mxhistory.NewHandler(mux.MessageHandlerFunc(c.dispatchHistoryMessage))

	sess, m, err := c.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("initial xmpp connect: %w", err)
	}

	c.mu.Lock()
	c.session = sess
	c.mu.Unlock()

	go c.supervisor(ctx, sess, m)

	return c, nil
}

// connect performs TCP dial + XMPP negotiation and returns the session and mux.
// It does not start the serve loop.
func (c *conn) connect(ctx context.Context) (*mx.Session, *mux.ServeMux, error) {
	dialTimeout := c.cfg.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 10 * time.Second
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, dialTimeout)
	defer dialCancel()

	addr := fmt.Sprintf("%s:%d", c.cfg.Host, c.cfg.Port)
	netConn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("tcp dial %s: %w", addr, err)
	}

	userJIDParsed, err := jid.Parse(c.userJID + "/" + c.cfg.Resource)
	if err != nil {
		netConn.Close()
		return nil, nil, fmt.Errorf("parse jid: %w", err)
	}

	sess, err := mx.NewClientSession(ctx, userJIDParsed, netConn, c.buildFeatures()...)
	if err != nil {
		netConn.Close()
		return nil, nil, fmt.Errorf("negotiate session: %w", err)
	}

	m := mux.New(stanza.NSClient,
		roster.Handle(c.newRosterHandler()),
		mxhistory.Handle(c.histHandler),
		// Key each handler to its distinguishing child element. Mellium's mux
		// invokes a message handler once per matching child, so a wildcard
		// (xml.Name{}) fires once for EVERY child — and ejabberd adds extra
		// children (MAM <stanza-id>, origin-id, …) beyond <body>, which caused
		// each message to be processed multiple times.
		mux.MessageFunc(stanza.ChatMessage, xml.Name{Local: "body"}, mux.MessageHandlerFunc(c.handleChatMsg)),
		mux.MessageFunc(stanza.GroupChatMessage, xml.Name{Local: "body"}, mux.MessageHandlerFunc(c.handleGroupChatMsg)),
		mux.MessageFunc(stanza.ErrorMessage, xml.Name{Local: "error"}, mux.MessageHandlerFunc(c.handleErrorMsg)),
		mux.PresenceFunc(stanza.AvailablePresence, xml.Name{}, mux.PresenceHandlerFunc(c.handlePresence)),
		mux.PresenceFunc(stanza.UnavailablePresence, xml.Name{}, mux.PresenceHandlerFunc(c.handleUnavailablePresence)),
		mux.PresenceFunc(stanza.SubscribePresence, xml.Name{}, mux.PresenceHandlerFunc(c.handleSubscribePresence)),
	)

	return sess, m, nil
}

// supervisor starts a session's serve loop in a goroutine, runs post-connect
// setup, waits for the session to end, then reconnects indefinitely.
func (c *conn) supervisor(ctx context.Context, sess *mx.Session, m *mux.ServeMux) {
	defer close(c.done)

	backoff := time.Second
	maxBackoff := c.cfg.ReconnectMaxBackoff
	if maxBackoff == 0 {
		maxBackoff = 30 * time.Second
	}

	serveErr := c.serveSession(ctx, sess, m)

	for ctx.Err() == nil {
		c.log.Warn("xmpp disconnected", "err", serveErr, "retry_in", backoff)
		c.events <- Event{Type: EventDisconnected}

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}

		var err error
		sess, m, err = c.connect(ctx)
		if err != nil {
			c.log.Warn("xmpp reconnect failed", "err", err)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			serveErr = err
			continue
		}

		backoff = time.Second
		c.mu.Lock()
		c.session = sess
		c.mu.Unlock()

		serveErr = c.serveSession(ctx, sess, m)
	}
}

// serveSession starts sess.Serve in a goroutine, runs post-connect work
// concurrently, then blocks until the session ends and returns its error.
func (c *conn) serveSession(ctx context.Context, sess *mx.Session, m *mux.ServeMux) error {
	ch := make(chan error, 1)
	go func() { ch <- sess.Serve(m) }()

	c.postConnect(ctx)

	return <-ch
}

// postConnect sends the initial presence, emits EventConnected, and kicks off
// roster fetch and room rejoin goroutines. Must be called while Serve is
// already running so IQ responses can be dispatched.
func (c *conn) postConnect(ctx context.Context) {
	c.log.Info("xmpp session established")
	c.events <- Event{Type: EventConnected}

	if err := c.sendPresenceStanza(ctx, "", ""); err != nil {
		c.log.Warn("send initial presence failed", "err", err)
	}

	go func() {
		items, err := c.fetchRoster(ctx)
		if err != nil {
			c.log.Warn("roster fetch failed", "err", err)
			return
		}
		c.events <- Event{Type: EventRoster, Roster: items}
	}()

	go c.rejoinRooms(ctx)
}

func (c *conn) buildFeatures() []mx.StreamFeature {
	// tls_mode: "starttls" verifies the cert; anything else skips verification.
	// Always register StartTLS: omitting the handler causes
	// "features advertised out of order" when the server advertises it.
	tlsCfg := &tls.Config{
		InsecureSkipVerify: c.cfg.TLSMode != "starttls", //nolint:gosec
	}
	return []mx.StreamFeature{
		mx.StartTLS(tlsCfg),
		mx.SASL("", c.password, sasl.ScramSha1Plus, sasl.ScramSha1, sasl.Plain),
		mx.BindResource(),
	}
}

// --- message handlers ---

func (c *conn) handleChatMsg(msg stanza.Message, t xmlstream.TokenReadEncoder) error {
	body, ts := readMessageChildren(t)
	if body == "" {
		return nil
	}
	c.events <- Event{
		Type: EventChat,
		From: msg.From.String(),
		Body: body,
		Time: ts,
	}
	return nil
}

func (c *conn) handleGroupChatMsg(msg stanza.Message, t xmlstream.TokenReadEncoder) error {
	body, ts := readMessageChildren(t)
	if body == "" {
		return nil
	}
	from := msg.From.String()
	c.events <- Event{
		Type: EventRoomMessage,
		From: from,
		Room: bareJIDStr(from),
		Nick: resourceOf(from),
		Body: body,
		Time: ts,
	}
	return nil
}

// handleErrorMsg handles a bounced message (<message type="error">). The error
// comes "from" the JID we tried to reach (a contact for a DM, or the room for a
// MUC message). We surface a human-readable reason to the browser so the sender
// learns their message was not delivered.
func (c *conn) handleErrorMsg(msg stanza.Message, t xmlstream.TokenReadEncoder) error {
	from := msg.From.String()
	reason := readErrorReason(t)

	bare := bareJIDStr(from)
	c.roomsMu.RLock()
	_, isRoom := c.joinedRooms[bare]
	c.roomsMu.RUnlock()

	evt := Event{Type: EventMessageError, From: bare, Body: reason, Time: time.Now()}
	if isRoom {
		evt.Room = bare
	}
	c.events <- evt
	return nil
}

func (c *conn) dispatchHistoryMessage(msg stanza.Message, t xmlstream.TokenReadEncoder) error {
	body, ts := readMessageChildren(t)
	if body == "" {
		return nil
	}
	from := msg.From.String()
	result := MAMResult{
		From:      from,
		To:        msg.To.String(),
		Body:      body,
		Timestamp: ts,
		IsRoom:    msg.Type == stanza.GroupChatMessage,
		Room:      bareJIDStr(from),
	}
	c.mamMu.Lock()
	for _, ch := range c.mamPending {
		select {
		case ch <- result:
		default:
		}
	}
	c.mamMu.Unlock()
	return nil
}

// --- presence handlers ---

func (c *conn) handlePresence(p stanza.Presence, t xmlstream.TokenReadEncoder) error {
	from := p.From.String()
	show, status, occs := readPresenceChildren(t)
	if len(occs) > 0 {
		c.events <- Event{
			Type:      EventRoomPresence,
			From:      from,
			Room:      bareJIDStr(from),
			Nick:      resourceOf(from),
			Show:      "available",
			Occupants: occs,
		}
		return nil
	}
	c.events <- Event{Type: EventPresence, From: from, Show: show, Status: status}
	return nil
}

func (c *conn) handleUnavailablePresence(p stanza.Presence, t xmlstream.TokenReadEncoder) error {
	from := p.From.String()
	_, _, occs := readPresenceChildren(t)
	if len(occs) > 0 {
		c.events <- Event{
			Type:      EventRoomPresence,
			From:      from,
			Room:      bareJIDStr(from),
			Nick:      resourceOf(from),
			Show:      "unavailable",
			Occupants: occs,
		}
		return nil
	}
	c.events <- Event{Type: EventPresence, From: from, Show: "unavailable"}
	return nil
}

// --- XML child readers ---

func readMessageChildren(t xmlstream.TokenReadEncoder) (body string, ts time.Time) {
	ts = time.Now()
	d := xml.NewTokenDecoder(t)
	// Mellium replays the stanza start element into the reader before calling
	// the handler. Discard it so we only see child elements.
	if _, err := d.Token(); err != nil {
		return
	}
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch v := tok.(type) {
		case xml.EndElement:
			return
		case xml.StartElement:
			switch v.Name.Local {
			case "body":
				d.DecodeElement(&body, &v) //nolint:errcheck
			case "delay":
				for _, attr := range v.Attr {
					if attr.Name.Local == "stamp" {
						if parsed, err := time.Parse(time.RFC3339, attr.Value); err == nil {
							ts = parsed
						}
					}
				}
				d.Skip() //nolint:errcheck
			default:
				d.Skip() //nolint:errcheck
			}
		}
	}
}

// readErrorReason extracts a human-readable reason from a bounced message's
// <error> child: the optional <text>, or failing that the defined-condition
// element name (e.g. "remote-server-not-found" → "remote server not found").
func readErrorReason(t xmlstream.TokenReadEncoder) string {
	const fallback = "message could not be delivered"
	d := xml.NewTokenDecoder(t)
	// Discard the stanza start element replayed by mellium.
	if _, err := d.Token(); err != nil {
		return fallback
	}
	for {
		tok, err := d.Token()
		if err != nil {
			return fallback
		}
		switch v := tok.(type) {
		case xml.EndElement:
			return fallback
		case xml.StartElement:
			if v.Name.Local == "error" {
				return readErrorElement(d, fallback)
			}
			d.Skip() //nolint:errcheck
		}
	}
}

func readErrorElement(d *xml.Decoder, fallback string) string {
	var condition, text string
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch v := tok.(type) {
		case xml.EndElement:
			if v.Name.Local == "error" {
				return formatErrorReason(condition, text, fallback)
			}
		case xml.StartElement:
			if v.Name.Local == "text" {
				d.DecodeElement(&text, &v) //nolint:errcheck
			} else {
				if condition == "" {
					condition = v.Name.Local
				}
				d.Skip() //nolint:errcheck
			}
		}
	}
	return formatErrorReason(condition, text, fallback)
}

func formatErrorReason(condition, text, fallback string) string {
	// Prefer the standardized defined-condition (e.g. "remote-server-not-found"
	// → "remote server not found"): it is concise and consistent. Some servers
	// add a verbose/technical <text> (e.g. raw DNS errors) that reads worse, so
	// only fall back to it when no condition is present.
	if condition != "" {
		return strings.ReplaceAll(condition, "-", " ")
	}
	if s := strings.TrimSpace(text); s != "" {
		return s
	}
	return fallback
}

const mucUserNS = "http://jabber.org/protocol/muc#user"

func readPresenceChildren(t xmlstream.TokenReadEncoder) (show, status string, occs []Occupant) {
	d := xml.NewTokenDecoder(t)
	// Discard the stanza start element replayed by mellium.
	if _, err := d.Token(); err != nil {
		return
	}
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch v := tok.(type) {
		case xml.EndElement:
			return
		case xml.StartElement:
			switch {
			case v.Name.Local == "show":
				d.DecodeElement(&show, &v) //nolint:errcheck
			case v.Name.Local == "status":
				d.DecodeElement(&status, &v) //nolint:errcheck
			case v.Name.Local == "x" && v.Name.Space == mucUserNS:
				occs = readMUCUserItems(d, v)
			default:
				d.Skip() //nolint:errcheck
			}
		}
	}
}

type xmlMUCUser struct {
	Items []xmlMUCItem `xml:"item"`
}

type xmlMUCItem struct {
	Nick string `xml:"nick,attr"`
	JID  string `xml:"jid,attr"`
	Role string `xml:"role,attr"`
}

func readMUCUserItems(d *xml.Decoder, start xml.StartElement) []Occupant {
	var x xmlMUCUser
	if err := d.DecodeElement(&x, &start); err != nil {
		return nil
	}
	occs := make([]Occupant, 0, len(x.Items))
	for _, item := range x.Items {
		occs = append(occs, Occupant{Nick: item.Nick, JID: item.JID, Role: item.Role})
	}
	return occs
}

// --- XMPPConn implementation ---

func (c *conn) SendChat(ctx context.Context, to, body string) error {
	c.log.Debug("SendChat", "to", to)
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return fmt.Errorf("not connected")
	}
	toJID, err := jid.Parse(to)
	if err != nil {
		return fmt.Errorf("invalid to jid: %w", err)
	}
	msg := stanza.Message{To: toJID, Type: stanza.ChatMessage}
	return sess.Encode(ctx, struct {
		stanza.Message
		Body string `xml:"body"`
	}{msg, body})
}

func (c *conn) SendRoomMessage(ctx context.Context, room, body string) error {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return fmt.Errorf("not connected")
	}
	roomJID, err := jid.Parse(room)
	if err != nil {
		return fmt.Errorf("invalid room jid: %w", err)
	}
	msg := stanza.Message{To: roomJID, Type: stanza.GroupChatMessage}
	return sess.Encode(ctx, struct {
		stanza.Message
		Body string `xml:"body"`
	}{msg, body})
}

func (c *conn) SetPresence(ctx context.Context, show, status string) error {
	return c.sendPresenceStanza(ctx, show, status)
}

func (c *conn) sendPresenceStanza(ctx context.Context, show, status string) error {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return fmt.Errorf("not connected")
	}
	type presEl struct {
		XMLName xml.Name `xml:"presence"`
		Show    string   `xml:"show,omitempty"`
		Status  string   `xml:"status,omitempty"`
	}
	return sess.Encode(ctx, presEl{Show: show, Status: status})
}

func (c *conn) Events() <-chan Event  { return c.events }
func (c *conn) Done() <-chan struct{} { return c.done }

func (c *conn) Close() error {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return nil
	}
	return sess.Close()
}

// --- helpers ---

func bareJIDStr(fullJID string) string {
	if idx := strings.LastIndex(fullJID, "/"); idx > 0 {
		return fullJID[:idx]
	}
	return fullJID
}

func resourceOf(fullJID string) string {
	if idx := strings.LastIndex(fullJID, "/"); idx >= 0 {
		return fullJID[idx+1:]
	}
	return ""
}
