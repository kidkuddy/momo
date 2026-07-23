package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/kidkuddy/momo/internal/core"
	"github.com/kidkuddy/momo/internal/schedule"
)

// scheduleCommand manages reminders the user asked for in chat.
//
// momo takes only absolute times and cron expressions. Reading "tomorrow after lunch"
// is the agent's job: it is already a language model, it knows today's date and the
// user's timezone, and a natural-language parser in Go would be worse at it and would
// fail silently. The agent translates, momo schedules.
func (a *app) scheduleCommand(ctx context.Context, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: momo schedule add|list|rm …")
	}
	switch args[0] {
	case "add":
		return a.scheduleAdd(ctx, args[1:])
	case "list", "ls":
		return a.scheduleList(ctx)
	case "rm", "cancel", "del":
		return a.scheduleRemove(ctx, args[1:])
	}
	return "", fmt.Errorf("unknown schedule command %q (add, list, rm)", args[0])
}

func (a *app) scheduleAdd(ctx context.Context, args []string) (string, error) {
	f := parseFlags(args)
	now := time.Now()

	message := f.get("message")
	if message == "" {
		message = strings.Join(f.rest, " ")
	}
	if message == "" {
		return "", fmt.Errorf("a reminder needs --message: what the user will see")
	}

	roomID := f.get("room")
	if roomID == "" {
		var err error
		if roomID, err = a.defaultRoom(ctx); err != nil {
			return "", err
		}
	}

	brief := f.get("brief")
	if brief == "" && f.get("brief-file") != "" {
		b, err := readFile(f.get("brief-file"))
		if err != nil {
			return "", err
		}
		brief = b
	}

	cronExpr := f.get("cron")
	if cronExpr != "" {
		// Reject a bad expression now. Accepting it would create a reminder that
		// silently never fires, which is worse than an error.
		if err := schedule.ValidateCron(cronExpr); err != nil {
			return "", err
		}
	}
	var every time.Duration
	if v := f.get("every"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return "", fmt.Errorf("--every wants a duration like 24h or 90m, got %q", v)
		}
		every = d
	}

	first, err := a.firstFire(f, cronExpr, now)
	if err != nil {
		return "", err
	}

	id, err := a.history.AddSchedule(ctx, core.Schedule{
		RoomID:  roomID,
		Kind:    f.get("kind"),
		Message: message,
		Brief:   brief,
		NextAt:  first,
		Every:   every,
		Cron:    cronExpr,
		WIP:     f.num("wip", 0),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d  first at %s%s\n", id, first.Format("Mon 2 Jan 15:04"),
		repeatSuffix(every, cronExpr)), nil
}

// firstFire works out when a reminder should first go off.
func (a *app) firstFire(f flags, cronExpr string, now time.Time) (time.Time, error) {
	if v := f.get("in"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return time.Time{}, fmt.Errorf("--in wants a duration like 30m or 2h, got %q", v)
		}
		return now.Add(d), nil
	}
	if v := f.get("at"); v != "" {
		t, err := schedule.ParseAt(v, now)
		if err != nil {
			return time.Time{}, err
		}
		if t.Before(now) {
			return time.Time{}, fmt.Errorf("%s is in the past", t.Format(time.RFC3339))
		}
		return t, nil
	}
	if cronExpr != "" {
		// No explicit start: the expression itself says when.
		next, err := schedule.Next(core.Schedule{Cron: cronExpr}, now, now)
		if err != nil {
			return time.Time{}, err
		}
		return next, nil
	}
	return time.Time{}, fmt.Errorf("when should this fire? give --at <time>, --in <duration>, or --cron <expr>")
}

func repeatSuffix(every time.Duration, cronExpr string) string {
	switch {
	case cronExpr != "":
		return ", repeating on " + cronExpr
	case every > 0:
		return ", repeating every " + every.String()
	default:
		return " (once)"
	}
}

func (a *app) scheduleList(ctx context.Context) (string, error) {
	list, err := a.history.ListSchedules(ctx)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "no reminders set\n", nil
	}
	var b strings.Builder
	for _, s := range list {
		repeat := "once"
		switch {
		case s.Cron != "":
			repeat = s.Cron
		case s.Every > 0:
			repeat = "every " + s.Every.String()
		}
		fmt.Fprintf(&b, "%-4d %-18s %-16s %s\n",
			s.ID, s.NextAt.Format("Mon 2 Jan 15:04"), repeat, firstLine(s.Message))
	}
	return b.String(), nil
}

func (a *app) scheduleRemove(ctx context.Context, args []string) (string, error) {
	if len(args) < 1 {
		return "", fmt.Errorf("usage: momo schedule rm <id>")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("%q is not a reminder id; momo schedule list shows them", args[0])
	}
	if err := a.history.CancelSchedule(ctx, id); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return "", fmt.Errorf("no active reminder %d", id)
		}
		return "", err
	}
	return "cancelled\n", nil
}

// runSchedules fires due reminders until ctx is cancelled.
//
// It checks on a tick rather than sleeping until the next one so that a reminder
// added while the daemon is running is picked up without waking anything, and so a
// missed tick is simply late rather than lost.
func (a *app) runSchedules(ctx context.Context) {
	const tick = 30 * time.Second
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.fireDue(ctx)
		}
	}
}

func (a *app) fireDue(ctx context.Context) {
	now := time.Now()
	due, err := a.history.DueSchedules(ctx, now)
	if err != nil {
		log.Printf("schedules: %v", err)
		return
	}
	for _, s := range due {
		next, err := schedule.Next(s, s.NextAt, now)
		if err != nil {
			log.Printf("schedule %d: %v", s.ID, err)
			continue
		}
		// Advance before firing. A crash mid-thread then loses one reminder rather
		// than looping on it forever, which is the safer of the two failures.
		if err := a.history.AdvanceSchedule(ctx, s.ID, now, next); err != nil {
			log.Printf("schedule %d: %v", s.ID, err)
			continue
		}
		log.Printf("firing reminder %d: %q", s.ID, firstLine(s.Message))

		args := []string{"--room", s.RoomID, "--message", s.Message}
		if s.Kind != "" {
			args = append(args, "--kind", s.Kind)
		}
		if s.Brief != "" {
			args = append(args, "--brief", s.Brief)
		}
		if s.WIP > 0 {
			args = append(args, "--wip", strconv.Itoa(s.WIP))
		}
		if _, err := a.startThread(ctx, args); err != nil {
			log.Printf("schedule %d: %v", s.ID, err)
		}
	}
}
