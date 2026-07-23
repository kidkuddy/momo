---
name: matrix-cli
description: Drive Matrix through the momo CLI — send messages, upload files, react, edit, redact, run polls, manage rooms, and read message history. Use whenever you need to say something in Matrix, respond to a user in a room or thread, share a file or diff, or look up what was said earlier.
---

# matrix-cli — acting in Matrix through momo

`momo` is a Go binary that owns a Matrix account. Every Matrix action is a
subcommand, so you never need to touch the Matrix API directly.

Build it once with `make build`; it needs `-tags=goolm`, which the Makefile supplies.
All commands read config from the environment, so source `.env` first:

```bash
set -a; . ./.env; set +a
```

Every command that creates an event prints the new event ID on stdout and nothing
else, so it composes:

```bash
EV=$(momo send "$ROOM" "working on it")
momo react "$ROOM" "$EV" "👀"
```

## Finding where to talk

```bash
momo whoami          # which account and device, and which engine is active
momo rooms           # joined rooms; a leading "e" marks an encrypted room
```

`momo rooms` prints `<e|space> <room id> <members> <label>`. DMs show as
`DM with @someone:server`.

## Sending

```bash
momo send <room> <text>
momo send <room> "text" --thread '$eventid'    # reply inside a thread
momo send <room> "text" --reply '$eventid'     # rich reply, not a thread
momo send <room> "text" --notice               # m.notice: the right type for bot output
momo send <room> "text" --emote                # "* momo does something"
momo send <room> "fallback" --html "<b>rich</b>"
```

Use `--notice` for anything automated. Clients style notices differently so a human
can tell your output from a person's, and other bots know not to react to it.

Fenced code blocks in the body are converted to `<pre><code>` automatically and
HTML-escaped, so pasting command output is safe and reads as code.

Long text is *not* split by the CLI — that only happens in the daemon. Keep a single
`send` under about 32KB or split it yourself.

## Files

```bash
momo upload <room> ./diff.patch
momo upload <room> ./screenshot.png --thread '$eventid'
momo upload <room> ./clip.mp4 --as video       # override the sniffed type
```

The type is inferred from the file extension, falling back to content sniffing.
In an encrypted room the bytes are encrypted client-side before upload, so the
homeserver never sees them — this is automatic, there is no flag.

## Annotating and amending

```bash
momo react  <room> <event> 👍
momo edit   <room> <event> "corrected text"
momo redact <room> <event> "reason"
momo typing <room> on|off
momo read   <room> <event>          # move the read marker
```

`edit` sends an `m.replace`, so clients show the new text with an "edited" marker.
`redact` removes the content for everyone — it is not an undo you can take back.

## Polls

```bash
momo poll <room> "Ship it?" "yes" "no" "later"
momo poll <room> "Which?" "a" "b" --multi 2 --disclosed
momo endpoll <room> <poll event id>
```

```bash
momo poll-results <room> <poll event id>
```

prints each answer with its vote count and who voted:

```
Ship it?  (open, 2 voter(s))
  yes                       2  @a:server @b:server
  no                        0
```

Counting follows MSC3381: a voter's most recent vote replaces their earlier ones,
votes cast after the poll closed do not count, and selections beyond
`max_selections` are ignored.

**Reading votes requires the daemon to have been running when they were cast.**
Votes arrive over sync; there is no way to fetch them afterwards, because the poll
response events are encrypted and momo only decrypts what it receives live.

Polls use the unstable MSC3381 namespace. Element renders them today; that is not a
guarantee for other clients or future versions.

## Starting a conversation over

```bash
momo clear <room>                  # redact momo's messages, wipe history and sessions
momo clear <room> --sessions-only  # forget the agent session, keep the transcript
momo clear <room> --local          # wipe what momo stores, leave the room alone
```

`--sessions-only` is the one to reach for when a conversation has gone in circles: the
next message starts a fresh agent session with no memory, without destroying anything.

momo cannot redact *your* messages — that needs a power level it does not have in a
DM you created. Say so rather than implying the room was emptied.

## Profiles

Every command takes `--profile <name>` to act as a particular bot. Without it, momo
uses the working directory. Inside an agent session `MOMO_PROFILE` is already set, so
plain `momo send` reaches the right daemon.

```bash
momo profiles
momo --profile work rooms
```

## Rooms

```bash
momo join <room id or #alias:server>
momo leave <room>              # also forgets it, so it stops appearing in syncs
momo invite <room> <user id>
```

## Reading history

History is a local SQLite database written by the daemon. It is the reliable way to
see what was said, including momo's own replies.

```bash
momo history --room '!abc:server' --limit 20
momo history --thread '$eventid'          # one conversation
momo history --sender '@someone:server'
```

Output is one line per message, oldest first, as `timestamp sender body`. Redacted
messages show `(redacted)`; attachments show `[kind] filename`.

History only contains what the daemon saw. If the daemon has never run, it is empty
even though the room has messages.

## When something fails

- **`error: ... M_FORBIDDEN`** — the account is not in that room, or lacks the power
  level for the action.
- **A send appears to work but nobody sees it** — check `momo rooms` shows the room
  as encrypted. An unencrypted send into an encrypted room is not possible; a send
  into the *wrong room* is.
- **Anything mentioning keys, sessions or decryption** — see the `matrix-e2ee` skill.

## Do not

- Do not run `momo daemon` to send one message. The daemon holds a lock on the crypto
  database; a CLI command run alongside it may fail with a busy database. Stop the
  daemon, or accept the retry.
- Do not put secrets in a message. Room history is durable and, on a hosted
  homeserver, stored on someone else's disk even when encrypted.
