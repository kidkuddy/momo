package matrix

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/signatures"
	"maunium.net/go/mautrix/crypto/ssss"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Server-side room key backup. Megolm keys exist only on the devices that received
// them, so without this, losing the crypto store makes every past message
// permanently unreadable.
//
// Uploading needs only the backup's *public* key, which the server hands out, so the
// running daemon holds no secret at all. The private key lives in SSSS behind the
// account recovery key and is needed only to restore.

// BackupTarget is the version and public key to upload to, resolved once at startup.
type BackupTarget struct {
	version id.KeyBackupVersion
	pubkey  *ecdh.PublicKey
}

func (b *BackupTarget) Version() string { return b.version.String() }

// LoadBackupTarget returns nil when no usable backup exists. That is not an error:
// momo runs fine without one, it just is not durable.
func (c *Client) LoadBackupTarget(ctx context.Context) (*BackupTarget, error) {
	v, err := c.mx.GetKeyBackupLatestVersion(ctx)
	if err != nil {
		if errors.Is(err, mautrix.MNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if v.Algorithm != id.KeyBackupAlgorithmMegolmBackupV1 {
		return nil, fmt.Errorf("unsupported key backup algorithm %q", v.Algorithm)
	}
	pubkey, err := decodePubkey(v.AuthData.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("backup public key unreadable: %w", err)
	}
	return &BackupTarget{version: v.Version, pubkey: pubkey}, nil
}

func decodePubkey(k id.Ed25519) (*ecdh.PublicKey, error) {
	raw, err := base64.RawStdEncoding.DecodeString(string(k))
	if err != nil {
		return nil, err
	}
	return ecdh.X25519().NewPublicKey(raw)
}

// BackupSession uploads one session. Re-uploading is harmless: the server keeps
// whichever copy has the lower first_message_index.
func (c *Client) BackupSession(ctx context.Context, t *BackupTarget, sess *crypto.InboundGroupSession) error {
	firstIndex := sess.Internal.FirstKnownIndex()
	exported, err := sess.Internal.Export(firstIndex)
	if err != nil {
		return fmt.Errorf("export session: %w", err)
	}
	encrypted, err := backup.EncryptSessionDataWithPubkey(t.pubkey, backup.MegolmSessionData{
		Algorithm:          id.AlgorithmMegolmV1,
		ForwardingKeyChain: sess.ForwardingChains,
		SenderClaimedKeys:  backup.SenderClaimedKeys{Ed25519: sess.SigningKey},
		SenderKey:          id.SenderKey(sess.SenderKey),
		SessionKey:         string(exported),
	})
	if err != nil {
		return fmt.Errorf("encrypt session data: %w", err)
	}
	raw, err := json.Marshal(encrypted)
	if err != nil {
		return err
	}
	_, err = c.mx.PutKeysInBackupForRoomAndSession(ctx, t.version, sess.RoomID, sess.ID(),
		&mautrix.ReqKeyBackupData{
			FirstMessageIndex: int(firstIndex),
			ForwardedCount:    len(sess.ForwardingChains),
			IsVerified:        true,
			SessionData:       raw,
		})
	return err
}

// BackupAll uploads everything already in the store: the initial fill, and the
// catch-up after any stretch where no backup existed.
func (c *Client) BackupAll(ctx context.Context, t *BackupTarget) (done, failed int) {
	mach := c.ch.Machine()
	err := mach.CryptoStore.GetAllGroupSessions(ctx).Iter(func(sess *crypto.InboundGroupSession) (bool, error) {
		if err := c.BackupSession(ctx, t, sess); err != nil {
			c.mx.Log.Warn().Err(err).Stringer("session", sess.ID()).Msg("backup failed")
			failed++
		} else {
			done++
		}
		return true, nil
	})
	if err != nil {
		c.mx.Log.Warn().Err(err).Msg("could not read sessions")
	}
	return done, failed
}

// WatchNewSessions uploads each room key as it arrives, so the backup stays current.
func (c *Client) WatchNewSessions(t *BackupTarget) {
	mach := c.ch.Machine()
	mach.SessionReceived = func(ctx context.Context, room id.RoomID, session id.SessionID, _ uint32) {
		sess, err := mach.CryptoStore.GetGroupSession(ctx, room, session)
		if err != nil || sess == nil {
			return
		}
		if err := c.BackupSession(ctx, t, sess); err != nil {
			c.mx.Log.Warn().Err(err).Stringer("session", session).Msg("backup failed")
		}
	}
}

// SetupBackup creates the backup version and stashes its private key in SSSS, where
// the account recovery key can reach it. Run once.
func (c *Client) SetupBackup(ctx context.Context, recoveryKey string) (version string, err error) {
	mach := c.ch.Machine()
	ssssKey, err := c.openSSSS(ctx, recoveryKey)
	if err != nil {
		return "", err
	}
	key, err := backup.NewMegolmBackupKey()
	if err != nil {
		return "", fmt.Errorf("generate backup key: %w", err)
	}
	resp, err := c.mx.CreateKeyBackupVersion(ctx, &mautrix.ReqRoomKeysVersionCreate[backup.MegolmAuthData]{
		Algorithm: id.KeyBackupAlgorithmMegolmBackupV1,
		AuthData: backup.MegolmAuthData{
			PublicKey: id.Ed25519(base64.RawStdEncoding.EncodeToString(key.PublicKey().Bytes())),
			// Unsigned: other clients will show the backup as unverified. momo is the
			// only writer and trusts it via the private key in SSSS. Sign the auth
			// data if a second client ever has to trust it.
			Signatures: signatures.Signatures{},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create backup version: %w", err)
	}
	// Without this the backup is write-only: the private key would exist nowhere.
	if err := mach.SSSS.SetEncryptedAccountData(ctx, event.AccountDataMegolmBackupKey, key.Bytes(), ssssKey); err != nil {
		return "", fmt.Errorf("store backup key in secret storage: %w", err)
	}
	t := &BackupTarget{version: resp.Version, pubkey: key.PublicKey()}
	c.BackupAll(ctx, t)
	return resp.Version.String(), nil
}

// RestoreBackup pulls room keys back down into a fresh crypto store.
func (c *Client) RestoreBackup(ctx context.Context, recoveryKey string) (version string, err error) {
	mach := c.ch.Machine()
	ssssKey, err := c.openSSSS(ctx, recoveryKey)
	if err != nil {
		return "", err
	}
	raw, err := mach.SSSS.GetDecryptedAccountData(ctx, event.AccountDataMegolmBackupKey, ssssKey)
	if err != nil {
		return "", fmt.Errorf("read backup key from secret storage: %w", err)
	}
	key, err := backup.MegolmBackupKeyFromBytes(raw)
	if err != nil {
		return "", fmt.Errorf("backup key in secret storage is unusable: %w", err)
	}
	v, err := mach.DownloadAndStoreLatestKeyBackup(ctx, key)
	if err != nil {
		return "", fmt.Errorf("restore key backup: %w", err)
	}
	return v.String(), nil
}

func (c *Client) openSSSS(ctx context.Context, recoveryKey string) (*ssss.Key, error) {
	if recoveryKey == "" {
		return nil, fmt.Errorf("a recovery key is required")
	}
	mach := c.ch.Machine()
	keyID, keyData, err := mach.SSSS.GetDefaultKeyData(ctx)
	if err != nil {
		return nil, fmt.Errorf("no secret storage on this account (run crosssign first): %w", err)
	}
	key, err := keyData.VerifyRecoveryKey(keyID, recoveryKey)
	if err != nil {
		return nil, fmt.Errorf("recovery key rejected: %w", err)
	}
	return key, nil
}
