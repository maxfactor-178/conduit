package history

import (
	"context"
	"log/slog"
	"time"

	"conduit/internal/xmpp"
	"conduit/pkg/protocol"
)

// Service issues MAM queries on behalf of users.
type Service struct {
	defaultLimit int
	maxLimit     int
	log          *slog.Logger
}

// New creates a history service.
func New(defaultLimit, maxLimit int, log *slog.Logger) *Service {
	if defaultLimit <= 0 {
		defaultLimit = 50
	}
	if maxLimit <= 0 {
		maxLimit = 200
	}
	return &Service{defaultLimit: defaultLimit, maxLimit: maxLimit, log: log}
}

// Broadcaster can receive history batch events.
type Broadcaster interface {
	Broadcast(protocol.OutboundMessage)
}

// LoadConversationHistory fetches MAM history for a 1:1 conversation and
// broadcasts the result batch to the user.
func (s *Service) LoadConversationHistory(ctx context.Context, u Broadcaster, conn xmpp.XMPPConn, conv, before string, limit int) {
	limit = s.clampLimit(limit)
	ch, err := conn.QueryMAM(ctx, xmpp.MAMQuery{
		With:   conv,
		Before: before,
		Max:    limit,
	})
	if err != nil {
		s.log.Warn("mam query failed", "conv", conv, "err", err)
		return
	}
	s.drainAndBroadcast(ctx, u, ch)
}

// LoadRoomHistory fetches MAM history for a MUC room.
func (s *Service) LoadRoomHistory(ctx context.Context, u Broadcaster, conn xmpp.XMPPConn, room string) {
	ch, err := conn.QueryMAM(ctx, xmpp.MAMQuery{
		With: room,
		Max:  s.defaultLimit,
	})
	if err != nil {
		s.log.Warn("mam room query failed", "room", room, "err", err)
		return
	}
	s.drainAndBroadcast(ctx, u, ch)
}

func (s *Service) drainAndBroadcast(ctx context.Context, u Broadcaster, ch <-chan xmpp.MAMResult) {
	var batch []protocol.OutboundMessage
	for {
		select {
		case result, ok := <-ch:
			if !ok {
				goto done
			}
			batch = append(batch, toOutbound(result))
		case <-ctx.Done():
			return
		}
	}
done:
	if len(batch) == 0 {
		return
	}
	u.Broadcast(protocol.OutboundMessage{
		Type:    protocol.TypeHistoryBatch,
		Payload: batch,
	})
}

func (s *Service) clampLimit(limit int) int {
	if limit <= 0 {
		return s.defaultLimit
	}
	if limit > s.maxLimit {
		return s.maxLimit
	}
	return limit
}

func toOutbound(r xmpp.MAMResult) protocol.OutboundMessage {
	msgType := protocol.TypeChat
	if r.IsRoom {
		msgType = protocol.TypeRoomMessage
	}
	return protocol.OutboundMessage{
		Type:      msgType,
		From:      r.From,
		To:        r.To,
		Room:      r.Room,
		Body:      r.Body,
		Timestamp: r.Timestamp.UTC().Format(time.RFC3339),
	}
}
