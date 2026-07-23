package matrix

import (
	"context"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"

	"github.com/kidkuddy/momo/internal/core"
)

// Handlers are the callbacks the sync loop drives. The daemon supplies them; this
// package stays free of policy.
type Handlers struct {
	OnMessage  func(ctx context.Context, m core.Message)
	OnReaction func(ctx context.Context, r core.Reaction)
	OnRedact   func(ctx context.Context, roomID, targetID string)
	// ShouldJoin decides whether to accept an invite from this inviter.
	ShouldJoin func(inviter string) bool
	OnJoined   func(roomID string)
}

// Sync runs the event loop until ctx is cancelled. mautrix handles rate limits,
// backoff and re-auth internally, so this only maps events into domain types.
func (c *Client) Sync(ctx context.Context, h Handlers) error {
	syncer := c.mx.Syncer.(*mautrix.DefaultSyncer)

	syncer.OnEventType(event.StateMember, func(ctx context.Context, evt *event.Event) {
		if evt.GetStateKey() != c.mx.UserID.String() ||
			evt.Content.AsMember().Membership != event.MembershipInvite {
			return
		}
		if h.ShouldJoin == nil || !h.ShouldJoin(evt.Sender.String()) {
			return
		}
		if _, err := c.mx.JoinRoomByID(ctx, evt.RoomID); err != nil {
			c.mx.Log.Warn().Err(err).Stringer("room", evt.RoomID).Msg("join failed")
			return
		}
		if h.OnJoined != nil {
			h.OnJoined(evt.RoomID.String())
		}
	})

	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		// The first sync after a fresh start has no position to resume from, so the
		// server replies with recent history. Acting on it would re-run old commands.
		if ctx.Value(mautrix.SyncTokenContextKey) == "" {
			return
		}
		if h.OnMessage != nil {
			h.OnMessage(ctx, toMessage(evt))
		}
	})

	syncer.OnEventType(event.EventReaction, func(ctx context.Context, evt *event.Event) {
		if ctx.Value(mautrix.SyncTokenContextKey) == "" || h.OnReaction == nil {
			return
		}
		rel := evt.Content.AsReaction().RelatesTo
		h.OnReaction(ctx, core.Reaction{
			EventID:   evt.ID.String(),
			RoomID:    evt.RoomID.String(),
			Sender:    evt.Sender.String(),
			TargetID:  rel.EventID.String(),
			Key:       rel.Key,
			Timestamp: time.UnixMilli(evt.Timestamp),
		})
	})

	syncer.OnEventType(event.EventRedaction, func(ctx context.Context, evt *event.Event) {
		if ctx.Value(mautrix.SyncTokenContextKey) == "" || h.OnRedact == nil {
			return
		}
		h.OnRedact(ctx, evt.RoomID.String(), evt.Redacts.String())
	})

	return c.mx.SyncWithContext(ctx)
}

// toMessage maps a Matrix event onto the domain type, keeping the raw content so
// history does not lose what the domain model has no name for.
func toMessage(evt *event.Event) core.Message {
	msg := evt.Content.AsMessage()
	m := core.Message{
		EventID:   evt.ID.String(),
		RoomID:    evt.RoomID.String(),
		Sender:    evt.Sender.String(),
		Timestamp: time.UnixMilli(evt.Timestamp),
		Kind:      core.KindText,
	}
	if msg == nil {
		return m
	}
	m.Kind = toKind(msg.MsgType)
	m.Body = msg.Body
	m.HTML = msg.FormattedBody
	if rel := msg.RelatesTo; rel != nil {
		m.ThreadRoot = rel.GetThreadParent().String()
		m.ReplyTo = rel.GetReplyTo().String()
	}
	if msg.URL != "" {
		m.URL = string(msg.URL)
		m.Filename = msg.Body
	}
	if msg.File != nil {
		m.URL = string(msg.File.URL)
		m.Filename = msg.Body
	}
	m.Raw = evt.Content.VeryRaw
	return m
}

func toKind(t event.MessageType) core.Kind {
	switch t {
	case event.MsgNotice:
		return core.KindNotice
	case event.MsgEmote:
		return core.KindEmote
	case event.MsgImage:
		return core.KindImage
	case event.MsgAudio:
		return core.KindAudio
	case event.MsgVideo:
		return core.KindVideo
	case event.MsgFile:
		return core.KindFile
	default:
		return core.KindText
	}
}
