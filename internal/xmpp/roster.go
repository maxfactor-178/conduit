package xmpp

import (
	"context"
	"fmt"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/roster"
	"mellium.im/xmpp/stanza"
)

func (c *conn) FetchRoster(ctx context.Context) ([]RosterItem, error) {
	return c.fetchRoster(ctx)
}

func (c *conn) fetchRoster(ctx context.Context) ([]RosterItem, error) {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return nil, fmt.Errorf("not connected")
	}

	iter := roster.Fetch(ctx, sess)
	defer iter.Close()

	var items []RosterItem
	for iter.Next() {
		item := iter.Item()
		items = append(items, RosterItem{
			JID:          item.JID.String(),
			Name:         item.Name,
			Subscription: item.Subscription,
			Groups:       item.Group,
		})
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("roster iter: %w", err)
	}
	return items, nil
}

// AddContact adds a JID to the roster and sends a subscription request.
func (c *conn) AddContact(ctx context.Context, bareJID, name string) error {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return fmt.Errorf("not connected")
	}

	j, err := jid.Parse(bareJID)
	if err != nil {
		return fmt.Errorf("invalid jid: %w", err)
	}

	// Add to roster.
	item := roster.Item{JID: j, Name: name}
	if err := roster.Set(ctx, sess, item); err != nil {
		return fmt.Errorf("roster set: %w", err)
	}

	// Send subscription request.
	return sess.Encode(ctx, stanza.Presence{To: j.Bare(), Type: stanza.SubscribePresence})
}

// RemoveContact removes a JID from the roster (also cancels any subscription).
func (c *conn) RemoveContact(ctx context.Context, bareJID string) error {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return fmt.Errorf("not connected")
	}

	j, err := jid.Parse(bareJID)
	if err != nil {
		return fmt.Errorf("invalid jid: %w", err)
	}

	item := roster.Item{JID: j, Subscription: "remove"}
	return roster.Set(ctx, sess, item)
}

// AcceptSubscription approves an inbound subscription request and requests
// mutual subscription back.
func (c *conn) AcceptSubscription(ctx context.Context, bareJID string) error {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return fmt.Errorf("not connected")
	}

	j, err := jid.Parse(bareJID)
	if err != nil {
		return fmt.Errorf("invalid jid: %w", err)
	}

	// Approve their subscription to us.
	if err := sess.Encode(ctx, stanza.Presence{To: j.Bare(), Type: stanza.SubscribedPresence}); err != nil {
		return err
	}
	// Request subscription to them (mutual).
	return sess.Encode(ctx, stanza.Presence{To: j.Bare(), Type: stanza.SubscribePresence})
}

// DeclineSubscription rejects an inbound subscription request.
func (c *conn) DeclineSubscription(ctx context.Context, bareJID string) error {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return fmt.Errorf("not connected")
	}

	j, err := jid.Parse(bareJID)
	if err != nil {
		return fmt.Errorf("invalid jid: %w", err)
	}

	return sess.Encode(ctx, stanza.Presence{To: j.Bare(), Type: stanza.UnsubscribedPresence})
}

func (c *conn) newRosterHandler() roster.Handler {
	return roster.Handler{
		Push: func(ver string, item roster.Item) error {
			c.log.Debug("roster push", "jid", item.JID, "subscription", item.Subscription)
			c.events <- Event{
				Type: EventRosterUpdate,
				Roster: []RosterItem{{
					JID:          item.JID.String(),
					Name:         item.Name,
					Subscription: item.Subscription,
					Groups:       item.Group,
				}},
			}
			return nil
		},
	}
}

// handleSubscribePresence handles incoming <presence type='subscribe'> stanzas
// and emits EventSubscribeRequest so the user can accept or decline.
func (c *conn) handleSubscribePresence(p stanza.Presence, t xmlstream.TokenReadEncoder) error {
	c.events <- Event{Type: EventSubscribeRequest, From: p.From.Bare().String()}
	return nil
}
