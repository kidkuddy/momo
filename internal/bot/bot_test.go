package bot

import (
	"context"
	"testing"
	"time"

	"github.com/kidkuddy/momo/internal/core"
)

const (
	self    = "@momo:example.org"
	allowed = "@owner:example.org"
)

func newBot() *Bot {
	return New(Deps{SelfID: self, Allowed: allowed})
}

// The allowlist is the whole security model: it is the only thing between a chat
// message and an engine run on the host. Every rejection path matters.
func TestShouldAnswer(t *testing.T) {
	cases := []struct {
		name string
		msg  core.Message
		want bool
	}{
		{"allowed text", core.Message{Sender: allowed, Kind: core.KindText, Body: "hi"}, true},
		{"stranger", core.Message{Sender: "@eve:example.org", Kind: core.KindText, Body: "rm -rf"}, false},
		{"our own echo", core.Message{Sender: self, Kind: core.KindText, Body: "hi"}, false},
		{"non-text", core.Message{Sender: allowed, Kind: core.KindImage, Body: "cat.png"}, false},
		{"notice", core.Message{Sender: allowed, Kind: core.KindNotice, Body: "hi"}, false},
		{"empty body", core.Message{Sender: allowed, Kind: core.KindText, Body: ""}, false},
		{"redacted", core.Message{Sender: allowed, Kind: core.KindText, Body: "hi", Redacted: true}, false},
	}
	b := newBot()
	for _, c := range cases {
		if got := b.ShouldAnswer(c.msg); got != c.want {
			t.Errorf("%s: ShouldAnswer = %v, want %v", c.name, got, c.want)
		}
	}
}

// A reply must land in the caller's thread, and open one when the caller did not.
// Otherwise a conversation with momo floods the main timeline.
func TestHandleRepliesInThread(t *testing.T) {
	for _, c := range []struct {
		name       string
		incoming   core.Message
		wantThread string
	}{
		{"opens a thread on the message", core.Message{EventID: "$a", RoomID: "!r", Body: "hi"}, "$a"},
		{"stays in an existing thread", core.Message{EventID: "$b", RoomID: "!r", Body: "hi", ThreadRoot: "$root"}, "$root"},
	} {
		chat := &fakeChat{}
		b := New(Deps{
			Chat: chat, History: nopHistory{}, Engine: &fakeEngine{},
			SelfID: self, Allowed: allowed,
			Chunk: func(s string, _ int) []string { return []string{s} },
		})
		b.Handle(context.Background(), c.incoming)

		if len(chat.sent) != 1 {
			t.Fatalf("%s: sent %d messages, want 1", c.name, len(chat.sent))
		}
		if got := chat.sent[0].opts.ThreadRoot; got != c.wantThread {
			t.Errorf("%s: thread root %q, want %q", c.name, got, c.wantThread)
		}
		// Typing must be turned off again even on the happy path, or the indicator
		// sticks until it times out.
		if chat.typing != 0 {
			t.Errorf("%s: typing left on (balance %d)", c.name, chat.typing)
		}
	}
}

// A long reply is split, and every chunk must stay in the same thread.
func TestHandleChunksStayThreaded(t *testing.T) {
	chat := &fakeChat{}
	b := New(Deps{
		Chat: chat, History: nopHistory{}, Engine: &fakeEngine{},
		SelfID: self, Allowed: allowed,
		Chunk: func(s string, _ int) []string { return []string{"one", "two", "three"} },
	})
	b.Handle(context.Background(), core.Message{EventID: "$a", RoomID: "!r", Body: "hi"})

	if len(chat.sent) != 3 {
		t.Fatalf("sent %d chunks, want 3", len(chat.sent))
	}
	for i, s := range chat.sent {
		if s.opts.ThreadRoot != "$a" {
			t.Errorf("chunk %d thread root %q, want $a", i, s.opts.ThreadRoot)
		}
	}
}

// ---- doubles -----------------------------------------------------------

type sent struct {
	roomID string
	body   string
	opts   core.SendOpts
}

type fakeChat struct {
	sent   []sent
	typing int // incremented on, decremented off; must balance to zero
}

func (f *fakeChat) Send(_ context.Context, roomID, body string, opts core.SendOpts) (string, error) {
	f.sent = append(f.sent, sent{roomID, body, opts})
	return "$sent", nil
}
func (f *fakeChat) Typing(_ context.Context, _ string, typing bool) error {
	if typing {
		f.typing++
	} else {
		f.typing--
	}
	return nil
}

func (f *fakeChat) SendMedia(context.Context, string, string, core.SendOpts) (string, error) {
	return "", nil
}
func (f *fakeChat) React(context.Context, string, string, string) (string, error) { return "", nil }
func (f *fakeChat) Edit(context.Context, string, string, string, core.SendOpts) (string, error) {
	return "", nil
}
func (f *fakeChat) Redact(context.Context, string, string, string) (string, error) { return "", nil }
func (f *fakeChat) MarkRead(context.Context, string, string) error                 { return nil }
func (f *fakeChat) StartPoll(context.Context, string, core.Poll) (string, error)   { return "", nil }
func (f *fakeChat) EndPoll(context.Context, string, string) (string, error)        { return "", nil }

type fakeEngine struct {
	reply string
	tasks []core.Task
}

func (fakeEngine) Name() string { return "fake" }
func (f *fakeEngine) Run(_ context.Context, t core.Task) (core.Answer, error) {
	f.tasks = append(f.tasks, t)
	reply := f.reply
	if reply == "" {
		reply = "reply"
	}
	return core.Answer{Reply: reply, SessionID: "sess-1"}, nil
}

