package store

import (
	"context"
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
