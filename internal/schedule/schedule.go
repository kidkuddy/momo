// Package schedule works out when a reminder fires next.
//
// It is separate from storage and from the daemon so the awkward part — catching up
// after downtime without firing the same reminder five times — can be tested as a
// pure function.
package schedule

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/kidkuddy/momo/internal/core"
)

// cronParser accepts the standard five fields, plus descriptors like @daily.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// ValidateCron reports whether an expression is usable, so a bad one is rejected when
// the reminder is created rather than silently never firing.
func ValidateCron(expr string) error {
	if _, err := cronParser.Parse(expr); err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return nil
}

// Next returns when a schedule should fire after having fired at firedAt. A zero
// time means it is finished and should be closed.
//
// The interesting case is catching up. If momo was down for three days, a daily
// reminder is three firings overdue — but the user does not want three threads, they
// want today's. So the next time is always advanced past now, skipping the missed
// occurrences rather than replaying them.
func Next(s core.Schedule, firedAt, now time.Time) (time.Time, error) {
	switch {
	case s.Cron != "":
		sched, err := cronParser.Parse(s.Cron)
		if err != nil {
			return time.Time{}, err
		}
		// Advancing from now rather than from firedAt is what skips the backlog.
		from := firedAt
		if now.After(from) {
			from = now
		}
		return sched.Next(from), nil

	case s.Every > 0:
		next := firedAt.Add(s.Every)
		// A long outage can leave this many intervals behind; jump forward instead of
		// firing once per missed interval.
		if !next.After(now) {
			missed := now.Sub(next)/s.Every + 1
			next = next.Add(missed * s.Every)
		}
		return next, nil

	default:
		return time.Time{}, nil // one-shot: done
	}
}

// ParseAt reads the time a reminder should first fire.
//
// The formats are deliberately plain and absolute. Natural language ("tomorrow at
// nine") is the agent's job — it already understands that, and knows the user's
// timezone, so momo only has to accept something unambiguous. Times are local.
func ParseAt(value string, now time.Time) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		"15:04", // today at this time, or tomorrow if it has passed
	}
	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, value, now.Location())
		if err != nil {
			continue
		}
		if layout == "15:04" {
			t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
			if !t.After(now) {
				t = t.AddDate(0, 0, 1)
			}
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("could not read %q as a time: use 2006-01-02T15:04, 15:04, or an RFC3339 timestamp", value)
}
