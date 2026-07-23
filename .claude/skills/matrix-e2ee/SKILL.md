---
name: matrix-e2ee
description: Matrix end-to-end encryption as it actually behaves — olm/megolm, device verification, cross-signing, key backup, and the failure modes. Use when messages will not decrypt, a client shows a shield or "unverified device" warning, keys or sessions are lost, or a homeserver rejects a crypto operation.
---

# matrix-e2ee — encryption, and what goes wrong with it

## The model in one pass

- **olm** is the 1:1 channel between two *devices*. It exists to deliver keys.
- **megolm** encrypts room messages. A sender creates an outbound megolm session,
  encrypts with it, and ships the session key to every device in the room over olm.
- A device can only read messages whose session key it was given. Keys flow forward
  only: **joining a room, or logging in a new device, gives you nothing that came
  before.** That is not a bug and cannot be worked around, only backed up against.

The practical consequence: losing the crypto store loses all readable history unless
a server-side key backup exists.

## momo's files

| File | Holds | Recoverable? |
|---|---|---|
| `momo.db` | olm/megolm keys, room state, sync position | from key backup |
| `state.json` | access token, device id, **pickle key** | no — but see rebuild |
| keychain | cross-signing recovery key | no |

The pickle key encrypts `momo.db` at rest. The two files are a matched pair: either
one alone is useless. That sounds fatal and is not — see *Rebuilding from nothing*.

## Commands

```bash
make crosssign    # sign momo's own device
make backup       # create the room key backup, upload everything held
make restore      # pull room keys back into a fresh momo.db
```

All three read the recovery key from the login keychain
(`security find-generic-password -s momo-matrix-recovery-key -w`).

Room key backup uploads using only the backup's **public** key, so the running daemon
holds no secret. The private key lives in secret storage (SSSS) behind the recovery
key and is needed only to restore.

## Failure modes, and what each actually means

**"Unable to decrypt" / "no session with given ID found"**
The sender's client never gave this device the room key. Usual causes, in order:
the message predates the device; the sender has "never send to unverified sessions"
enabled; the crypto store was reset. Only the second is fixable after the fact —
turn that setting off in the sender's client and have them resend.

**"Encrypted by a device not verified by its owner"**
The *account* has never signed its own device with its cross-signing key. Fixed by
`make crosssign`. Nothing to do with the recipient trusting you.

**Shields on messages generally**
Cosmetic. Encryption is working; the client is telling you it cannot vouch for who
sent it. It does not block key sharing unless the user has turned on the strict
setting.

**`olm account is not marked as shared, but there are keys on the server`**
The crypto store was lost while the server still holds device keys for that device
id. mautrix refuses to continue because it cannot prove it is the same device. The
device is unrecoverable; make a new one (see below).

**HTTP 401 on cross-signing upload**
Replacing an existing cross-signing identity needs interactive auth. Classic Synapse
takes the account password. **matrix.org runs MAS and offers no password stage at
all** — the flows are `m.oauth` and `org.matrix.cross_signing_reset`, and the reset
must be approved in a browser at `account.matrix.org` first. `momo crosssign` prints
the approval URL out of the challenge body.

**Resetting cross-signing has a cost.** It un-signs every device the old identity had
signed, so other sessions on that account show as unverified until re-verified. If a
recovery key exists, prefer `momo crosssign "<key>"`, which signs against the
existing identity and costs nothing.

## Rebuilding from nothing

With `BOT_PASSWORD` and the recovery key, total loss is recoverable:

```bash
momo reset-session                          # blank token + device id
momo whoami                                 # logs in, creates a new device
momo crosssign "<recovery key>"             # sign it against the existing identity
momo restore "<recovery key>"               # pull room keys back down
```

Each rebuild leaves a dead device on the account. Clean those up in the client.

## Things that are true and surprising

- **Encryption cannot be turned off** for a room, ever. Decide before history builds.
- **Reactions and redactions are never encrypted**, even in encrypted rooms.
- **momo can read its own sent messages.** mautrix stores an inbound copy of every
  outbound megolm session at send time. This is easy to doubt: a grep for
  `PutGroupSession` in `encryptmegolm.go` finds nothing, because it goes through
  `createGroupSession`.
- **The `goolm` build tag is mandatory.** Without it mautrix links libolm through cgo
  and the build fails on a missing `olm/olm.h`. The Makefile always passes it.
- **matrix.org is not fully scriptable.** Device deletion returns `M_UNRECOGNIZED`
  over the client-server API because MAS owns device management. Plain Synapse is
  fine.

## Debugging

`DEBUG=1` turns on mautrix's own logging. Key sharing, session creation and
decryption failures appear only there. Expect a lot of output.

To see what keys are actually held:

```bash
cp momo.db /tmp/check.db   # avoid fighting the daemon for the lock
sqlite3 /tmp/check.db "select room_id, substr(sender_key,1,16), key_source
                       from crypto_megolm_inbound_session;"
```

`key_source` is `direct` for sessions created locally, `forward` for ones shared by
another device, and `backup` for ones pulled from the key backup.
