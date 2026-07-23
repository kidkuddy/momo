---
name: momo-threads
description: Interrupt the user through Matrix — open a piece of work as a pinned thread with the work already prepared, push on what has stalled, see what is outstanding, and close it out. Use when a schedule, workflow or cron decides the user should be prompted about something (inbox triage, a planning ritual, a digest, a review), or when checking what momo has already raised and not had answered.
---

# momo-threads — raising work with the user

momo is an interruption surface. This skill is the *inverted* direction: not answering
a message, but deciding the user should be prompted, and creating that conversation.

A **thread** is one piece of work: an opening ping, a pinned root, a durable brief,
and a state. It carries state because scheduled work has to survive being ignored —
which is the normal case, not the exception.

Every command takes `--profile <name>` to pick which bot acts; `MOMO_PROFILE` does the
same. Inside a session momo spawned it is already set, so plain `momo …` works. Pass
it explicitly from a cron or workflow momo did not start, or the command has no
credentials and fails with `no HOMESERVER set`.

## Open a piece of work

```bash
momo start '!room:server' \
  --kind inbox \
  --message "inbox time — here's where things stand" \
  --brief-file /path/to/brief.md \
  --wip 2
```

Order of events: the ping is posted and pinned, the thread is recorded with its
brief, and an agent runs the brief **in the background**. The command returns the
thread root event id in about a second — it does not wait for the agent. Keep that id;
it is the handle for everything else.

The agent's output lands in the thread a minute or two later. That is the whole
design: the ping and the prepared work arrive together, so responding is one tap
rather than a context switch. **Never send a bare "time to do X" — send X, prepared.**

| Flag | Effect |
|---|---|
| `--message T` | the opening ping. Required |
| `--kind K` | groups recurring work so duplicates can settle each other |
| `--brief T` / `--brief-file P` | what the agent prepares. Without either, no agent runs |
| `--wip N` | skip if N threads of this kind are already open |
| `--no-pin` | do not pin the root |
| `--no-agent` | post the ping only |

## The WIP limit

A daily reminder that gets ignored becomes a wall of identical unread threads, and a
wall of unread threads is ignored harder. `--wip` caps it. Hitting the limit prints
`skipped: N open "kind" thread(s)…` plus the existing thread id, and **exits
successfully**.

That is not a failure. The backlog already carries the signal. Do not retry, do not
escalate, do not open a second thread by another route.

## What is outstanding

```bash
momo threads                       # everything open
momo threads --kind inbox
momo threads --room '!room:server'
```

One line per thread: kind, root id, age, first line of the brief. Age is the column
that matters — a thread open for two days means the ritual is not working, which is
worth saying out loud rather than silently re-pinging.

## Push on what has stalled

```bash
momo nudge                                    # open threads older than 12h
momo nudge --kind inbox --older-than 6h
momo nudge --dry-run                          # what it would push on
```

Posts **into the existing thread**, never a new one. A second ping about the same
thing is how a reminder system becomes wallpaper; thread state exists so there is
exactly one place per piece of work.

The agent receives the original brief plus the conversation so far, and is asked to
argue for finishing rather than restate the task: shrink the next step, name what is
blocking it, or say plainly that the task is malformed and should be dropped.
Repeating the original message is precisely what already failed.

| Flag | Default | Effect |
|---|---|---|
| `--older-than D` | `12h` | only threads open at least this long |
| `--min-interval D` | `20h` | skip threads nudged more recently than this |
| `--kind K` / `--room R` | — | narrow the sweep |
| `--dry-run` | — | list targets, push on nothing |

`--min-interval` is why a daily sweep run twice does not nag twice. Run it from a
schedule; it is a no-op when nothing is stale.

## Close it out

**The user resolves, not the agent.** A thread is done when *they* judge the goal
met — the research acknowledged, the action item created, the task actually finished.
Never resolve a thread because you finished talking.

The normal path is a reaction: they tap **✅** (or 👍 ☑️ 🆗) on the thread root and
momo closes it, sweeps up the rest of its kind, and unpins. That exists because typing
an event id on a phone is not something anyone will do, and unresolved threads are the
signal the whole system runs on — if resolving is awkward, everything looks stalled.

From a script:

```bash
momo resolve '!room:server' '$threadroot'
momo resolve '!room:server' '$threadroot' --only      # leave same-kind threads open
momo resolve '!room:server' '$threadroot' --keep-pin  # leave pins alone
```

By default this also settles every other open thread of the same kind, and says so.
Deliberate: three unanswered inbox reminders are one overdue task, so doing it late
clears the backlog instead of leaving stale reminders nagging about finished work.

Unpinning is the only feedback in the simple case. momo speaks up only when something
non-obvious happened, such as older threads being swept up with it.

## Talking inside a thread

Use the `matrix-cli` skill for the full surface. The short version:

```bash
momo send '!room:server' "your text" --thread '$threadroot'
momo upload '!room:server' ./diff.patch --thread '$threadroot'
momo poll '!room:server' "Ship it?" "yes" "no"        # then momo poll-results
```

Single-quote every room and event id. They contain `!` and `$`, which the shell
expands — `"$abc"` silently becomes empty.

## Rules

- **Prepare, then ping.** A reminder with no work attached is an alarm, and alarms are
  what this system exists to replace.
- **One kind per ritual.** `inbox`, `weekly-plan`, `papers`. The kind drives
  deduplication, WIP limits and nudge scoping; without it every thread is unique and
  they pile up.
- **Never re-ping an open thread.** Add to it — it is pinned and findable. Use `nudge`.
- **Resolution tracks the user's work, not yours.**
- Skipping on a WIP limit is a normal outcome. Report it plainly and stop.

## When something looks wrong

- **`no HOMESERVER set`** — no profile selected. Pass `--profile <name>`; `momo
  profiles` lists them.
- **A thread got a ping but no prepared work** — the agent ran in the background and
  failed. Check the daemon log at `~/.momo/<profile>/momo.log`.
- **"I was restarted while working on that"** — the daemon was restarted mid-run. The
  sync position has already moved past the message, so it is not retried; the user
  needs to send it again.
- **Nothing at all after a message** — check the daemon is up:
  `launchctl print gui/$(id -u)/com.github.kidkuddy.momo.<profile>`.
