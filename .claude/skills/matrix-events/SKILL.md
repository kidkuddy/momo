---
name: matrix-events
description: The Matrix event model — how messages, threads, replies, edits, reactions, redactions and message types actually work on the wire. Use when reasoning about why a message rendered oddly, designing a feature that touches Matrix event structure, or deciding which relation type to use.
---

# matrix-events — how Matrix actually models a conversation

Everything in a Matrix room is an event with a `type` and a `content`. There is no
separate concept of "a message" — a message is just `m.room.message`.

## The types that matter

| Type | What it is |
|---|---|
| `m.room.message` | Anything a person or bot says, including files |
| `m.reaction` | An emoji annotation on another event |
| `m.room.redaction` | Removes another event's content |
| `m.room.encrypted` | An encrypted envelope; the real type is inside |
| `m.room.member` | Joins, leaves, invites, bans |
| `m.room.encryption` | Marks a room encrypted. Once set, never unset |
| `org.matrix.msc3381.poll.*` | Polls, still unstable |

## msgtype: what kind of message

`m.room.message` carries a `msgtype` that decides how clients render it:

- `m.text` — normal
- `m.notice` — automated output. **Use this for bots.** Clients style it differently
  and well-behaved bots ignore each other's notices, which is what stops loops.
- `m.emote` — renders as "* name does something"
- `m.image` `m.audio` `m.video` `m.file` — attachments

The spec does not limit msgtypes; custom ones are allowed, and unknown ones fall back
to showing `body`.

## Formatting

A message has a plain `body` and optionally `format: org.matrix.custom.html` plus
`formatted_body`. The plain body is the fallback and must always make sense on its
own — never put the real content only in HTML.

Clients allow a restricted HTML subset. Anything you did not generate yourself must be
escaped: message content is untrusted input that renders in someone's client.

## Relations: how events point at each other

All relations live in `m.relates_to`, but they work differently.

**Threads** (`rel_type: m.thread`) — `event_id` is the *thread root*, which is the
first message of the thread, never the message being answered. Every event in a
thread points at the same root. This is the one people get wrong: pointing at the
previous message creates a broken thread.

Threads also carry a reply fallback (`is_falling_back: true`) so clients without
thread support show something sensible.

**Replies** (`m.in_reply_to`) — a nested object, not a `rel_type`. Points at the exact
event being answered. A message can be both in a thread and a reply to something
inside it.

**Edits** (`rel_type: m.replace`) — the new content goes in `m.new_content`; the
top-level `body` is a fallback conventionally prefixed with `* `. Clients that
understand edits show the new content with an "edited" marker; ones that do not show
the fallback as a new message. **Editing does not rewrite history** — the original
event still exists, so an edit is not a way to unsay something.

**Reactions** (`rel_type: m.annotation`) — `key` holds the emoji. Reactions are not
encrypted even in encrypted rooms, and they are not aggregated server-side in any
useful way; clients count them.

## Redaction

`m.room.redaction` strips an event's content. The event itself remains, so the
transcript keeps its shape, but the body is gone for everyone. It is irreversible.

Redaction removes content, not knowledge: anyone who already received the message
still has it.

## Encryption's effect on all of this

In an encrypted room, `m.room.message` never appears on the wire. You get
`m.room.encrypted`, and the real event is inside. Relations are inside the ciphertext
too, so a server cannot thread or aggregate.

Two exceptions stay in the clear: `m.reaction` and `m.room.redaction`.

Attachments are encrypted separately — the file is encrypted client-side and the
event carries a `file` object with the decryption key instead of a plain `url`.

## Event IDs

Event IDs start with `$` and are opaque. In shell, **always single-quote them** —
`"$abc"` is shell variable expansion and silently becomes empty.

## Timing

`origin_server_ts` is the *sending server's* clock. It is not trustworthy for
ordering across servers and can be arbitrarily wrong. Use it for display, not logic.

## Gotchas worth remembering

- A room's encryption cannot be turned off. Decide before history accumulates.
- Editing a message does not edit what people already read, and does not remove the
  original event.
- `m.notice` is the difference between a well-behaved bot and one that gets into a
  loop with another bot.
- Threads are the only relation whose target is not "the thing I am responding to".
