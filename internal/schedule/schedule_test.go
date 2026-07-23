package schedule

import (
	"testing"
	"time"

	"github.com/kidkuddy/momo/internal/core"
)

var at9 = time.Date(2026, 7, 23, 9, 0, 0, 0, time.Local)

// A one-shot must close, or it fires forever.
func TestOneShotIsDone(t *testing.T) {
	next, err := Next(core.Schedule{}, at9, at9)
	if err != nil {
		t.Fatal(err)
	}
	if !next.IsZero() {
		t.Fatalf("next = %v, want zero so it closes", next)
	}
}

func TestIntervalAdvancesOnce(t *testing.T) {
	s := core.Schedule{Every: 24 * time.Hour}
	next, err := Next(s, at9, at9.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if want := at9.Add(24 * time.Hour); !next.Equal(want) {
		t.Fatalf("next = %v, want %v", next, want)
	}
}

// The case that matters: momo was down for days. A daily reminder is several firings
// overdue, but the user wants today's, not one thread per missed day.
func TestIntervalSkipsMissedOccurrences(t *testing.T) {
	s := core.Schedule{Every: 24 * time.Hour}
	now := at9.Add(72*time.Hour + time.Minute) // three days later

	next, err := Next(s, at9, now)
	if err != nil {
		t.Fatal(err)
	}
	if !next.After(now) {
		t.Fatalf("next = %v is not after now = %v; it would fire immediately again", next, now)
	}
	if want := at9.Add(96 * time.Hour); !next.Equal(want) {
		t.Fatalf("next = %v, want %v — exactly one interval past now", next, want)
	}
}

func TestCronAdvances(t *testing.T) {
	s := core.Schedule{Cron: "0 9 * * *"} // every day at 09:00
	next, err := Next(s, at9, at9.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if want := at9.AddDate(0, 0, 1); !next.Equal(want) {
		t.Fatalf("next = %v, want %v", next, want)
	}
}

func TestCronSkipsMissedOccurrences(t *testing.T) {
	s := core.Schedule{Cron: "0 9 * * *"}
	now := at9.Add(72*time.Hour + time.Minute)

	next, err := Next(s, at9, now)
	if err != nil {
		t.Fatal(err)
	}
	if !next.After(now) {
		t.Fatalf("next = %v is not after now = %v", next, now)
	}
	if want := at9.AddDate(0, 0, 4); !next.Equal(want) {
		t.Fatalf("next = %v, want %v", next, want)
	}
}

// A bad expression must be rejected when the reminder is created. Accepting it would
// produce a reminder that silently never fires — worse than an error.
func TestValidateCron(t *testing.T) {
	for _, ok := range []string{"0 9 * * *", "*/15 * * * *", "@daily", "0 9 * * MON-FRI"} {
		if err := ValidateCron(ok); err != nil {
			t.Errorf("rejected valid expression %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "not a cron", "99 * * * *", "0 9 * *"} {
		if err := ValidateCron(bad); err == nil {
			t.Errorf("accepted invalid expression %q", bad)
		}
	}
}

func TestParseAt(t *testing.T) {
	now := time.Date(2026, 7, 23, 14, 30, 0, 0, time.Local)

	t.Run("absolute formats", func(t *testing.T) {
		for _, in := range []string{
			"2026-07-24T09:00", "2026-07-24 09:00", "2026-07-24T09:00:00",
		} {
			got, err := ParseAt(in, now)
			if err != nil {
				t.Fatalf("%q: %v", in, err)
			}
			want := time.Date(2026, 7, 24, 9, 0, 0, 0, time.Local)
			if !got.Equal(want) {
				t.Errorf("%q gave %v, want %v", in, got, want)
			}
		}
	})

	// A bare time later today means today; one already past means tomorrow, because
	// nobody asking at 14:30 for "09:00" means nine hours ago.
	t.Run("bare time rolls forward", func(t *testing.T) {
		got, err := ParseAt("18:00", now)
		if err != nil {
			t.Fatal(err)
		}
		if want := time.Date(2026, 7, 23, 18, 0, 0, 0, time.Local); !got.Equal(want) {
			t.Errorf("got %v, want today at 18:00", got)
		}

		got, err = ParseAt("09:00", now)
		if err != nil {
			t.Fatal(err)
		}
		if want := time.Date(2026, 7, 24, 9, 0, 0, 0, time.Local); !got.Equal(want) {
			t.Errorf("got %v, want tomorrow at 09:00", got)
		}
	})

	t.Run("nonsense is rejected", func(t *testing.T) {
		if _, err := ParseAt("next tuesday", now); err == nil {
			t.Error("accepted natural language; that is the agent's job, not momo's")
		}
	})
}
