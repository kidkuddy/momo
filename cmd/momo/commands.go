package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kidkuddy/momo/internal/core"
)

// flags is a tiny parser for the `--name value` / `--name` style the commands use.
// The stdlib flag package wants flags before positionals, which reads badly for
// `momo send <room> <text> --thread X`.
type flags struct {
	values map[string]string
	rest   []string
}

func parseFlags(args []string, boolFlags ...string) flags {
	isBool := map[string]bool{}
	for _, b := range boolFlags {
		isBool[b] = true
	}
	f := flags{values: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			f.rest = append(f.rest, a)
			continue
		}
		name := strings.TrimPrefix(a, "--")
		if k, v, ok := strings.Cut(name, "="); ok {
			f.values[k] = v
			continue
		}
		if isBool[name] {
			f.values[name] = "true"
			continue
		}
		if i+1 < len(args) {
			i++
			f.values[name] = args[i]
		} else {
			f.values[name] = "true"
		}
	}
	return f
}

func (f flags) get(name string) string { return f.values[name] }
func (f flags) has(name string) bool   { _, ok := f.values[name]; return ok }
func (f flags) num(name string, d int) int {
	if v, err := strconv.Atoi(f.values[name]); err == nil {
		return v
	}
	return d
}

// sendOpts builds the modifiers shared by send, upload and edit.
func (f flags) sendOpts() core.SendOpts {
	opts := core.SendOpts{
		ThreadRoot: f.get("thread"),
		ReplyTo:    f.get("reply"),
		HTML:       f.get("html"),
		Kind:       core.KindText,
	}
	switch {
	case f.has("notice"):
		opts.Kind = core.KindNotice
	case f.has("emote"):
		opts.Kind = core.KindEmote
	}
	if as := f.get("as"); as != "" {
		opts.Kind = core.Kind(as)
	}
	return opts
}

func (a *app) chatCommand(ctx context.Context, cmd string, args []string) error {
	f := parseFlags(args, "notice", "emote", "disclosed")
	pos := f.rest
	need := func(n int, form string) error {
		if len(pos) < n {
			return fmt.Errorf("usage: momo %s %s", cmd, form)
		}
		return nil
	}

	switch cmd {
	case "send":
		if err := need(2, "<room> <text>"); err != nil {
			return err
		}
		// Join the tail so an unquoted multi-word message still works.
		id, err := a.chat.Send(ctx, pos[0], strings.Join(pos[1:], " "), f.sendOpts())
		return printID(id, err)

	case "upload":
		if err := need(2, "<room> <path>"); err != nil {
			return err
		}
		id, err := a.chat.SendMedia(ctx, pos[0], pos[1], f.sendOpts())
		return printID(id, err)

	case "react":
		if err := need(3, "<room> <event> <emoji>"); err != nil {
			return err
		}
		id, err := a.chat.React(ctx, pos[0], pos[1], pos[2])
		return printID(id, err)

	case "edit":
		if err := need(3, "<room> <event> <text>"); err != nil {
			return err
		}
		id, err := a.chat.Edit(ctx, pos[0], pos[1], strings.Join(pos[2:], " "), f.sendOpts())
		return printID(id, err)

	case "redact":
		if err := need(2, "<room> <event> [reason]"); err != nil {
			return err
		}
		id, err := a.chat.Redact(ctx, pos[0], pos[1], strings.Join(pos[2:], " "))
		return printID(id, err)

	case "typing":
		if err := need(2, "<room> on|off"); err != nil {
			return err
		}
		return a.chat.Typing(ctx, pos[0], pos[1] == "on")

	case "read":
		if err := need(2, "<room> <event>"); err != nil {
			return err
		}
		return a.chat.MarkRead(ctx, pos[0], pos[1])

	case "poll":
		if err := need(4, "<room> <question> <answer> <answer>..."); err != nil {
			return err
		}
		id, err := a.chat.StartPoll(ctx, pos[0], core.Poll{
			Question:      pos[1],
			Answers:       pos[2:],
			MaxSelections: f.num("multi", 1),
			Disclosed:     f.has("disclosed"),
		})
		return printID(id, err)

	case "endpoll":
		if err := need(2, "<room> <event>"); err != nil {
			return err
		}
		id, err := a.chat.EndPoll(ctx, pos[0], pos[1])
		return printID(id, err)
	}
	return fmt.Errorf("unhandled command %q", cmd)
}

