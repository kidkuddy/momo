package core

import (
	"testing"
	"time"
)

var base = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func poll(max int, closed time.Time) PollRecord {
	return PollRecord{
		EventID:  "$poll",
		Question: "Ship it?",
		Answers: []PollAnswer{
			{ID: "answer-0", Text: "yes"},
			{ID: "answer-1", Text: "no"},
			{ID: "answer-2", Text: "later"},
		},
		MaxSelections: max,
		ClosedAt:      closed,
	}
}

func vote(id, sender string, at time.Duration, answers ...string) PollVote {
	return PollVote{
		EventID:   id,
		PollID:    "$poll",
		Sender:    sender,
		Timestamp: base.Add(at),
		AnswerIDs: answers,
	}
}

func counts(t PollTally) map[string]int {
	out := map[string]int{}
	for _, c := range t.Counts {
		out[c.Answer.Text] = c.Votes
	}
	return out
}

func TestTallyCountsOneVoteEach(t *testing.T) {
	got := Tally(poll(1, time.Time{}), []PollVote{
		vote("$1", "@a:x", time.Second, "answer-0"),
		vote("$2", "@b:x", 2*time.Second, "answer-0"),
		vote("$3", "@c:x", 3*time.Second, "answer-1"),
	})
	if c := counts(got); c["yes"] != 2 || c["no"] != 1 || c["later"] != 0 {
		t.Fatalf("counts = %v", c)
	}
	if got.Voters != 3 {
		t.Fatalf("voters = %d, want 3", got.Voters)
	}
}

// Changing your mind is normal in a poll; only the last vote counts.
func TestTallyLastVotePerSenderWins(t *testing.T) {
	got := Tally(poll(1, time.Time{}), []PollVote{
		vote("$1", "@a:x", time.Second, "answer-0"),
		vote("$2", "@a:x", 5*time.Second, "answer-1"),
	})
	c := counts(got)
	if c["yes"] != 0 || c["no"] != 1 {
		t.Fatalf("counts = %v, want the later vote only", c)
	}
	if got.Voters != 1 {
		t.Fatalf("one person voting twice counted as %d voters", got.Voters)
	}
}

// Votes cast after the poll closed must not count, or closing a poll would not
// actually settle anything.
func TestTallyIgnoresVotesAfterClose(t *testing.T) {
	closed := base.Add(3 * time.Second)
	got := Tally(poll(1, closed), []PollVote{
		vote("$1", "@a:x", time.Second, "answer-0"),
		vote("$2", "@b:x", 10*time.Second, "answer-1"),
	})
	c := counts(got)
	if c["yes"] != 1 || c["no"] != 0 {
		t.Fatalf("counts = %v, want the late vote discarded", c)
	}
}

// A voter changing their mind after the close keeps their last *valid* vote.
func TestTallyLateChangeDoesNotOverrideValidVote(t *testing.T) {
	closed := base.Add(3 * time.Second)
	got := Tally(poll(1, closed), []PollVote{
		vote("$1", "@a:x", time.Second, "answer-0"),
		vote("$2", "@a:x", 10*time.Second, "answer-1"),
	})
	if c := counts(got); c["yes"] != 1 || c["no"] != 0 {
		t.Fatalf("counts = %v, want the in-time vote to stand", c)
	}
}

func TestTallyRespectsMaxSelections(t *testing.T) {
	t.Run("single choice truncates", func(t *testing.T) {
		got := Tally(poll(1, time.Time{}), []PollVote{
			vote("$1", "@a:x", time.Second, "answer-0", "answer-1"),
		})
		if c := counts(got); c["yes"] != 1 || c["no"] != 0 {
			t.Fatalf("counts = %v, want only the first selection", c)
		}
	})
	t.Run("multi choice keeps up to the limit", func(t *testing.T) {
		got := Tally(poll(2, time.Time{}), []PollVote{
			vote("$1", "@a:x", time.Second, "answer-0", "answer-1", "answer-2"),
		})
		c := counts(got)
		if c["yes"] != 1 || c["no"] != 1 || c["later"] != 0 {
			t.Fatalf("counts = %v, want the first two", c)
		}
		if got.Voters != 1 {
			t.Fatalf("voters = %d, want 1", got.Voters)
		}
	})
}

// A malicious or buggy client can send anything; unknown IDs must not create
// phantom answers or crash the tally.
func TestTallyIgnoresUnknownAnswers(t *testing.T) {
	got := Tally(poll(1, time.Time{}), []PollVote{
		vote("$1", "@a:x", time.Second, "answer-99"),
		vote("$2", "@b:x", 2*time.Second, "answer-0"),
	})
	c := counts(got)
	if len(got.Counts) != 3 {
		t.Fatalf("answer list grew to %d", len(got.Counts))
	}
	if c["yes"] != 1 {
		t.Fatalf("counts = %v", c)
	}
	// The voter who picked nothing valid is not a voter.
	if got.Voters != 1 {
		t.Fatalf("voters = %d, want 1", got.Voters)
	}
}

// The printed order must follow the poll, not map iteration, or results reshuffle
// between runs.
func TestTallyPreservesAnswerOrder(t *testing.T) {
	got := Tally(poll(1, time.Time{}), nil)
	want := []string{"yes", "no", "later"}
	for i, c := range got.Counts {
		if c.Answer.Text != want[i] {
			t.Fatalf("position %d is %q, want %q", i, c.Answer.Text, want[i])
		}
	}
}
