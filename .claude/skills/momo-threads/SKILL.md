---
name: momo-threads
description: Interrupt the user through Matrix — open a piece of work as a pinned thread with a prepared proposal, see what is still outstanding, and mark it done. Use when a schedule, workflow or cron decides the user should be prompted about something (inbox triage, a planning ritual, a digest), or when checking what momo has already asked for and not had answered.
---

# momo-threads — raising work with the user

momo is the interruption surface. This skill is for the *inverted* direction: not
answering a message, but deciding the user should be prompted, and creating that
conversation.

A **thread** is one piece of work: a ping, a pinned root, a brief, and a state. It
exists because scheduled work has to survive being ignored — which is the normal
case, not the exception.

## Open a piece of work

```bash
momo start '!room:server' \
  --kind inbox \
  --message "inbox time — here's where things stand" \
  --brief-file /path/to/brief.md \
  --wip 2
```

What happens, in order: the ping is posted and pinned, the thread is recorded with
its brief, and an agent session runs the brief immediately so the reply arrives with
the work already done. That last part is the whole design — responding should be one
tap, not a context switch. Never send a bare "time to do X"; send X, prepared.

It prints the thread root event id. Keep it: that is the handle for everything else.

| Flag | Effect |
|---|---|
| `--kind K` | groups recurring work so duplicates can settle each other |
| `--brief T` / `--brief-file P` | what the agent should prepare. Without it no agent runs |
| `--message T` | the ping itself |
| `--wip N` | skip if N threads of this kind are already open |
| `--no-pin` | do not pin |
| `--no-agent` | post the ping only |

## The WIP limit and why it matters

A daily reminder that is ignored becomes a wall of identical unread threads, and a
wall of unread threads is ignored harder. `--wip` caps that: hitting the limit prints
`skipped:` and the existing thread's id, and exits successfully. **That is not a
failure.** The backlog already carries the signal — do not retry, do not escalate,
and do not open a second thread by another route.

## What is outstanding

```bash
momo threads                 # everything open
momo threads --kind inbox
```

One line per thread: kind, root id, age, first line of the brief. Age is the
interesting column — a thread open for two days means the ritual is not working, and
is worth saying out loud rather than silently re-pinging.

## Mark it done

```bash
momo resolve '!room:server' '$threadroot'
momo resolve '!room:server' '$threadroot' --only     # leave same-kind threads open
```

By default this also settles every other open thread of the same kind, and says so.
That is deliberate: three unanswered inbox reminders are one overdue task, so doing
it late clears the backlog rather than leaving two stale reminders nagging about work
that is finished.

Resolving unpins. `--keep-pin` leaves the pins alone.

## Replying inside a thread

Use the `matrix-cli` skill. The short version:

```bash
momo send '!room:server' "your text" --thread '$threadroot'
```

Single-quote every room and event id — they contain `!` and `$`, which the shell
expands.

## Rules

- **Prepare, then ping.** A reminder with no work attached is an alarm, and alarms
  are what this system exists to replace.
- **One kind per ritual.** `inbox`, `weekly-plan`, `papers`. The kind is what makes
  deduplication and WIP limits work; without it every thread is unique and piles up.
- **Do not re-ping an open thread.** Add to the existing one instead — it is pinned
  and the user can find it.
- **Resolve when the user acts**, not when the agent finishes talking. The thread
  tracks *their* work, not yours.
- Skipping on a WIP limit is a normal outcome. Report it plainly and stop.

## Profiles

Every command takes `--profile <name>` to act as a particular bot; `MOMO_PROFILE`
does the same. Inside a session momo spawned it is already set, so plain `momo …`
reaches the right daemon. Only pass it explicitly from a cron or workflow that momo
did not start.