func (a *app) roomCommand(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "rooms":
		rooms, err := a.rooms.List(ctx)
		if err != nil {
			return err
		}
		for _, r := range rooms {
			label := r.Name
			if label == "" && r.DirectWith != "" {
				label = "DM with " + r.DirectWith
			}
			if label == "" {
				label = "(unnamed)"
			}
			lock := " "
			if r.Encrypted {
				lock = "e" // encrypted
			}
			fmt.Printf("%s %-48s %2d  %s\n", lock, r.ID, r.MemberCount, label)
		}
		return nil

	case "join":
		if len(args) < 1 {
			return fmt.Errorf("usage: momo join <room|alias>")
		}
		id, err := a.rooms.Join(ctx, args[0])
		return printID(id, err)

	case "leave":
		if len(args) < 1 {
			return fmt.Errorf("usage: momo leave <room>")
		}
		return a.rooms.Leave(ctx, args[0])

	case "invite":
		if len(args) < 2 {
			return fmt.Errorf("usage: momo invite <room> <user>")
		}
		return a.rooms.Invite(ctx, args[0], args[1])

	case "whoami":
		user, device, err := a.rooms.WhoAmI(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("%s (device %s), engine=%s\n", user, device, a.engine.Name())
		return nil
	}
	return fmt.Errorf("unhandled command %q", cmd)
}

func (a *app) showHistory(ctx context.Context, args []string) error {
	f := parseFlags(args)
	msgs, err := a.history.Messages(ctx, core.HistoryFilter{
		RoomID:     f.get("room"),
		ThreadRoot: f.get("thread"),
		Sender:     f.get("sender"),
		Limit:      f.num("limit", 50),
	})
	if err != nil {
		return err
	}
	// Queried newest-first for the LIMIT; printed oldest-first so it reads as a
	// transcript.
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		body := m.Body
		if m.Redacted {
			body = "(redacted)"
		}
		if m.IsMedia() {
			body = fmt.Sprintf("[%s] %s", m.Kind, m.Filename)
		}
		fmt.Printf("%s  %-28s %s\n", m.Timestamp.Format(time.RFC3339), m.Sender, body)
	}
	return nil
}

// pollResults prints a tally. Counting happens in core.Tally so the MSC3381 rules
// live somewhere testable rather than inside a print loop.
func (a *app) pollResults(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: momo poll-results <room> <poll event id>")
	}
	roomID, pollID := args[0], args[1]
	poll, err := a.history.Poll(ctx, roomID, pollID)
	if errors.Is(err, core.ErrNotFound) {
		return fmt.Errorf("no poll %s recorded in %s — the daemon must be running to see polls", pollID, roomID)
	}
	if err != nil {
		return err
	}
	votes, err := a.history.PollVotes(ctx, pollID)
	if err != nil {
		return err
	}
	t := core.Tally(poll, votes)

	status := "open"
	if !poll.Open() {
		status = "closed " + poll.ClosedAt.Format(time.RFC3339)
	}
	fmt.Printf("%s  (%s, %d voter(s))\n", t.Poll.Question, status, t.Voters)
	for _, c := range t.Counts {
		fmt.Printf("  %-24s %2d  %s\n", c.Answer.Text, c.Votes, strings.Join(c.Voters, " "))
	}
	return nil
}

func (a *app) crossSign(ctx context.Context, recoveryKey string) error {
	key, err := a.mx.CrossSign(ctx, recoveryKey, os.Getenv("BOT_PASSWORD"))
	if err != nil {
		return err
	}
	if key == "" {
		fmt.Println("device signed against the existing cross-signing identity")
		return nil
	}
	fmt.Printf("\ncross-signing set up\n\nRECOVERY KEY: %s\n\n"+
		"Store it offline now — it is not saved anywhere and cannot be shown again.\n"+
		"It also unlocks the room key backup.\n\n", key)
	return nil
}

func printID(id string, err error) error {
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}
