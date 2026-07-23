package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kidkuddy/momo/internal/core"
)

// nudgeThreads pushes on work that is still open.
//
// It posts *into* the existing thread rather than opening a new one. A second ping
// about the same thing is how a reminder system becomes wallpaper — the whole point
// of thread state is that there is one place per piece of work.
//
// The agent gets the original brief plus what has been said so far, and is asked to
// argue for finishing rather than to repeat itself. A reminder that only restates the
// task is the thing that already failed.
func (a *app) nudgeThreads(ctx context.Context, args []string) (string, error) {
	f := parseFlags(args, "dry-run")
	older := duration(f.get("older-than"), 12*time.Hour)
	// A daily sweep run twice must not nag twice, and two sweeps in a day are more
	// likely a mistake than an intent.
	interval := duration(f.get("min-interval"), 20*time.Hour)

	threads, err := a.history.OpenThreads(ctx, f.get("room"), f.get("kind"))
	if err != nil {
		return "", err
	}

	var b strings.Builder
	nudged := 0
	for _, t := range threads {
		// A kind marks a ritual momo is responsible for chasing. A thread without
		// one is a conversation the user opened; pinned so it is findable, but
		// nagging about a question they asked once would be obnoxious.
		if t.Kind == "" && f.get("kind") == "" {
			continue
		}
		age := time.Since(t.CreatedAt)
		if age < older {
			continue
		}
		if !t.NudgedAt.IsZero() && time.Since(t.NudgedAt) < interval {
			continue
		}
		fmt.Fprintf(&b, "%s %s (open %s)\n", t.Kind, t.ThreadRoot, age.Round(time.Hour))
		if f.has("dry-run") {
			continue
		}
		if err := a.history.MarkNudged(ctx, t.RoomID, t.ThreadRoot, time.Now()); err != nil {
			return b.String(), err
		}
		go a.runBrief(ctx, t.RoomID, t.ThreadRoot, a.nudgeBrief(ctx, t))
		nudged++
	}
	if nudged == 0 && b.Len() == 0 {
		return "nothing to nudge\n", nil
	}
	fmt.Fprintf(&b, "nudged %d\n", nudged)
	return b.String(), nil
}

// nudgeBrief builds the prompt for a push. It carries the original brief so the
// agent knows what the thread is for, and the conversation so far so it can pick up
// where it left off instead of starting the pitch over.
func (a *app) nudgeBrief(ctx context.Context, t core.Thread) string {
	var b strings.Builder
	fmt.Fprintf(&b, `This thread has been open for %s and the user has not finished it.

Push on it. Do not repeat the original message — they have already read it and it did
not work. Instead: look at what is blocking it, make the next step smaller, or ask the
one question that would unstick it. Be short and direct, one message.

If the task looks malformed rather than merely postponed — too big, too vague, or
depending on something that has not happened — say so and propose shrinking or
dropping it. Carrying an impossible task forward is worse than closing it.

What this thread was for:

%s`, time.Since(t.CreatedAt).Round(time.Hour), t.Brief)

	msgs, err := a.history.Messages(ctx, core.HistoryFilter{
		RoomID: t.RoomID, ThreadRoot: t.ThreadRoot, Limit: 12,
	})
	if err == nil && len(msgs) > 0 {
		b.WriteString("\n\nWhat has been said so far, oldest first:\n")
		for i := len(msgs) - 1; i >= 0; i-- {
			fmt.Fprintf(&b, "\n[%s] %s", msgs[i].Sender, msgs[i].Body)
		}
	}
	return b.String()
}

func duration(s string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return fallback
}
