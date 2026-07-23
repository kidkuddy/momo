# momo

A Matrix bot that is the chat UI for [Claude Code](https://claude.com/claude-code).
You talk to it in an end-to-end encrypted DM; it runs Claude Code on your machine and
answers in the thread.

It is also a general-purpose Matrix CLI, so an agent can act in Matrix — send, upload,
react, edit, poll, read history — without touching the protocol.

> **This is a remote code execution surface by design.** With `ENGINE=claude`, anyone
> who can post in an allowed room runs Claude Code on your machine as you. A single
> allowlisted user ID is the only gate. Put 2FA on that Matrix account.

## Status

End-to-end encryption works, including DMs, cross-signing and server-side room key
backup. The CLI covers the common Matrix surface. What is *not* built yet is the part
that makes it feel like a conversation — session continuity and streaming. See
[ROADMAP.md](ROADMAP.md).

## Requirements

- Go 1.26+
- A Matrix account for the bot, separate from your own
- `claude` on `PATH`, only if you set `ENGINE=claude`

## Setup

```bash
git clone https://github.com/kidkuddy/momo
cd momo
cp .env.example .env      # then fill it in
make build
```

`.env`:

```sh
HOMESERVER=https://matrix.org
BOT_USER=yourbot                    # localpart, first login only
BOT_PASSWORD=...                    # first login only, but keep it: recovery needs it
ALLOWED_USER=@you:matrix.org        # the only user momo obeys
# ENGINE=claude                     # omit to stay on the echo engine
# WORKDIR=/path/to/project
```

Then, once:

```bash
make crosssign    # sign the bot's own device, or clients show a shield
make backup       # server-side room key backup
```

`crosssign` prints a recovery key. **Store it offline** — it is the only way to sign a
replacement device or restore room keys later. On macOS:

```bash
security add-generic-password -a "@yourbot:matrix.org" \
  -s momo-matrix-recovery-key -w "<the key>" -U
```

The Makefile's `crosssign`/`backup`/`restore` targets read it from there.

Start it, then DM the bot from your own account:

```bash
make run
```

## CLI

```
momo daemon                      run the bot
momo send <room> <text>          [--thread ID] [--reply ID] [--notice] [--emote] [--html S]
momo upload <room> <path>        [--as image|audio|video|file]
momo react|edit|redact <room> <event> ...
momo poll <room> <question> <answer>...
momo rooms|join|leave|invite|whoami
momo history [--room ID] [--thread ID] [--limit N]
momo crosssign|backup|restore [recovery key]
momo reset-session               forget token+device, forcing a fresh login
```

Every command that creates an event prints its event ID and nothing else, so it
composes:

```bash
EV=$(momo send "$ROOM" "working on it")
momo react "$ROOM" "$EV" 👀
```

## Architecture

Ports and adapters. `internal/core` holds the domain types and interfaces and imports
nothing else; adapters implement them; `cmd/momo` is the only place that wires
concrete types together.

```
cmd/momo/          composition root + CLI
internal/core/     domain types and ports
internal/matrix/   Matrix adapter — the only package that imports mautrix
internal/store/    SQLite history
internal/engine/   echo / Claude Code
internal/bot/      the rules: who gets answered, and how
```

Three files hold state, and they are not interchangeable:

| File | Holds |
|---|---|
| `state.json` | access token, device id, pickle key |
| `momo.db` | olm/megolm keys, room state, sync position (mautrix owns the schema) |
| `history.db` | message history (momo owns this one) |

## Build tag

Every `go` command needs `-tags=goolm`, which selects mautrix's pure-Go olm. Without
it the build links libolm through cgo and fails on a missing `olm/olm.h`. The Makefile
always passes it.

## Skills

`.claude/skills/` ships four skills so Claude Code can drive and modify momo:
`matrix-cli`, `matrix-events`, `matrix-e2ee`, `momo-dev`.

## Known limits

- **Polls are unstable.** MSC3381 has no stable room version; momo uses the
  `org.matrix.msc3381.poll.*` namespace. Element understands it today.
- **No SAS verification.** momo cannot be verified as a *user*, so "Never send
  encrypted messages to unverified sessions" must stay off in your client.
- **matrix.org is not fully scriptable.** It runs MAS, so device deletion and
  cross-signing resets need a browser. Plain Synapse does not.
- **No session continuity yet.** Every message starts a fresh Claude Code run.

## License

MIT
