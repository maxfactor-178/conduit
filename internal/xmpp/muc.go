package xmpp

import (
	"context"
	"encoding/xml"
	"fmt"
)

const mucNS = "http://jabber.org/protocol/muc"

type mucPresence struct {
	XMLName xml.Name `xml:"presence"`
	To      string   `xml:"to,attr"`
	X       mucX     `xml:"http://jabber.org/protocol/muc x"`
}

type mucX struct{}

type unavailPresence struct {
	XMLName xml.Name `xml:"presence"`
	To      string   `xml:"to,attr"`
	Type    string   `xml:"type,attr"`
}

func (c *conn) JoinRoom(ctx context.Context, room, nick string) error {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return fmt.Errorf("not connected")
	}

	roomNickJID := room + "/" + nick
	if err := sess.Encode(ctx, mucPresence{To: roomNickJID}); err != nil {
		return fmt.Errorf("join room %s: %w", room, err)
	}

	c.roomsMu.Lock()
	c.joinedRooms[room] = nick
	c.roomsMu.Unlock()
	return nil
}

func (c *conn) LeaveRoom(ctx context.Context, room string) error {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return fmt.Errorf("not connected")
	}

	c.roomsMu.RLock()
	nick := c.joinedRooms[room]
	c.roomsMu.RUnlock()
	if nick == "" {
		return nil
	}

	roomNickJID := room + "/" + nick
	if err := sess.Encode(ctx, unavailPresence{To: roomNickJID, Type: "unavailable"}); err != nil {
		return fmt.Errorf("leave room %s: %w", room, err)
	}

	c.roomsMu.Lock()
	delete(c.joinedRooms, room)
	c.roomsMu.Unlock()
	return nil
}

func (c *conn) rejoinRooms(ctx context.Context) {
	c.roomsMu.RLock()
	rooms := make(map[string]string, len(c.joinedRooms))
	for room, nick := range c.joinedRooms {
		rooms[room] = nick
	}
	c.roomsMu.RUnlock()

	for room, nick := range rooms {
		if err := c.JoinRoom(ctx, room, nick); err != nil {
			c.log.Warn("rejoin room failed", "room", room, "err", err)
		}
	}
}

// mucNS is referenced here to satisfy the compiler if not used elsewhere.
var _ = mucNS
