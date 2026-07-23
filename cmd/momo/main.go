// Command momo is a Matrix bot that acts as the chat UI for Claude Code, and the CLI
// for driving that same Matrix account by hand or from inside an agent session.
//
// This file is the composition root: the only place that knows which concrete
// adapter satisfies which port. Everything below internal/ depends on interfaces.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/kidkuddy/momo/internal/config"
	"github.com/kidkuddy/momo/internal/core"
	"github.com/kidkuddy/momo/internal/ipc"
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
  momo poll-results <room> <event>     tally votes (needs the daemon to have seen them)
  momo clear <room>                    redact momo's messages, wipe local history and sessions
                                       [--local] keep the room, wipe locally only
                                       [--sessions-only] forget sessions, keep the transcript
  momo start <room> --message <ping>   open a piece of work: ping, pin, run a brief
                                       [--kind K] [--brief T|--brief-file P] [--wip N]
  momo threads [--kind K] [--room R]   what is still outstanding
  momo resolve <room> <thread>         mark done; also settles older threads of the
                                       same kind [--only] [--keep-pin]
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
  momo profiles                        list configured bots

Run any command as a particular bot with --profile <name>, or MOMO_PROFILE. A profile
is a directory under ~/.momo holding one bot's credentials, crypto store, history and
socket. Without one, momo uses the files in the working directory.

While the daemon is running, commands are forwarded to it over MOMO_SOCKET rather
than opening the crypto store a second time, which two processes cannot safely share.

Environment: HOMESERVER BOT_USER BOT_PASSWORD ALLOWED_USER ENGINE WORKDIR DEBUG
             ENGINE_TIMEOUT MOMO_SOCKET STATE_FILE CRYPTO_DB HISTORY_DB
`

// forwardable are the commands a running daemon can execute on a caller's behalf.
// Setup and recovery commands are not: they change state the daemon has already
// loaded, so they need it stopped.
var forwardable = map[string]bool{
	"send": true, "upload": true, "react": true, "edit": true, "redact": true,
	"typing": true, "read": true, "poll": true, "endpoll": true,
	"poll-results": true, "rooms": true, "join": true, "leave": true,
	"invite": true, "whoami": true, "history": true, "clear": true,
	"start": true, "resolve": true, "threads": true,
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return
	}
	// --profile selects which bot to act as, and must be resolved before anything
	// reads a path or a credential.
	profileName, args := takeProfile(args)
	if len(args) == 0 {
		fmt.Print(usage)
		return
	}
	cmd, rest := args[0], args[1:]

	if cmd == "profiles" {
		names, err := config.List()
		fail(err)
		if len(names) == 0 {
			fmt.Printf("no profiles yet — create one with:\n  mkdir -p %s/<name> && $EDITOR %s/<name>/config\n",
				config.Root(), config.Root())
			return
		}
		for _, n := range names {
			fmt.Println(n)
		}
		return
	}

	profile, err := config.Load(profileName)
	fail(err)

	// reset-session touches only the state file, so it must work even when the
	// client cannot start — which is exactly when it is needed.
	if cmd == "reset-session" {
		fail(matrix.ResetSession(profile.State))
		fmt.Println("token and device cleared; next run logs in fresh")
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Try the daemon first. This is how an agent session replies: it runs this same
	// binary, which forwards to the process that owns the olm account.
	if cmd != "daemon" && forwardable[cmd] {
		out, err := ipc.Send(profile.Socket, ipc.Request{Command: cmd, Args: rest})
		if !errors.Is(err, ipc.ErrNoDaemon) {
			if out != "" {
				fmt.Print(out)
			}
			fail(err)
			return
		}
		// No daemon: fall through and open the store directly, which is safe
		// because nothing else holds it.
	}

	// Claim the socket before the slow work of opening the crypto store, so a CLI
	// command that arrives during startup waits for us instead of falling back to
	// opening the store itself and contending with a daemon that is still booting.
	var ln net.Listener
	if cmd == "daemon" {
		ln, err = ipc.Listen(profile.Socket)
		fail(err)
	}

	app, err := open(ctx, profile)
	fail(err)
	defer app.Close()

	if cmd == "daemon" {
		fail(app.daemon(ctx, ln))
		return
	}
	out, err := app.runCommand(ctx, cmd, rest)
	if out != "" {
		fmt.Print(out)
	}
	fail(err)
}

// app holds the wired dependencies. Concrete types here, interfaces everywhere else.
type app struct {
	profile *config.Profile
	mx      *matrix.Client
	chat    core.Chat
	rooms   core.Rooms
	history *store.Store
	allowed string
	sends   *sendTracker
}

func open(ctx context.Context, profile *config.Profile) (*app, error) {
	mx, err := matrix.New(ctx, matrix.Config{
		Homeserver: strings.TrimRight(os.Getenv("HOMESERVER"), "/"),
		User:       os.Getenv("BOT_USER"),
		Password:   os.Getenv("BOT_PASSWORD"),
		StatePath:  profile.State,
		CryptoPath: profile.Crypto,
		Debug:      os.Getenv("DEBUG") != "",
	})
	if err != nil {
		return nil, err
	}
	hist, err := store.Open(profile.History)
	if err != nil {
		mx.Close()
		return nil, err
	}
	return &app{
		profile: profile,
		mx:      mx,
		chat:    matrix.NewChat(mx),
		rooms:   matrix.NewRooms(mx),
		history: hist,
		allowed: os.Getenv("ALLOWED_USER"),
		sends:   newSendTracker(),
	}, nil
}

func (a *app) Close() {
	a.history.Close()
	a.mx.Close()
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

// takeProfile pulls --profile out of the argument list wherever it appears, so it
// can be given before or after the subcommand.
func takeProfile(args []string) (string, []string) {
	name := os.Getenv("MOMO_PROFILE")
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--profile" && i+1 < len(args):
			name = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--profile="):
			name = strings.TrimPrefix(args[i], "--profile=")
		default:
			rest = append(rest, args[i])
		}
	}
	return name, rest
}
