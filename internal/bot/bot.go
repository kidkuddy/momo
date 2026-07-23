// Package bot is the application layer: the rules that decide what momo does with
// an incoming message. It depends only on the ports in core, so a test double drives
// it as easily as a real homeserver.
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
	Chat     core.Chat
	History  core.History
	Engine   core.Engine
	Sessions core.Sessions

	// SelfID is momo's own user ID, so it never answers itself.
	SelfID string
	// Allowed is the only user momo obeys. This is the entire security model: the
	// sole gate between a chat message and an engine run on this host.
	Allowed string
	// MaxBody caps one outgoing event; longer replies are split.
	MaxBody int
	// Chunk splits an over-long body on line boundaries.
	Chunk func(string, int) []string
	// Workdir bounds where an engine may operate.
	Workdir string

	// SessionIdle ends an agent session that has been untouched this long. Zero
	// keeps sessions forever. This is a cost control, not a memory one: `claude -p`
	// exits after each reply, so nothing is held open — but resuming an
	// ever-growing context gets more expensive every turn.
	SessionIdle time.Duration

	// SentInThread counts messages momo has posted into a thread. An agent engine
	// answers by calling the CLI, so this is how the bot tells "it already replied"
	// from "it finished without saying anything" — there is no flag on the result
	// that can be trusted for that.
	SentInThread func(threadRoot string) int
}

type Bot struct{ d Deps }

func New(d Deps) *Bot {
	if d.MaxBody == 0 {
		d.MaxBody = 32 << 10
	}
	if d.SentInThread == nil {
		d.SentInThread = func(string) int { return 0 }
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

// Handle answers one message.
//
// Replies open a thread on the incoming event when there is not one already, so a
// conversation with momo stays out of the main timeline and each thread maps to one
// agent session.
func (b *Bot) Handle(ctx context.Context, m core.Message) {
	root := m.ThreadRoot
	if root == "" {
		root = m.EventID
	}

	resume := b.resumeID(ctx, m.RoomID, root)
	before := b.d.SentInThread(root)

	if err := b.d.Chat.Typing(ctx, m.RoomID, true); err != nil {
		log.Printf("typing: %v", err)
	}
	defer func() {
		if err := b.d.Chat.Typing(ctx, m.RoomID, false); err != nil {
			log.Printf("typing: %v", err)
		}
	}()

	answer, err := b.d.Engine.Run(ctx, core.Task{
		Prompt:     m.Body,
		RoomID:     m.RoomID,
		ThreadRoot: root,
		Sender:     m.Sender,
		ResumeID:   resume,
		Workdir:    b.d.Workdir,
	})
	if err != nil {
		log.Printf("engine: %v", err)
		// A shutdown kills the engine mid-run. The sync position has already moved
		// past this message, so it is never retried — without a word here the user
		// sees silence and cannot tell it from a hang. The parent context is already
		// cancelled, so this notice needs its own.
		if ctx.Err() != nil {
			notice, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			b.post(notice, m.RoomID, root, "I was restarted while working on that — send it again.")
			return
		}
		b.post(ctx, m.RoomID, root, "The session failed: "+err.Error())
		return
	}

	// Save the session before posting: if sending fails, the conversation should
	// still resume next time rather than start over.
	b.saveSession(ctx, m.RoomID, root, answer.SessionID)

	if b.d.SentInThread(root) > before {
		// The agent already spoke for itself. Posting answer.Reply too would say
		// everything twice.
		return
	}
	reply := answer.Reply
	if reply == "" {
		// A session that ends silently is indistinguishable from a hang. Say so.
		reply = "The session finished without replying."
	}
	b.post(ctx, m.RoomID, root, reply)
}

func (b *Bot) post(ctx context.Context, roomID, root, text string) {
	chunks := []string{text}
	if b.d.Chunk != nil {
		chunks = b.d.Chunk(text, b.d.MaxBody)
	}
	for _, chunk := range chunks {
		eventID, err := b.d.Chat.Send(ctx, roomID, chunk, core.SendOpts{ThreadRoot: root})
		if err != nil {
			log.Printf("send: %v", err)
			return
		}
		// Record our own reply too: the protocol will not hand it back to us in a
		// queryable form, so this local copy is the durable record of what momo said.
		b.Record(ctx, core.Message{
			EventID:    eventID,
			RoomID:     roomID,
			Sender:     b.d.SelfID,
			Timestamp:  time.Now(),
			Kind:       core.KindText,
			Body:       chunk,
			ThreadRoot: root,
		})
	}
}

func (b *Bot) resumeID(ctx context.Context, roomID, root string) string {
	if b.d.Sessions == nil {
		return ""
	}
	id, err := b.d.Sessions.SessionFor(ctx, roomID, root, b.d.SessionIdle)
	if err != nil {
		log.Printf("sessions: %v", err)
		return ""
	}
	return id
}

func (b *Bot) saveSession(ctx context.Context, roomID, root, sessionID string) {
	if b.d.Sessions == nil || sessionID == "" {
		return
	}
	if err := b.d.Sessions.SetSession(ctx, roomID, root, sessionID); err != nil {
		log.Printf("sessions: %v", err)
	}
}
