package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kidkuddy/momo/internal/core"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func msg(id, room, sender, body string, at time.Time) core.Message {
	return core.Message{
		EventID: id, RoomID: room, Sender: sender, Body: body,
		Timestamp: at, Kind: core.KindText,
	}
}

// A sync replay redelivers events. Saving the same event twice must not duplicate
// the row, or history double-counts every restart.
func TestSaveMessageIsIdempotent(t *testing.T) {
	s, ctx := open(t), context.Background()
	m := msg("$1", "!r", "@a:x", "hello", time.Now())

	for range 3 {
		if err := s.SaveMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Messages(ctx, core.HistoryFilter{RoomID: "!r"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("stored %d rows for one event, want 1", len(got))
	}
	if got[0].Body != "hello" {
		t.Fatalf("body %q", got[0].Body)
	}
}

func TestFiltersAndOrdering(t *testing.T) {
	s, ctx := open(t), context.Background()
	base := time.Now().Truncate(time.Second)

	must(t, s.SaveMessage(ctx, msg("$1", "!a", "@x:s", "first", base)))
	must(t, s.SaveMessage(ctx, msg("$2", "!a", "@y:s", "second", base.Add(time.Second))))
	must(t, s.SaveMessage(ctx, msg("$3", "!b", "@x:s", "other room", base.Add(2*time.Second))))

	threaded := msg("$4", "!a", "@x:s", "in thread", base.Add(3*time.Second))
	threaded.ThreadRoot = "$1"
	must(t, s.SaveMessage(ctx, threaded))

	t.Run("by room", func(t *testing.T) {
		got, err := s.Messages(ctx, core.HistoryFilter{RoomID: "!a"})
		must(t, err)
		if len(got) != 3 {
			t.Fatalf("got %d, want 3", len(got))
		}
		// Newest first, so a LIMIT keeps the most recent.
		if got[0].EventID != "$4" {
			t.Fatalf("first result %q, want $4", got[0].EventID)
		}
	})
	t.Run("by thread", func(t *testing.T) {
		got, err := s.Messages(ctx, core.HistoryFilter{ThreadRoot: "$1"})
		must(t, err)
		if len(got) != 1 || got[0].EventID != "$4" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("by sender", func(t *testing.T) {
		got, err := s.Messages(ctx, core.HistoryFilter{Sender: "@y:s"})
		must(t, err)
		if len(got) != 1 || got[0].Body != "second" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("limit", func(t *testing.T) {
		got, err := s.Messages(ctx, core.HistoryFilter{RoomID: "!a", Limit: 2})
		must(t, err)
		if len(got) != 2 {
			t.Fatalf("got %d, want 2", len(got))
		}
	})
	t.Run("since", func(t *testing.T) {
		got, err := s.Messages(ctx, core.HistoryFilter{RoomID: "!a", Since: base.Add(2 * time.Second)})
		must(t, err)
		if len(got) != 1 || got[0].EventID != "$4" {
			t.Fatalf("got %+v", got)
		}
	})
}

// Redaction means the event happened but its content no longer exists. The row must
// survive so the transcript keeps its shape.
func TestMarkRedactedKeepsRowDropsContent(t *testing.T) {
	s, ctx := open(t), context.Background()
	m := msg("$1", "!r", "@a:x", "secret", time.Now())
	m.HTML = "<b>secret</b>"
	must(t, s.SaveMessage(ctx, m))
	must(t, s.MarkRedacted(ctx, "!r", "$1"))

	got, err := s.Messages(ctx, core.HistoryFilter{RoomID: "!r"})
	must(t, err)
	if len(got) != 1 {
		t.Fatalf("row disappeared on redaction")
	}
	if !got[0].Redacted || got[0].Body != "" || got[0].HTML != "" {
		t.Fatalf("content survived redaction: %+v", got[0])
	}
}

func TestReactions(t *testing.T) {
	s, ctx := open(t), context.Background()
	r := core.Reaction{EventID: "$r1", RoomID: "!r", Sender: "@a:x", TargetID: "$1", Key: "👍", Timestamp: time.Now()}
	must(t, s.SaveReaction(ctx, r))
	must(t, s.SaveReaction(ctx, r)) // duplicate delivery must not double up

	got, err := s.Reactions(ctx, "!r", "$1")
	must(t, err)
	if len(got) != 1 || got[0].Key != "👍" {
		t.Fatalf("got %+v", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Clearing must take everything about a room with it. A half-cleared room is worse
// than either state: the agent would resume a conversation whose messages are gone.
func TestClearRoomRemovesEverything(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now()

	must(t, s.SaveMessage(ctx, msg("$1", "!a", "@x:s", "hello", now)))
	must(t, s.SaveMessage(ctx, msg("$2", "!keep", "@x:s", "other room", now)))
	must(t, s.SaveReaction(ctx, core.Reaction{EventID: "$r", RoomID: "!a", TargetID: "$1", Key: "👍", Timestamp: now}))
	must(t, s.SavePoll(ctx, core.PollRecord{
		EventID: "$p", RoomID: "!a", Question: "?", Timestamp: now,
		Answers: []core.PollAnswer{{ID: "answer-0", Text: "yes"}},
	}))
	must(t, s.SavePollVote(ctx, core.PollVote{EventID: "$v", PollID: "$p", RoomID: "!a", Sender: "@x:s", Timestamp: now}))
	must(t, s.SetSession(ctx, "!a", "$1", "sess-1"))

	must(t, s.ClearRoom(ctx, "!a"))

	if got, _ := s.Messages(ctx, core.HistoryFilter{RoomID: "!a"}); len(got) != 0 {
		t.Errorf("%d messages survived", len(got))
	}
	if got, _ := s.Reactions(ctx, "!a", "$1"); len(got) != 0 {
		t.Errorf("%d reactions survived", len(got))
	}
	if _, err := s.Poll(ctx, "!a", "$p"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("poll survived: %v", err)
	}
	if got, _ := s.PollVotes(ctx, "$p"); len(got) != 0 {
		t.Errorf("%d votes survived", len(got))
	}
	if got, _ := s.SessionFor(ctx, "!a", "$1", 0); got != "" {
		t.Errorf("agent session survived: %q", got)
	}
	// Other rooms must be untouched.
	if got, _ := s.Messages(ctx, core.HistoryFilter{RoomID: "!keep"}); len(got) != 1 {
		t.Errorf("clearing one room took %d messages from another", 1-len(got))
	}
}

// Forgetting the session without wiping the transcript: start a new conversation but
// keep the record of the old one.
func TestClearSessionsKeepsHistory(t *testing.T) {
	s, ctx := open(t), context.Background()
	must(t, s.SaveMessage(ctx, msg("$1", "!a", "@x:s", "hello", time.Now())))
	must(t, s.SetSession(ctx, "!a", "$1", "sess-1"))

	must(t, s.ClearSessions(ctx, "!a"))

	if got, _ := s.SessionFor(ctx, "!a", "$1", 0); got != "" {
		t.Errorf("session survived: %q", got)
	}
	if got, _ := s.Messages(ctx, core.HistoryFilter{RoomID: "!a"}); len(got) != 1 {
		t.Errorf("history was destroyed: %d messages", len(got))
	}
}

// Three unanswered inbox reminders are one overdue task. Doing it late must settle
// the whole backlog, not leave stale reminders nagging about finished work.
func TestResolveSupersedesSameKind(t *testing.T) {
	s, ctx := open(t), context.Background()
	for i, root := range []string{"$t1", "$t2", "$t3"} {
		must(t, s.CreateThread(ctx, core.Thread{
			RoomID: "!r", ThreadRoot: root, Kind: "inbox",
			State: core.ThreadOpen, CreatedAt: time.Now().Add(time.Duration(i) * time.Hour),
		}))
	}
	must(t, s.CreateThread(ctx, core.Thread{
		RoomID: "!r", ThreadRoot: "$other", Kind: "papers",
		State: core.ThreadOpen, CreatedAt: time.Now(),
	}))

	closed, err := s.SetThreadState(ctx, "!r", "$t3", core.ThreadResolved, true)
	must(t, err)
	if closed != 3 {
		t.Fatalf("closed %d threads, want 3 (the one done plus two superseded)", closed)
	}

	open, err := s.OpenThreads(ctx, "!r", "")
	must(t, err)
	if len(open) != 1 || open[0].Kind != "papers" {
		t.Fatalf("a different kind was swept up: %+v", open)
	}
	// The one actually done is distinguishable from the ones dropped.
	done, err := s.Thread(ctx, "!r", "$t3")
	must(t, err)
	if done.State != core.ThreadResolved {
		t.Errorf("state = %q, want resolved", done.State)
	}
	dropped, err := s.Thread(ctx, "!r", "$t1")
	must(t, err)
	if dropped.State != core.ThreadSuperseded {
		t.Errorf("state = %q, want superseded", dropped.State)
	}
}

func TestResolveOnlyLeavesOthersOpen(t *testing.T) {
	s, ctx := open(t), context.Background()
	for _, root := range []string{"$a", "$b"} {
		must(t, s.CreateThread(ctx, core.Thread{
			RoomID: "!r", ThreadRoot: root, Kind: "inbox",
			State: core.ThreadOpen, CreatedAt: time.Now(),
		}))
	}
	closed, err := s.SetThreadState(ctx, "!r", "$a", core.ThreadResolved, false)
	must(t, err)
	if closed != 1 {
		t.Fatalf("closed %d, want 1", closed)
	}
	if open, _ := s.OpenThreads(ctx, "!r", "inbox"); len(open) != 1 {
		t.Fatalf("%d open, want 1", len(open))
	}
}

// A session abandoned hours ago should not be resumed: the context has grown and the
// conversation has moved on.
func TestSessionExpiresWhenIdle(t *testing.T) {
	s, ctx := open(t), context.Background()
	must(t, s.SetSession(ctx, "!r", "$t", "sess-1"))

	if got, _ := s.SessionFor(ctx, "!r", "$t", time.Hour); got != "sess-1" {
		t.Fatalf("fresh session not returned: %q", got)
	}
	// A zero window means never expire.
	if got, _ := s.SessionFor(ctx, "!r", "$t", 0); got != "sess-1" {
		t.Fatalf("zero idle expired a session: %q", got)
	}
	// Anything already older than the window is gone.
	if got, _ := s.SessionFor(ctx, "!r", "$t", time.Nanosecond); got != "" {
		t.Fatalf("stale session returned: %q", got)
	}
}

// A daily sweep run twice must not nag twice. The nudge timestamp is what makes the
// interval enforceable, so it has to survive a read.
func TestNudgeTimestampRoundTrips(t *testing.T) {
	s, ctx := open(t), context.Background()
	must(t, s.CreateThread(ctx, core.Thread{
		RoomID: "!r", ThreadRoot: "$t", Kind: "inbox",
		State: core.ThreadOpen, CreatedAt: time.Now(),
	}))

	got, err := s.Thread(ctx, "!r", "$t")
	must(t, err)
	if !got.NudgedAt.IsZero() {
		t.Fatalf("a new thread claims to have been nudged: %v", got.NudgedAt)
	}

	at := time.Now().Truncate(time.Millisecond)
	must(t, s.MarkNudged(ctx, "!r", "$t", at))

	got, err = s.Thread(ctx, "!r", "$t")
	must(t, err)
	if !got.NudgedAt.Equal(at) {
		t.Fatalf("nudged_at = %v, want %v", got.NudgedAt, at)
	}
	// It must come back from the list query too — that is what the sweep reads.
	list, err := s.OpenThreads(ctx, "!r", "")
	must(t, err)
	if len(list) != 1 || !list[0].NudgedAt.Equal(at) {
		t.Fatalf("list lost nudged_at: %+v", list)
	}
}

// A database created before a column existed must pick it up on open. Without this
// every schema change breaks every existing install.
func TestMigrationAddsColumnToOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite3", path)
	must(t, err)
	// The threads table as it shipped before nudged_at.
	_, err = old.Exec(`CREATE TABLE threads (
        room_id TEXT NOT NULL, thread_root TEXT NOT NULL, kind TEXT NOT NULL DEFAULT '',
        brief TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'open',
        created_at INTEGER NOT NULL, resolved_at INTEGER NOT NULL DEFAULT 0,
        PRIMARY KEY (room_id, thread_root))`)
	must(t, err)
	_, err = old.Exec(`INSERT INTO threads (room_id, thread_root, created_at) VALUES ('!r','$t',1)`)
	must(t, err)
	must(t, old.Close())

	s, err := Open(path)
	must(t, err)
	defer s.Close()

	ctx := context.Background()
	// The pre-existing row survives, and the new column is usable on it.
	got, err := s.Thread(ctx, "!r", "$t")
	must(t, err)
	if !got.NudgedAt.IsZero() {
		t.Fatalf("nudged_at = %v, want zero", got.NudgedAt)
	}
	must(t, s.MarkNudged(ctx, "!r", "$t", time.Now()))

	// Opening twice must not fail on the already-applied migration.
	s2, err := Open(path)
	must(t, err)
	s2.Close()
}
