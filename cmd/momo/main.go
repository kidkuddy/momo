// Command momo is a Matrix bot that acts as the chat UI for Claude Code sessions,
// and the CLI for driving that same Matrix account by hand.
//
// This file is the composition root: the only place that knows which concrete
// adapter satisfies which port. Everything below internal/ depends on interfaces.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kidkuddy/momo/internal/core"
	"github.com/kidkuddy/momo/internal/engine"
	"github.com/kidkuddy/momo/internal/matrix"
	"github.com/kidkuddy/momo/internal/store"
)

const usage = `momo — Matrix bot and CLI

  momo daemon                          run the bot
  momo send <room> <text>              [--thread ID] [--reply ID] [--notice] [--emote] [--html S]
  momo upload <room> <path>            [--thread ID] [--as image|audio|video|file]
  momo react <room> <event> <emoji>
  momo edit <room> <event> <text>
  momo redact <room> <event> [reason]
  momo typing <room> on|off
  momo read <room> <event>
  momo poll <room> <question> <answer>... [--multi N] [--disclosed]
  momo endpoll <room> <event>
  momo rooms                           list joined rooms
  momo join <room|alias>
  momo leave <room>
  momo invite <room> <user>
  momo whoami
  momo history [--room ID] [--thread ID] [--sender U] [--limit N]
  momo crosssign [recovery key]        sign momo's own device
  momo backup <recovery key>           create the room key backup
  momo restore <recovery key>          pull room keys back down
  momo reset-session                   forget token+device, forcing a fresh login

Environment: HOMESERVER BOT_USER BOT_PASSWORD ALLOWED_USER ENGINE WORKDIR DEBUG
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return
	}
	cmd, rest := args[0], args[1:]

	// reset-session touches only the state file, so it must work even when the
	// client cannot start — which is exactly when it is needed.
	if cmd == "reset-session" {
		fail(matrix.ResetSession(envOr("STATE_FILE", "state.json")))
		fmt.Println("token and device cleared; next run logs in fresh")
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := open(ctx)
	fail(err)
	defer app.Close()

	switch cmd {
	case "daemon":
		fail(app.daemon(ctx))
	case "send", "upload", "react", "edit", "redact", "typing", "read", "poll", "endpoll":
		fail(app.chatCommand(ctx, cmd, rest))
	case "rooms", "join", "leave", "invite", "whoami":
		fail(app.roomCommand(ctx, cmd, rest))
	case "history":
		fail(app.showHistory(ctx, rest))
	case "crosssign":
		fail(app.crossSign(ctx, strings.Join(rest, " ")))
	case "backup":
		v, err := app.mx.SetupBackup(ctx, strings.Join(rest, " "))
		fail(err)
		fmt.Printf("room key backup version %s created; new keys upload as they arrive\n", v)
	case "restore":
		v, err := app.mx.RestoreBackup(ctx, strings.Join(rest, " "))
		fail(err)
		fmt.Printf("restored room keys from backup version %s\n", v)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

// app holds the wired dependencies. Concrete types here, interfaces everywhere else.
type app struct {
	mx      *matrix.Client
	chat    core.Chat
	rooms   core.Rooms
	history core.History
	engine  core.Engine
	allowed string
}

func open(ctx context.Context) (*app, error) {
	mx, err := matrix.New(ctx, matrix.Config{
		Homeserver: strings.TrimRight(os.Getenv("HOMESERVER"), "/"),
		User:       os.Getenv("BOT_USER"),
		Password:   os.Getenv("BOT_PASSWORD"),
		StatePath:  envOr("STATE_FILE", "state.json"),
		CryptoPath: envOr("CRYPTO_DB", "momo.db"),
		Debug:      os.Getenv("DEBUG") != "",
	})
	if err != nil {
		return nil, err
	}
	hist, err := store.Open(envOr("HISTORY_DB", "history.db"))
	if err != nil {
		mx.Close()
		return nil, err
	}
	return &app{
		mx:      mx,
		chat:    matrix.NewChat(mx),
		rooms:   matrix.NewRooms(mx),
		history: hist,
		engine:  newEngine(),
		allowed: os.Getenv("ALLOWED_USER"),
	}, nil
}

func (a *app) Close() {
	a.history.Close()
	a.mx.Close()
}

// newEngine defaults to echo on purpose: a stray run of momo must never be able to
// execute anything. ENGINE=claude is the explicit opt-in.
func newEngine() core.Engine {
	if os.Getenv("ENGINE") != "claude" {
		return engine.Echo{}
	}
	return engine.Claude{
		Workdir: envOr("WORKDIR", os.Getenv("HOME")),
		Timeout: 10 * time.Minute,
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
