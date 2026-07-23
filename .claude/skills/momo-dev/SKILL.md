---
name: momo-dev
description: How the momo codebase is laid out and how to change it — the ports-and-adapters structure, where each concern lives, build and test commands, and the conventions to follow. Use before editing momo's Go code or adding a feature to it.
---

# momo-dev — working on momo itself

## Build and test

```bash
make check     # fmt, vet, test
make build     # -> ./momo
make run       # sources .env, runs the daemon
```

**Every go command needs `-tags=goolm`.** Without it mautrix links libolm through cgo
and the build dies on a missing `olm/olm.h`. The Makefile always passes it; if you
run `go` directly, pass it yourself.

## Layout

```
cmd/momo/          composition root + CLI. The only place that knows which
                   concrete type satisfies which interface.
internal/core/     domain types and ports. Imports nothing from the project.
internal/matrix/   Matrix adapter (mautrix). The only package importing mautrix.
internal/store/    SQLite adapter for history.
internal/engine/   what answers a message: echo or Claude Code.
internal/bot/      application layer: the rules. Depends only on core.
```

Dependencies point inward. `core` defines interfaces; adapters implement them;
`cmd/momo` wires them together. Nothing outside `internal/matrix` should import
mautrix — if you find yourself wanting to, add a method to the adapter instead.

## The ports

`core.Chat` — sending, reacting, editing, redacting, polls.
`core.Rooms` — membership and metadata.
`core.History` — the durable local record.
`core.Engine` — turns a prompt into a reply.

They are separate on purpose: the CLI constructs far less than the daemon does, and
a test double for one does not have to fake the others.

## Where things go

| Change | Where |
|---|---|
| New Matrix action | `internal/matrix/chat.go` + a method on `core.Chat` + a CLI case |
| New CLI subcommand | `cmd/momo/commands.go`, plus the `usage` string in `main.go` |
| New stored field | `internal/store/store.go` schema and both scan sites |
| Change what momo replies to | `internal/bot/bot.go` — `ShouldAnswer` |
| Anything crypto | `internal/matrix/` — see the `matrix-e2ee` skill first |

## Conventions

- **Comments explain why, not what.** The code says what it does. A comment earns its
  place by recording a constraint, a trap, or a decision that is not visible locally.
- **Errors go up, they do not exit.** Only `cmd/momo` calls `os.Exit`. Library code
  returns errors; the CLI prints them.
- **Tests cover rules, not plumbing.** `ShouldAnswer` is the allowlist and is tested
  exhaustively. Store idempotency is tested because sync replays events. Do not write
  tests that assert mautrix works.
- Conventional commits: `feat(matrix): ...`, `fix(store): ...`.

## Two things that will bite you

**The daemon holds a lock on `momo.db`.** A CLI command run while the daemon is up
can fail with a busy database. Stop the daemon, or copy the file if you only want to
read it.

**`ALLOWED_USER` is the entire security model.** It is the only thing between a chat
message and a Claude Code process running on the host with the user's privileges.
Any change to `ShouldAnswer` or the invite handler is a security change — treat it
as one.

## Config

Environment, read in `cmd/momo/main.go`:

`HOMESERVER` `BOT_USER` `BOT_PASSWORD` `ALLOWED_USER` `ENGINE` `WORKDIR` `DEBUG`
`STATE_FILE` `CRYPTO_DB` `HISTORY_DB`

`ENGINE` defaults to echo. `ENGINE=claude` is the opt-in that makes momo execute
things, and it is deliberately not the default.
