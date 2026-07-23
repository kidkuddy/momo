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
	Threads  core.Threads

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

	// Pin marks a thread root so outstanding conversations are one tap away.
	// Optional: pinning needs a power level momo may not have in a group room.
	Pin func(ctx context.Context, roomID, eventID string) error

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

	// Acknowledge immediately. A typing indicator is not enough on its own: the
	// server expires it, it is not attached to any particular message, and phone
	// clients show it inconsistently. A reaction lands instantly, sticks, and says
	// "this one, I have it".
	ack := b.ack(ctx, m)
	defer b.keepTyping(ctx, m.RoomID, typingNotice)()

	// A conversation the user opened is outstanding work too, so track and pin it
	// the same way a scheduled thread is. Without a kind it is a conversation
	// rather than a ritual, which is what keeps the nudge sweep off it.
	b.trackThread(ctx, m, root)

	answer, err := b.d.Engine.Run(ctx, core.Task{
		Prompt:     m.Body,
		RoomID:     m.RoomID,
		ThreadRoot: root,
		Sender:     m.Sender,
		ResumeID:   resume,
		Workdir:    b.d.Workdir,
		Now:        time.Now().Format("Monday 2 January 2006, 15:04 MST"),
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
	ack()

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

// keepTyping holds the typing indicator up for as long as the engine runs, and
// returns the function that clears it.
//
// The server expires a typing notice after the timeout it was given, so setting it
// once means it vanishes ~30s in while a run that takes minutes carries on. To the
// user that is indistinguishable from a dead bot — which is the single most common
// reason to think momo has broken when it has not.
const typingNotice = 30 * time.Second

func (b *Bot) keepTyping(ctx context.Context, roomID string, notice time.Duration) func() {
	send := func(on bool) {
		if err := b.d.Chat.Typing(ctx, roomID, on); err != nil {
			log.Printf("typing: %v", err)
		}
	}
	send(true)

	done := make(chan struct{})
	go func() {
		// Comfortably inside the expiry, so there is no gap where the indicator
		// lapses between refreshes.
		t := time.NewTicker(notice * 2 / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				send(true)
			}
		}
	}()
	return func() {
		close(done)
		// A cancelled context cannot clear the indicator, and leaving it set makes
		// momo look busy forever. Use a fresh one for the last call.
		if ctx.Err() != nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
		}
		send(false)
	}
}

// ack puts 👀 on the message and returns the function that clears it, so the mark
// means "working on this" rather than "seen at some point".
//
// A crash leaves it in place, which is the right failure: a stuck 👀 is a visible
// sign that something died mid-reply.
func (b *Bot) ack(ctx context.Context, m core.Message) func() {
	id, err := b.d.Chat.React(ctx, m.RoomID, m.EventID, "👀")
	if err != nil {
		log.Printf("ack: %v", err)
		return func() {}
	}
	return func() {
		if _, err := b.d.Chat.Redact(ctx, m.RoomID, id, ""); err != nil {
			log.Printf("ack clear: %v", err)
		}
	}
}

// trackThread records a user-opened conversation as an outstanding thread and pins
// it. Only the root message opens one — later messages belong to a thread that
// already exists.
func (b *Bot) trackThread(ctx context.Context, m core.Message, root string) {
	if b.d.Threads == nil || m.ThreadRoot != "" {
		return
	}
	if _, err := b.d.Threads.Thread(ctx, m.RoomID, root); err == nil {
		return // already tracked
	}
	if err := b.d.Threads.CreateThread(ctx, core.Thread{
		RoomID:     m.RoomID,
		ThreadRoot: root,
		Brief:      m.Body,
		State:      core.ThreadOpen,
		CreatedAt:  time.Now(),
	}); err != nil {
		log.Printf("thread: %v", err)
		return
	}
	if b.d.Pin != nil {
		if err := b.d.Pin(ctx, m.RoomID, root); err != nil {
			log.Printf("pin: %v", err) // cosmetic; needs a power level momo may lack
		}
	}
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
