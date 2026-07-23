# momo — roadmap

A Matrix bot that is the chat UI for Claude Code sessions. You talk to it in an
encrypted DM; it runs Claude Code and answers in the thread.

Status legend: `[x]` done · `[ ]` planned · `[~]` partially done

## Phase 0 — encryption (done)

- [x] mautrix-based client, pure-Go olm (`goolm` build tag)
- [x] E2EE send + receive, verified in a real encrypted DM
- [x] Persistent crypto store, room state survives restarts
- [x] Cross-signing, own device signed (`momo crosssign`)
- [x] Server-side room key backup + restore (`momo backup` / `momo restore`)
- [x] Clean shutdown, WAL checkpointed

## Phase 1 — repo (this pass)

- [x] Go layout: `cmd/` + `internal/`, module `github.com/kidkuddy/momo`
- [x] Clean architecture: domain ports in `internal/core`, adapters implement them
- [x] MIT license, README, git repo, conventional commits
- [x] Scrub personal identifiers from docs before going public

## Phase 2 — history (this pass)

- [x] SQLite message history in its own `history.db`, separate from mautrix's `momo.db`
- [x] Records both directions, including momo's own sends
- [x] Reactions and redactions tracked, not just messages
- [x] Query by room, thread, sender, time

## Phase 3 — own-message readability (investigated, no change needed)

- [x] Verified that momo can already read back its own messages

  This was on the list because a decrypt failure on one of momo's own messages
  looked like a missing inbound copy of the outbound megolm session. It was not.
  mautrix creates that copy at send time in `encryptmegolm.go`, via
  `createGroupSession` — a level of indirection that a grep for `PutGroupSession`
  in that file misses.

  The original failure had a duller cause: the crypto store had been deleted, which
  destroyed the session along with everything else. Confirmed by experiment — with
  our own retain code removed, a freshly rotated outbound session still gets a
  matching inbound row with `key_source=direct`. The code written for this was
  deleted rather than shipped.

## Phase 4 — CLI (this pass)

Every Matrix action momo can take is a subcommand, so Claude Code drives it through
skills rather than through bespoke glue.

- [x] `daemon` — the bot
- [x] `send` — text/html/notice/emote, `--thread`, `--reply`
- [x] `upload` — file/image/video/audio, encrypted in encrypted rooms
- [x] `react` `edit` `redact` — annotate and amend
- [x] `typing` `read` — presence and receipts
- [x] `rooms` `join` `leave` `invite` `whoami` — room management
- [x] `history` — read back from SQLite
- [x] `poll` / `endpoll` — MSC3381, unstable namespace
- [x] `poll-results` — reads votes back and tallies them

## Phase 5 — skills (this pass)

- [x] `matrix-cli` — driving momo from Claude Code
- [x] `matrix-events` — the event model: threads, edits, reactions, redactions
- [x] `matrix-e2ee` — crypto operations, recovery, the failure modes
- [x] `momo-dev` — repo architecture

## Phase 6 — the actual point (next)

The Matrix side is a means to an end. None of this is built yet.

- [ ] **Session continuity.** Map a thread to a Claude Code session id and pass
      `--resume`. Without it every message starts a fresh agent, which is what makes
      momo feel like a mailbox instead of a conversation. Highest value item left.
- [ ] **Stream via edits.** Send a placeholder, then `m.replace` as output arrives.
      Debounce to ~1/sec or the server throttles and it reads worse than no streaming.
- [ ] **Approval gates.** The plumbing is in place: momo records poll starts, votes
      and closes, and `core.Tally` counts them per MSC3381. What is missing is an
      engine that *waits* on a result before acting.
- [ ] **Bind rooms to working directories** so a session cannot wander the filesystem.
- [ ] **Redact secrets** from engine output before it reaches room history.
- [ ] **Cap concurrent sessions.** One runaway loop should not spawn twenty agents.

## Phase 7 — operations

- [ ] launchd plist so momo survives reboot (SIGTERM, not SIGKILL — it checkpoints)
- [ ] Log to file with rotation
- [ ] Notice when the sync loop dies; a silent bot looks identical to an idle one
- [ ] Handle timeline gaps — backfill from `prev_batch` when the server truncates

## Recovering from a lost crypto store

`BOT_PASSWORD` and the recovery key are the only irreplaceable things. With both,
momo rebuilds from nothing:

    momo reset-session                  # blank token + device id
    momo whoami                         # logs in, creates a new device
    momo crosssign "<recovery key>"     # sign it against the existing identity
    momo restore "<recovery key>"       # pull room keys back down

Each rebuild leaves a dead device on the account; clean those up in your client.

- [ ] Restore-test this for real once. Every step is exercised, but never against an
      actually empty store.

## Known limits

- **Polls are unstable.** MSC3381 has no stable room version, so momo uses the
  `org.matrix.msc3381.poll.*` namespace. Element understands it today; that is not a
  promise about tomorrow.
- **No SAS verification.** momo cannot be verified as a *user* by another account, so
  "Never send encrypted messages to unverified sessions" must stay off in your client.
- **matrix.org is not fully scriptable.** It runs MAS, so device deletion and
  cross-signing resets require a browser. Plain Synapse does not.
