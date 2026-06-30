package xmpp

import (
	"context"
	"fmt"
	"sync"

	"conduit/internal/sanitize"
	mxdisco "mellium.im/xmpp/disco"
	"mellium.im/xmpp/disco/items"
	"mellium.im/xmpp/jid"
)

func (c *conn) DiscoverRooms(ctx context.Context) ([]RoomInfo, error) {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return nil, fmt.Errorf("not connected")
	}

	mucHost := c.cfg.MUCHost
	if mucHost == "" {
		return nil, fmt.Errorf("muc_host not configured")
	}
	mucJID, err := jid.Parse(mucHost)
	if err != nil {
		return nil, fmt.Errorf("parse muc host %q: %w", mucHost, err)
	}

	iter := mxdisco.FetchItems(ctx, items.Item{JID: mucJID}, sess)

	var discovered []items.Item
	for iter.Next() {
		discovered = append(discovered, iter.Item())
	}
	iterErr := iter.Err()
	// Close the IQ response stream now rather than deferring to function
	// exit: mellium pauses the session's read loop until the response is
	// closed, and the GetInfo calls below need that loop running to ever
	// receive their replies.
	if err := iter.Close(); err != nil && iterErr == nil {
		iterErr = err
	}
	if iterErr != nil {
		return nil, fmt.Errorf("disco#items: %w", iterErr)
	}

	// Fetch disco#info for each room concurrently to detect password protection.
	const maxConcurrent = 8
	sem := make(chan struct{}, maxConcurrent)
	var mu sync.Mutex
	rooms := make([]RoomInfo, 0, len(discovered))
	var wg sync.WaitGroup

	for _, it := range discovered {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			ri := RoomInfo{
				JID:  it.JID.Bare().String(),
				Name: sanitize.Text(it.Name),
			}
			if ri.Name == "" {
				ri.Name = sanitize.Text(it.JID.Localpart())
			}

			infoCtx, cancel := context.WithTimeout(ctx, c.cfg.DialTimeout)
			defer cancel()
			info, err := mxdisco.GetInfo(infoCtx, "", it.JID, sess)
			if err == nil {
				for _, f := range info.Features {
					if f.Var == "muc_passwordprotected" {
						ri.PasswordProtected = true
					}
				}
				// Prefer the identity name from the conference category.
				for _, ident := range info.Identity {
					if ident.Category == "conference" && ident.Name != "" {
						ri.Name = sanitize.Text(ident.Name)
						break
					}
				}
			}

			mu.Lock()
			rooms = append(rooms, ri)
			mu.Unlock()
		}()
	}
	wg.Wait()

	return rooms, nil
}
