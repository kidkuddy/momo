package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kidkuddy/momo/internal/core"
)

// startThread opens a new piece of work: it posts a ping, records what the thread is
// for, pins it, and hands the brief to an agent session that answers in the thread.
//
// This is the inverted direction from the rest of momo. Normally a message arrives
// and momo reacts. Here something external — a schedule, a workflow — decides you
// should be interrupted, and momo has to create the conversation.
func (a *app) startThread(ctx context.Context, args []string) (string, error) {
	f := parseFlags(args, "no-pin", "no-agent")
	if len(f.rest) < 1 {
		return "", fmt.Errorf("usage: momo start <room> --kind <kind> --message <ping> [--brief <text>] [--wip N]")
	}
	roomID := f.rest[0]
	kind := f.get("kind")
	brief := f.get("brief")
	if brief == "" && f.get("brief-file") != "" {
		b, err := readFile(f.get("brief-file"))
		if err != nil {
			return "", err
		}
		brief = b
	}
	ping := f.get("message")
	if ping == "" {
		ping = strings.Join(f.rest[1:], " ")
	}
	if ping == "" {
		return "", fmt.Errorf("a thread needs an opening message: --message <text>")
	}

	// A work-in-progress limit is what stops a daily reminder becoming a wall of
	// identical unread threads. Hitting it is a normal outcome, not an error: the
	// backlog already carries the signal.
	if wip := f.num("wip", 0); wip > 0 && kind != "" {
		open, err := a.history.OpenThreads(ctx, roomID, kind)
		if err != nil {
			return "", err
		}
		if len(open) >= wip {
			return fmt.Sprintf("skipped: %d open %q thread(s), at the limit of %d\n%s\n",
				len(open), kind, wip, open[0].ThreadRoot), nil
		}
	}

	// Deliberately not recorded as a send *into* the thread: this message is the
	// thread root, not a reply in it. Counting it would make runBrief believe the
	// agent had already answered, and suppress the fallback that catches a silent
	// session.
	eventID, err := a.chat.Send(ctx, roomID, ping, core.SendOpts{Kind: core.KindNotice})
	if err != nil {
		return "", err
	}

	if err := a.history.CreateThread(ctx, core.Thread{
		RoomID:     roomID,
		ThreadRoot: eventID,
		Kind:       kind,
		Brief:      brief,
		State:      core.ThreadOpen,
		CreatedAt:  time.Now(),
	}); err != nil {
		return "", err
	}

	var out strings.Builder
	out.WriteString(eventID + "\n")

	if !f.has("no-pin") {
		if err := a.mx.Pin(ctx, roomID, eventID); err != nil {
			// Pinning needs a power level momo may not have in a group room. Losing
			// the pin is cosmetic; losing the thread would not be.
			fmt.Fprintf(&out, "note: could not pin (%v)\n", err)
		}
	}

	// The agent prepares the work in the background. A scheduled trigger must not
	// block for the minutes an agent takes — krakoa wants the thread id back now,
	// and the user sees the ping immediately and the prepared work when it lands.
	//
	// ctx here is the daemon's, not the connection's, so this outlives the caller.
	if !f.has("no-agent") && brief != "" {
		go a.runBrief(ctx, roomID, eventID, brief)
	}
	return out.String(), nil
}

// runBrief hands a thread's brief to the engine and lets it answer in place.
func (a *app) runBrief(ctx context.Context, roomID, threadRoot, brief string) {
	eng := a.newEngine(a.profile.Socket)
	answer, err := eng.Run(ctx, core.Task{
		Prompt:     brief,
		RoomID:     roomID,
		ThreadRoot: threadRoot,
		Workdir:    workdir(),
	})
	if err != nil {
		_, _ = a.chat.Send(ctx, roomID, "Could not prepare this: "+err.Error(),
			core.SendOpts{ThreadRoot: threadRoot, Kind: core.KindNotice})
		return
	}
	if answer.SessionID != "" {
		_ = a.history.SetSession(ctx, roomID, threadRoot, answer.SessionID)
	}
	// If the agent replied through the CLI the thread already has its content, and
	// posting the transcript too would say everything twice.
	if a.sends.count(threadRoot) > 0 {
		return
	}
	reply := strings.TrimSpace(answer.Reply)
	if reply == "" {
		reply = "I prepared this but did not manage to say anything — check the daemon log."
	}
	_, _ = a.chat.Send(ctx, roomID, reply, core.SendOpts{ThreadRoot: threadRoot})
}

// resolveThread marks work done. By default it also settles every other open thread
// of the same kind, because three unanswered inbox reminders are one overdue task.
func (a *app) resolveThread(ctx context.Context, args []string) (string, error) {
	f := parseFlags(args, "only", "keep-pin")
	if len(f.rest) < 2 {
		return "", fmt.Errorf("usage: momo resolve <room> <thread> [--only] [--keep-pin]")
	}
	roomID, threadRoot := f.rest[0], f.rest[1]

	thread, err := a.history.Thread(ctx, roomID, threadRoot)
	if errors.Is(err, core.ErrNotFound) {
		return "", fmt.Errorf("no thread %s recorded in %s", threadRoot, roomID)
	}
	if err != nil {
		return "", err
	}

	// Unpin the whole kind before closing, since superseded threads lose their
	// records of being pinned.
	if !f.has("keep-pin") {
		a.unpinKind(ctx, roomID, thread.Kind, threadRoot, f.has("only"))
	}

	closed, err := a.history.SetThreadState(ctx, roomID, threadRoot, core.ThreadResolved, !f.has("only"))
	if err != nil {
		return "", err
	}
	if closed > 1 {
		return fmt.Sprintf("resolved; %d older %q thread(s) superseded — same task, done late\n",
			closed-1, thread.Kind), nil
	}
	return "resolved\n", nil
}

func (a *app) unpinKind(ctx context.Context, roomID, kind, threadRoot string, onlyThis bool) {
	_ = a.mx.Unpin(ctx, roomID, threadRoot)
	if onlyThis || kind == "" {
		return
	}
	open, err := a.history.OpenThreads(ctx, roomID, kind)
	if err != nil {
		return
	}
	for _, t := range open {
		_ = a.mx.Unpin(ctx, roomID, t.ThreadRoot)
	}
}

// listThreads shows what is outstanding — the backlog of things momo has asked for
// that you have not dealt with.
func (a *app) listThreads(ctx context.Context, args []string) (string, error) {
	f := parseFlags(args)
	threads, err := a.history.OpenThreads(ctx, f.get("room"), f.get("kind"))
	if err != nil {
		return "", err
	}
	if len(threads) == 0 {
		return "nothing open\n", nil
	}
	var b strings.Builder
	for _, t := range threads {
		age := time.Since(t.CreatedAt).Round(time.Minute)
		fmt.Fprintf(&b, "%-12s %-46s %8s ago  %s\n",
			t.Kind, t.ThreadRoot, age, firstLine(t.Brief))
	}
	return b.String(), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return s
}
