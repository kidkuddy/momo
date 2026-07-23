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

Polls use the unstable MSC3381 namespace. Element renders them today; that is not a
guarantee for other clients or future versions.

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
