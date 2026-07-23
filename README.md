# momo

A Matrix bot that is the chat UI for [Claude Code](https://claude.com/claude-code).
DM it from your phone; it spawns a Claude Code session on your machine, which reasons
and then replies by calling momo's own CLI. So anything momo can do in Matrix, the
agent can do — send, upload a diff, react, run a poll.

It also raises work *with* you, on a schedule. A reminder or a ritual opens a pinned
thread with the work already prepared, so responding is one tap instead of a context
switch.

Named after Momo Ayase from *Dandadan*, who also deals with whatever turns up so you
do not have to.

> **This is remote code execution by design.** With `ENGINE=claude`, anyone who can
> post in an allowed room runs Claude Code on your machine, as you. A single
> allowlisted Matrix user ID is the only gate. Put 2FA on that account.

## Quick start

Needs Go 1.26+, a Matrix account for the bot that is not your own, and `claude` on
`PATH` if you want the agent engine.

```bash
git clone https://github.com/kidkuddy/momo && cd momo
cp .env.example .env      # then fill it in
make build
```

```sh
HOMESERVER=https://matrix.org
BOT_USER=yourbot                 # localpart, first login only
BOT_PASSWORD=...                 # first login only — but keep it, recovery needs it
ALLOWED_USER=@you:matrix.org     # the only user momo obeys
# ENGINE=claude                  # omit and you get the harmless echo engine
# WORKDIR=/path/to/project
```

Then once, or clients show a shield on every message and a lost database takes your
room keys with it:

```bash
make crosssign    # signs the bot's own device; prints a recovery key
make backup       # server-side room key backup
```

**Store that recovery key offline.** It is shown once, saved nowhere, and is the only
way to sign a replacement device or restore room keys. The Makefile reads it back from
the macOS keychain:

```bash
security add-generic-password -a "@yourbot:matrix.org" \
  -s momo-matrix-recovery-key -w "<the key>" -U
```

Start it, then DM the bot from your own account:

```bash
make run
```

## The `goolm` build tag

Every `go` command needs `-tags=goolm`. It selects mautrix's pure-Go olm; without it
the build links libolm through cgo and fails on a missing `olm/olm.h`. The Makefile
always passes it — this only bites you running `go build` by hand.

## Reminders and threads

Ask in chat — "remind me to pay the invoice tomorrow at nine" — and the agent turns it
into an absolute time and calls `momo schedule add`. momo itself takes only exact times
and cron expressions; reading "after lunch" is the agent's job, since it already knows
the date and your timezone.

When it fires, a pinned thread opens with the work already prepared. React ✅ on the
root to close it, which also settles older threads of the same `--kind` — three
unanswered inbox reminders are one overdue task. Missed occurrences are skipped, not
replayed: three days offline means one thread today, not three.

## CLI

```
momo daemon                          run the bot
momo send|upload|react|edit|redact|typing|read <room> …
momo poll|endpoll|poll-results <room> …
momo rooms|join|leave|invite|whoami
momo history [--room ID] [--thread ID] [--limit N]
momo clear <room>                    redact momo's messages, wipe local history

momo start --message <ping>          open a pinned thread, work prepared
                                     (room defaults to the DM with ALLOWED_USER)
momo threads                         what is still outstanding
momo nudge                           push on threads that have stalled
momo resolve <room> <thread>         mark done
momo schedule add|list|rm            reminders that open a thread when they fire

momo profiles                        list configured bots
momo crosssign|backup|restore|reset-session
```

`momo --help` lists the flags. The exhaustive reference lives in `.claude/skills/` —
`matrix-cli`, `matrix-events`, `matrix-e2ee`, `momo-dev`, `momo-threads`, which
`make install-skills` symlinks into `~/.claude/skills`.

Every command that creates an event prints its event ID and nothing else, so it
composes:

```bash
EV=$(momo send "$ROOM" "working on it")
momo react "$ROOM" "$EV" 👀
```

## Profiles and running it as a service

A profile is a directory under `~/.momo/<name>` holding one bot's whole identity —
config, credentials, crypto store, history, socket. Pick one with `--profile work`.

To run momo in the background: `make install` then `make service PROFILE=momo`, which
writes a LaunchAgent plist for you to read before `make service-load`. Install first —
pointing the service at the repo build output means a rebuild swaps the file under a
starting process. launchd also gives it a minimal `PATH`, so `CLAUDE_BIN` has to be an
absolute path or the agent is not found.

## Architecture

Ports and adapters. `internal/core` holds the domain types and interfaces and imports
nothing else; adapters implement them; `cmd/momo` is the only place that wires
concrete types together.

```
cmd/momo/          composition root + CLI
internal/core/     domain types and ports
internal/matrix/   the only package that imports mautrix
internal/store/    SQLite history
internal/engine/   echo / Claude Code — swap in another agent here
internal/bot/      the rules: who gets answered, and how
internal/config/   profile resolution
internal/ipc/      unix socket
internal/schedule/ when a reminder fires next
```

The socket exists because the daemon owns an olm account and a second process cannot
share it: both would encrypt from the same megolm index, a silent cryptographic fault.
So `momo send` from inside an agent session forwards to the daemon rather than opening
the crypto store. With no daemon running it opens it directly, which is safe.

## Known limits

- **Polls are unstable.** MSC3381 has no stable room version, so momo uses the
  `org.matrix.msc3381.poll.*` namespace. Element understands it today.
- **The agent runs with `bypassPermissions` by default.** A headless session has
  nobody to approve a prompt, so anything stricter means it refuses and never replies.
  Narrow it with `ENGINE_ALLOWED_TOOLS`; an approval gate in the chat is on the
  roadmap.
- **matrix.org needs a browser.** It runs MAS, so device deletion and cross-signing
  resets are not scriptable. Plain Synapse is.
- **`clear` cannot remove your messages.** Redacting someone else's event needs a
  power level momo does not have in a DM you created. It redacts its own and wipes
  what it stores; yours stay until you delete them in your client.
- **momo cannot be verified as a user** — no SAS — so "never send encrypted messages
  to unverified sessions" has to stay off in your client.

Not built yet: streaming, approval gates, secret redaction. See [ROADMAP.md](ROADMAP.md).

## License

MIT
