# momo — roadmap

A Matrix bot that is the chat UI for Claude Code. You talk to it in an encrypted DM;
it spawns an agent session in the background, which reasons and answers in the thread
by calling momo's own CLI.

Status: `[x]` done · `[ ]` planned

## Done

**Encryption.** mautrix client on pure-Go olm (`goolm` tag). E2EE send and receive
verified in a real encrypted DM. Persistent crypto store, room state survives
restarts. Cross-signing (`momo crosssign`). Server-side room key backup and restore.
Clean shutdown with WAL checkpoint.

**Repo.** `cmd/` + `internal/`, module `github.com/kidkuddy/momo`. Ports and adapters:
domain and interfaces in `internal/core`, adapters implement them, `cmd/momo` is the
only composition root. MIT, git, conventional commits.

**History.** SQLite in its own `history.db`, separate from mautrix's `momo.db`.
Messages both directions, reactions, redactions, polls and votes. Queryable by room,
thread, sender, time.

**CLI.** `daemon send upload react edit redact typing read poll endpoll poll-results
rooms join leave invite whoami history crosssign backup restore reset-session`.

**Polls.** Start, end, and read votes back. `core.Tally` implements the MSC3381
counting rules as a pure function.

**Skills.** `matrix-cli`, `matrix-events`, `matrix-e2ee`, `momo-dev`.

## Done — scheduled interruption

momo's inverted direction: something external decides you should be prompted, and
momo creates the conversation with the work already prepared.

- [x] `momo start` — post a ping, pin it, record a durable brief, run an agent in the
      background that answers in the thread. Returns the thread id immediately.
- [x] Threads carry state. `momo threads` lists what is outstanding; `momo resolve`
      closes it and settles every other open thread of the same kind, because three
      unanswered inbox reminders are one overdue task.
- [x] `--wip N` caps how many threads of a kind can pile up. Hitting it is a normal
      outcome, not an error.
- [x] Agent sessions expire after `SESSION_IDLE` (default 1h). The brief outlives
      them, so a stale thread answered tomorrow still knows its purpose.
- [x] `momo-threads` skill, installed to `~/.claude/skills` by `make install-skills`,
      so a krakoa workflow step can drive momo with no wiring.

## In progress — the agent engine

The point of the project. An incoming message spawns an agent session in the
background; the agent reasons and replies by calling the momo CLI, so anything momo
can do in Matrix, the agent can do.

- [x] **Engine interface that carries context.** `Run(ctx, Session) (Result, error)`
      where Session has the room, thread and prior session id. An engine may answer
      by returning text, or by acting through the CLI and reporting that it did.
      Claude Code is one implementation; Codex or anything else is another.
- [x] **Session continuity.** Thread root maps to an agent session id, passed back as
      `--resume`. Without it every message starts a fresh agent with no memory of the
      conversation — the single thing that makes momo feel like a mailbox.
- [x] **Daemon IPC.** The daemon owns the crypto store. A second process cannot
      safely share an olm account with it: concurrent use of the same megolm ratchet
      risks reusing a message index. So `momo send` from inside an agent session must
      forward to the running daemon over a unix socket rather than open the store
      itself, falling back to direct access when no daemon is running.
- [ ] **Secret redaction** on output before it reaches room history. Claude Code
      echoes env vars and tokens; room history is durable and, on a hosted
      homeserver, sits on someone else's disk.
- [ ] **Room to working directory binding**, so a session cannot wander the
      filesystem.
- [ ] **Concurrency cap.** The sync loop currently stalls during an engine run, which
      accidentally limits momo to one session. That stops being true the moment
      anything runs asynchronously.

## Next

- [ ] **Stream via edits.** Placeholder message, then `m.replace` as output arrives.
      Debounce to about 1/sec or the server throttles and it reads worse than no
      streaming at all.
- [ ] **Approval gates.** The plumbing exists — momo records poll starts, votes and
      closes, and can tally them. What is missing is an engine that *waits* on a
      result before acting.

## Matrix work still open, none of it blocking

- [ ] **Timeline gaps.** The server truncates long batches, so events are silently
      missed. Backfill from `prev_batch`. Only bites a busy, long-running bot.
- [ ] **SAS verification.** momo cannot be verified as a *user* by another account.
      Cosmetic: the workaround is leaving "never send to unverified sessions" off.
- [ ] **Re-verify other sessions** on the bot account after the cross-signing reset.
- [ ] **Delete stale devices.** Not scriptable on matrix.org — `DELETE /devices/{id}`
      returns `M_UNRECOGNIZED` because MAS owns device management. Browser only.

## Operations

- [x] launchd plist. **Install the binary first** (`make install` → `~/.local/bin`);
      pointing the service at the repo build output means a rebuild replaces the file
      under a starting process, which wedges it in dyld before `main()` runs. That
      failure looks like a running daemon with no socket and no log.

- [ ] Log to file with rotation
- [ ] Notice when the sync loop dies; a silent bot looks identical to an idle one
- [ ] Restore-test for real: delete `momo.db`, log in fresh, `momo restore`

## Recovering from a lost crypto store

`BOT_PASSWORD` and the recovery key are the only irreplaceable things. With both,
momo rebuilds from nothing:

    momo reset-session                  # blank token + device id
    momo whoami                         # logs in, creates a new device
    momo crosssign "<recovery key>"     # sign it against the existing identity
    momo restore "<recovery key>"       # pull room keys back down

Each rebuild leaves a dead device on the account; clean those up in your client.

## Known limits

- **Polls are unstable.** MSC3381 has no stable room version, so momo uses the
  `org.matrix.msc3381.poll.*` namespace. Element understands it today; that is not a
  promise about tomorrow.
- **Poll votes are only seen live.** Responses are encrypted and arrive over sync, so
  a vote cast while the daemon was down cannot be recovered afterwards.
- **matrix.org is not fully scriptable.** It runs MAS, so device deletion and
  cross-signing resets require a browser. Plain Synapse does not.

## Resolved, recorded so it is not re-litigated

**momo can read its own sent messages.** This was believed broken: a decrypt failure
on one of momo's own messages looked like a missing inbound copy of the outbound
megolm session. mautrix creates that copy at send time in `encryptmegolm.go` via
`createGroupSession` — a level of indirection a grep for `PutGroupSession` misses.
The original failure was caused by deleting the crypto store. Confirmed by
experiment: with the retain code removed, a freshly rotated outbound session still
gets a matching inbound row with `key_source=direct`.