// fakeSessions records the thread-to-session mapping in memory.
type fakeSessions struct{ ids map[string]string }

func newFakeSessions() *fakeSessions { return &fakeSessions{ids: map[string]string{}} }

func (f *fakeSessions) SessionFor(_ context.Context, room, thread string, _ time.Duration) (string, error) {
	return f.ids[room+thread], nil
}
func (f *fakeSessions) SetSession(_ context.Context, room, thread, id string) error {
	f.ids[room+thread] = id
	return nil
}

type nopHistory struct{}

func (nopHistory) SaveMessage(context.Context, core.Message) error   { return nil }
func (nopHistory) SaveReaction(context.Context, core.Reaction) error { return nil }
func (nopHistory) MarkRedacted(context.Context, string, string) error {
	return nil
}
func (nopHistory) Messages(context.Context, core.HistoryFilter) ([]core.Message, error) {
	return nil, nil
}
func (nopHistory) Reactions(context.Context, string, string) ([]core.Reaction, error) {
	return nil, nil
}
func (nopHistory) CreateThread(context.Context, core.Thread) error { return nil }
func (nopHistory) Thread(context.Context, string, string) (core.Thread, error) {
	return core.Thread{}, nil
}
func (nopHistory) OpenThreads(context.Context, string, string) ([]core.Thread, error) {
	return nil, nil
}
func (nopHistory) SetThreadState(context.Context, string, string, core.ThreadState, bool) (int, error) {
	return 0, nil
}
func (nopHistory) SavePoll(context.Context, core.PollRecord) error   { return nil }
func (nopHistory) SavePollVote(context.Context, core.PollVote) error { return nil }
func (nopHistory) ClosePoll(context.Context, string, string, time.Time) error {
	return nil
}
func (nopHistory) Poll(context.Context, string, string) (core.PollRecord, error) {
	return core.PollRecord{}, nil
}
func (nopHistory) PollVotes(context.Context, string) ([]core.PollVote, error) {
	return nil, nil
}
func (nopHistory) Close() error { return nil }

// A thread is one conversation: the agent session id from the last reply must come
// back as ResumeID on the next message, or every message starts a stranger.
func TestHandleResumesSessionPerThread(t *testing.T) {
	eng := &fakeEngine{}
	sessions := newFakeSessions()
	b := New(Deps{
		Chat: &fakeChat{}, History: nopHistory{}, Engine: eng, Sessions: sessions,
		SelfID: self, Allowed: allowed,
		Chunk: func(s string, _ int) []string { return []string{s} },
	})

	b.Handle(context.Background(), core.Message{EventID: "$a", RoomID: "!r", Body: "first"})
	if got := eng.tasks[0].ResumeID; got != "" {
		t.Fatalf("first message resumed %q, want a fresh session", got)
	}

	b.Handle(context.Background(), core.Message{EventID: "$b", RoomID: "!r", Body: "second", ThreadRoot: "$a"})
	if got := eng.tasks[1].ResumeID; got != "sess-1" {
		t.Fatalf("second message resumed %q, want sess-1", got)
	}

	// A different thread is a different conversation.
	b.Handle(context.Background(), core.Message{EventID: "$c", RoomID: "!r", Body: "elsewhere"})
	if got := eng.tasks[2].ResumeID; got != "" {
		t.Fatalf("new thread resumed %q, want a fresh session", got)
	}
}

// An agent engine answers by calling the CLI. When it has, the bot must not also
// post the engine's text, or every message gets answered twice.
func TestHandleDoesNotDoubleReply(t *testing.T) {
	chat := &fakeChat{}
	sent := 0
	b := New(Deps{
		Chat: chat, History: nopHistory{}, Engine: &fakeEngine{}, Sessions: newFakeSessions(),
		SelfID: self, Allowed: allowed,
		Chunk:        func(s string, _ int) []string { return []string{s} },
		SentInThread: func(string) int { return sent },
	})

	// The agent posted one message of its own while running.
	sent = 0
	origRun := b.d.Engine
	b.d.Engine = &replyingEngine{inner: origRun, onRun: func() { sent++ }}

	b.Handle(context.Background(), core.Message{EventID: "$a", RoomID: "!r", Body: "hi"})
	if len(chat.sent) != 0 {
		t.Fatalf("bot posted %d messages on top of the agent's own reply", len(chat.sent))
	}
}

// A session that ends without saying anything must not look like a hang.
func TestHandleSpeaksWhenEngineIsSilent(t *testing.T) {
	chat := &fakeChat{}
	b := New(Deps{
		Chat: chat, History: nopHistory{}, Engine: &fakeEngine{reply: " "}, Sessions: newFakeSessions(),
		SelfID: self, Allowed: allowed,
		Chunk: func(s string, _ int) []string { return []string{s} },
	})
	b.Handle(context.Background(), core.Message{EventID: "$a", RoomID: "!r", Body: "hi"})
	if len(chat.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(chat.sent))
	}
}

// replyingEngine simulates an agent that answers through the CLI during its run.
type replyingEngine struct {
	inner core.Engine
	onRun func()
}

func (r *replyingEngine) Name() string { return "replying" }
func (r *replyingEngine) Run(ctx context.Context, t core.Task) (core.Answer, error) {
	r.onRun()
	return core.Answer{Reply: "this must not be posted", SessionID: "sess-1"}, nil
}
