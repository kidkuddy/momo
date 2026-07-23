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
			Chat: chat, History: nopHistory{}, Engine: fakeEngine{},
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
		Chat: chat, History: nopHistory{}, Engine: fakeEngine{},
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

type fakeEngine struct{}

func (fakeEngine) Name() string                       { return "fake" }
func (fakeEngine) Run(context.Context, string) string { return "reply" }

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
