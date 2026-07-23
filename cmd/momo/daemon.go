package main

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	"github.com/kidkuddy/momo/internal/bot"
	"github.com/kidkuddy/momo/internal/core"
	"github.com/kidkuddy/momo/internal/engine"
	"github.com/kidkuddy/momo/internal/ipc"
	"github.com/kidkuddy/momo/internal/matrix"
)

func (a *app) daemon(ctx context.Context) error {
	if a.allowed == "" {
		return errors.New("ALLOWED_USER must be set: it is the only gate between a chat message and an engine run")
	}
	self, device, err := a.rooms.WhoAmI(ctx)
	if err != nil {
		return err
	}

	// An agent engine answers by shelling out to this same binary, which must not
	// open the crypto store a second time. The socket is how it comes back in.
	socket := a.profile.Socket
	eng := a.newEngine(socket)

	b := bot.New(bot.Deps{
		Chat:         a.chat,
		History:      a.history,
		Engine:       eng,
		Sessions:     a.history,
		SelfID:       self,
		Allowed:      a.allowed,
		MaxBody:      matrix.MaxBody,
		Chunk:        matrix.Chunk,
		Workdir:      workdir(),
		SentInThread: a.sends.count,
	})

	go func() {
		if err := ipc.Serve(ctx, socket, a.handleIPC); err != nil {
			log.Printf("ipc: %v", err)
		}
	}()

	a.startBackup(ctx)
	a.reportDecryptFailures()

	log.Printf("momo as %s (device %s), obeying %s, engine=%s, workdir=%s, socket=%s",
		self, device, a.allowed, eng.Name(), workdir(), socket)

	err = a.mx.Sync(ctx, matrix.Handlers{
		OnMessage: func(ctx context.Context, m core.Message) {
			b.Record(ctx, m)
			if !b.ShouldAnswer(m) {
				return
			}
			log.Printf("%s: %q", m.Sender, m.Body)
			// Runs inline, so the sync loop stalls for one engine run. That also
			// caps momo at one session at a time, which is the behaviour we want
			// until there is a real concurrency limit.
			b.Handle(ctx, m)
		},
		OnReaction: func(ctx context.Context, r core.Reaction) {
			if err := a.history.SaveReaction(ctx, r); err != nil {
				log.Printf("history: %v", err)
			}
		},
		OnRedact: func(ctx context.Context, roomID, targetID string) {
			if err := a.history.MarkRedacted(ctx, roomID, targetID); err != nil {
				log.Printf("history: %v", err)
			}
		},
		OnPoll: func(ctx context.Context, p core.PollRecord) {
			if err := a.history.SavePoll(ctx, p); err != nil {
				log.Printf("history: %v", err)
			}
		},
		OnPollVote: func(ctx context.Context, v core.PollVote) {
			if err := a.history.SavePollVote(ctx, v); err != nil {
				log.Printf("history: %v", err)
				return
			}
			log.Printf("%s voted in %s: %v", v.Sender, v.PollID, v.AnswerIDs)
		},
		OnPollEnd: func(ctx context.Context, roomID, pollID string, at time.Time) {
			if err := a.history.ClosePoll(ctx, roomID, pollID, at); err != nil {
				log.Printf("history: %v", err)
			}
		},
		// The allowlist governs invites too: anyone who can get momo into a room can
		// talk to it.
		ShouldJoin: func(inviter string) bool {
			if inviter != a.allowed {
				log.Printf("ignoring invite from %s", inviter)
				return false
			}
			return true
		},
		OnJoined: func(roomID string) { log.Printf("joined %s", roomID) },
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Print("shutting down")
	return nil
}

// handleIPC runs a command forwarded by another momo process — in practice, an agent
// session replying through the CLI.
func (a *app) handleIPC(ctx context.Context, req ipc.Request) (string, error) {
	log.Printf("ipc: %s %v", req.Command, req.Args)
	return a.runCommand(ctx, req.Command, req.Args)
}

// sendTracker counts what momo has posted per thread, so the bot can tell whether an
// agent answered for itself.
type sendTracker struct {
	mu sync.Mutex
	n  map[string]int
}

func newSendTracker() *sendTracker { return &sendTracker{n: map[string]int{}} }

func (s *sendTracker) record(threadRoot string) {
	if threadRoot == "" {
		return
	}
	s.mu.Lock()
	s.n[threadRoot]++
	s.mu.Unlock()
}

func (s *sendTracker) count(threadRoot string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n[threadRoot]
}

// newEngine picks the backend. Echo is the default on purpose: a stray run of momo
// must never be able to execute anything.
func (a *app) newEngine(socket string) core.Engine {
	if os.Getenv("ENGINE") != "claude" {
		return engine.Echo{}
	}
	self, err := os.Executable()
	if err != nil {
		self = "momo"
	}
	return engine.Claude{
		Binary:         os.Getenv("CLAUDE_BIN"),
		MomoBinary:     self,
		Workdir:        workdir(),
		Timeout:        engineTimeout(),
		SocketPath:     socket,
		PermissionMode: os.Getenv("ENGINE_PERMISSION_MODE"),
		AllowedTools:   os.Getenv("ENGINE_ALLOWED_TOOLS"),
	}
}

func workdir() string { return envOr("WORKDIR", os.Getenv("HOME")) }

func engineTimeout() time.Duration {
	if v := os.Getenv("ENGINE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 10 * time.Minute
}

// startBackup wires room key backup if one exists. Absence is not fatal: momo runs
// fine without it, it just is not durable.
func (a *app) startBackup(ctx context.Context) {
	target, err := a.mx.LoadBackupTarget(ctx)
	if err != nil {
		log.Printf("key backup unavailable: %v", err)
		return
	}
	if target == nil {
		log.Print("no room key backup; run `momo backup <recovery key>` to create one")
		return
	}
	done, failed := a.mx.BackupAll(ctx, target)
	log.Printf("key backup version %s (%d keys uploaded, %d failed)", target.Version(), done, failed)
	a.mx.WatchNewSessions(target)
}

// reportDecryptFailures says something in the room once, because silence looks
// exactly like a hang.
func (a *app) reportDecryptFailures() {
	var warned sync.Map
	a.mx.OnDecryptError(func(roomID, eventID, sender string, err error) {
		log.Printf("cannot decrypt %s in %s: %v", eventID, roomID, err)
		if sender != a.allowed {
			return
		}
		if _, seen := warned.LoadOrStore(roomID, true); seen {
			return
		}
		_, _ = a.chat.Send(context.Background(), roomID,
			"I couldn't decrypt that message — your client didn't share the room key with "+
				"my device. Check that \"Never send encrypted messages to unverified sessions\" "+
				"is off in Element (Settings → Security & Privacy), then send it again.",
			core.SendOpts{Kind: core.KindNotice})
	})
}
