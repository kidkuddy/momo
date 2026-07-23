package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/kidkuddy/momo/internal/core"
)

// clearRoom starts a conversation over.
//
// Matrix has no "delete this chat". The closest honest thing is to redact every
// message momo sent, drop the local transcript, and forget the agent session so the
// next message is not a continuation.
//
// What it cannot do is remove the other person's messages: redacting someone else's
// event needs a power level momo does not have in a DM you created. Those stay until
// you remove them in your own client.
func (a *app) clearRoom(ctx context.Context, args []string) (string, error) {
	f := parseFlags(args, "local", "sessions-only", "yes")
	if len(f.rest) < 1 {
		return "", fmt.Errorf("usage: momo clear <room> [--local] [--sessions-only]")
	}
	roomID := f.rest[0]

	if f.has("sessions-only") {
		if err := a.history.ClearSessions(ctx, roomID); err != nil {
			return "", err
		}
		return "agent sessions forgotten; the next message starts a fresh conversation\n", nil
	}

	var b strings.Builder
	if !f.has("local") {
		redacted, failed := a.redactOwnMessages(ctx, roomID)
		fmt.Fprintf(&b, "redacted %d of momo's messages", redacted)
		if failed > 0 {
			fmt.Fprintf(&b, " (%d could not be redacted)", failed)
		}
		b.WriteString("\n")
	}
	if err := a.history.ClearRoom(ctx, roomID); err != nil {
		return b.String(), err
	}
	b.WriteString("local history and agent sessions cleared\n")
	b.WriteString("your own messages are untouched — momo cannot redact them, remove them in your client\n")
	return b.String(), nil
}

// redactOwnMessages redacts everything momo said in a room, working from the local
// transcript because the server will not hand back a decrypted list.
func (a *app) redactOwnMessages(ctx context.Context, roomID string) (redacted, failed int) {
	self, _, err := a.rooms.WhoAmI(ctx)
	if err != nil {
		return 0, 0
	}
	msgs, err := a.history.Messages(ctx, core.HistoryFilter{RoomID: roomID, Sender: self})
	if err != nil {
		return 0, 0
	}
	for _, m := range msgs {
		if m.Redacted {
			continue
		}
		if _, err := a.chat.Redact(ctx, roomID, m.EventID, "cleared"); err != nil {
			failed++
			continue
		}
		redacted++
	}
	return redacted, failed
}
