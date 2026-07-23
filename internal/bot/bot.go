// Package bot is the application layer: the rules that decide what momo does with
// an incoming message. It depends only on the ports in core, so it can be driven by
// a test double as easily as by a real homeserver.
package bot

import (
	"context"
	"log"
	"time"

	"github.com/kidkuddy/momo/internal/core"
)

// Deps are the collaborators the bot needs. Constructor injection rather than
// package globals: the CLI wires a subset, the daemon wires all of it.
type Deps struct {
	Chat    core.Chat
	Rooms   core.Rooms
	History core.History
	Engine  core.Engine

	// SelfID is momo's own user ID, so it never answers itself.
	SelfID string
	// Allowed is the only user momo obeys. This is the entire security model:
	// it is the sole gate between a chat message and an engine run.
	Allowed string
	// MaxBody caps one outgoing event; longer replies are split.
	MaxBody int
	// Chunk splits an over-long body on line boundaries.
	Chunk func(string, int) []string
}

type Bot struct{ d Deps }

func New(d Deps) *Bot {
	if d.MaxBody == 0 {
		d.MaxBody = 32 << 10
	}
	return &Bot{d: d}
}

// ShouldAnswer decides whether an incoming message is momo's to act on.
//
// Kept separate from Handle and free of I/O so the security rule can be tested
// exhaustively without a homeserver.
func (b *Bot) ShouldAnswer(m core.Message) bool {
	if m.Sender == b.d.SelfID || m.Sender != b.d.Allowed {
		return false
	}
	if m.Redacted || m.Body == "" {
		return false
	}
	return m.Kind == core.KindText
}

// Record files a message in history regardless of whether momo acts on it. History
// is a record of the room, not of the bot's decisions.
func (b *Bot) Record(ctx context.Context, m core.Message) {
	if err := b.d.History.SaveMessage(ctx, m); err != nil {
		log.Printf("history: %v", err)
	}
}

// Handle answers one message: run the engine, then reply in the caller's thread.
//
// Replies open a thread on the incoming event when there is not one already, so a
// conversation with momo stays out of the main timeline.
func (b *Bot) Handle(ctx context.Context, m core.Message) {
	root := m.ThreadRoot
	if root == "" {
		root = m.EventID
	}

	if err := b.d.Chat.Typing(ctx, m.RoomID, true); err != nil {
		log.Printf("typing: %v", err)
	}
	defer func() {
		if err := b.d.Chat.Typing(ctx, m.RoomID, false); err != nil {
			log.Printf("typing: %v", err)
		}
	}()

	out := b.d.Engine.Run(ctx, m.Body)
	for _, chunk := range b.d.Chunk(out, b.d.MaxBody) {
		eventID, err := b.d.Chat.Send(ctx, m.RoomID, chunk, core.SendOpts{ThreadRoot: root})
		if err != nil {
			log.Printf("send: %v", err)
			return
		}
		// Record our own reply too: the protocol cannot give it back to us later,
		// so this local copy is the only durable record of what momo said.
		b.Record(ctx, core.Message{
			EventID:    eventID,
			RoomID:     m.RoomID,
			Sender:     b.d.SelfID,
			Timestamp:  time.Now(),
			Kind:       core.KindText,
			Body:       chunk,
			ThreadRoot: root,
		})
	}
}
