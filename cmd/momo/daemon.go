package main

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/kidkuddy/momo/internal/bot"
	"github.com/kidkuddy/momo/internal/core"
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

	b := bot.New(bot.Deps{
		Chat:    a.chat,
		Rooms:   a.rooms,
		History: a.history,
		Engine:  a.engine,
		SelfID:  self,
		Allowed: a.allowed,
		MaxBody: matrix.MaxBody,
		Chunk:   matrix.Chunk,
	})

	a.startBackup(ctx)
	a.reportDecryptFailures(self)

	log.Printf("momo as %s (device %s), obeying %s, engine=%s", self, device, a.allowed, a.engine.Name())

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
		// The allowlist governs invites too: anyone who can get momo into a room
		// can talk to it.
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
	done, failed := a.mx.BackupAll(ctx, target) // catch up on anything missed
	log.Printf("key backup version %s (%d keys uploaded, %d failed)", target.Version(), done, failed)
	a.mx.WatchNewSessions(target)
}

// reportDecryptFailures says something in the room once, because silence looks
// exactly like a hang.
func (a *app) reportDecryptFailures(self string) {
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
