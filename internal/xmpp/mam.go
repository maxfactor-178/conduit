package xmpp

import (
	"context"
	"fmt"

	mxhistory "mellium.im/xmpp/history"
	"mellium.im/xmpp/jid"

	"github.com/google/uuid"
)

func (c *conn) QueryMAM(ctx context.Context, q MAMQuery) (<-chan MAMResult, error) {
	c.mu.RLock()
	sess := c.session
	c.mu.RUnlock()
	if sess == nil {
		return nil, fmt.Errorf("not connected")
	}

	queryID := uuid.NewString()
	ch := make(chan MAMResult, 32)

	c.mamMu.Lock()
	c.mamPending[queryID] = ch
	c.mamMu.Unlock()

	go func() {
		defer func() {
			c.mamMu.Lock()
			delete(c.mamPending, queryID)
			c.mamMu.Unlock()
			close(ch)
		}()

		filter := buildFilter(q)
		var toJID jid.JID
		if q.With != "" {
			var err error
			toJID, err = jid.Parse(q.With)
			if err != nil {
				c.log.Warn("mam: invalid 'with' jid", "jid", q.With, "err", err)
				return
			}
		}

		if _, err := mxhistory.Fetch(ctx, filter, toJID, sess); err != nil {
			c.log.Warn("mam fetch failed", "err", err, "with", q.With)
		}
	}()

	return ch, nil
}

func buildFilter(q MAMQuery) mxhistory.Query {
	max := q.Max
	if max <= 0 {
		max = 50
	}
	// Use RSM-based pagination (Last+PageID → <set><before>ID</before></set>)
	// instead of the MAM:2#extended data form fields (before-id / after-id),
	// which many servers including ejabberd do not support.
	f := mxhistory.Query{
		Limit:  uint64(max),
		Last:   true,     // fetch from most recent page, scrolling backwards
		PageID: q.Before, // when non-empty: fetch the page ending before this msg ID
	}
	return f
}
