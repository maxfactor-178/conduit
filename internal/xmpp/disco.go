package xmpp

import (
	"context"
	"fmt"
	"sync"

	"conduit/internal/sanitize"
	mx "mellium.im/xmpp"
	mxdisco "mellium.im/xmpp/disco"
	"mellium.im/xmpp/disco/items"
	"mellium.im/xmpp/jid"
)

// DiscoverRooms lists rooms across the configured conference hosts plus any
// extraHosts the caller supplies (e.g. a remote server's conference component).
// Each host is queried independently; a host that fails (unreachable, not a
// conference service, etc.) is logged and skipped so one bad server does not
// break discovery of the others. It returns the aggregated rooms and the full
// deduplicated list of hosts that were queried.
func (c *conn) DiscoverRooms(ctx context.Context, extraHosts []string) ([]RoomInfo, []string, error) {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return nil, nil, fmt.Errorf("not connected")
	}

	hosts := dedupeHosts(c.cfg.MUCHost, c.cfg.MUCHosts, extraHosts)
	if len(hosts) == 0 {
		return nil, nil, fmt.Errorf("no conference hosts configured")
	}

	var rooms []RoomInfo
	for _, host := range hosts {
		hostRooms, err := c.discoverRoomsOnHost(ctx, sess, host)
		if err != nil {
			c.log.Warn("muc discovery failed for host", "host", host, "err", err)
			continue
		}
		rooms = append(rooms, hostRooms...)
	}
	return rooms, hosts, nil
}

// discoverRoomsOnHost lists the rooms advertised by a single conference host and
// enriches each with disco#info (name, password protection).
func (c *conn) discoverRoomsOnHost(ctx context.Context, sess *mx.Session, mucHost string) ([]RoomInfo, error) {
	mucJID, err := jid.Parse(mucHost)
	if err != nil {
		return nil, fmt.Errorf("parse muc host %q: %w", mucHost, err)
	}

	// Bound each host so one slow/unreachable remote server (e.g. an s2s peer
	// that never answers) cannot stall discovery of the others.
	ctx, cancel := context.WithTimeout(ctx, c.cfg.DialTimeout)
	defer cancel()

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

// dedupeHosts merges the primary host, configured extra hosts, and caller-supplied
// hosts into a single ordered, deduplicated list (primary first). Empty entries
// are dropped.
func dedupeHosts(primary string, configured, extra []string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(h string) {
		if h == "" {
			return
		}
		if _, ok := seen[h]; ok {
			return
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	add(primary)
	for _, h := range configured {
		add(h)
	}
	for _, h := range extra {
		add(h)
	}
	return out
}
