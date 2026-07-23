// Package matrix is the Matrix adapter: it implements core.Chat and core.Rooms on
// top of mautrix, and owns everything protocol-specific — crypto, event shapes,
// media encryption. Nothing outside this package imports mautrix.
package matrix

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Config is everything needed to bring a client up.
type Config struct {
	Homeserver string
	User       string // localpart, only needed for a first login
	Password   string // only needed for a first login
	StatePath  string // JSON: access token, device id, pickle key
	CryptoPath string // SQLite: olm/megolm keys, room state, sync position
	Debug      bool
}

// Client wraps a logged-in, encryption-capable mautrix client.
type Client struct {
	mx    *mautrix.Client
	ch    *cryptohelper.CryptoHelper
	state *stateFile
}

type stateFile struct {
	path        string
	AccessToken string `json:"access_token"`
	DeviceID    string `json:"device_id"`
	UserID      string `json:"user_id"`
	// PickleKey encrypts the olm keys at rest in the crypto store. Lose it and
	// that database is unreadable even though the file is intact.
	PickleKey string `json:"pickle_key"`
}

// New logs in (or resumes), starts the crypto machinery and returns a ready client.
// Callers must Close it so the crypto database is checkpointed.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Homeserver == "" {
		return nil, fmt.Errorf("homeserver is required")
	}
	st, err := loadState(cfg.StatePath)
	if err != nil {
		return nil, err
	}

	mx, err := mautrix.NewClient(cfg.Homeserver, id.UserID(st.UserID), st.AccessToken)
	if err != nil {
		return nil, err
	}
	mx.DeviceID = id.DeviceID(st.DeviceID)
	mx.Log = newLogger(cfg.Debug)

	pickle, err := st.pickleKey()
	if err != nil {
		return nil, err
	}
	// Passing a path makes the helper create and own the crypto store, the room
	// state store, and the sync store. The room state store is the one that is easy
	// to miss: without it a restarted bot forgets a room is encrypted and posts
	// plaintext into it.
	ch, err := cryptohelper.NewCryptoHelper(mx, pickle, cfg.CryptoPath)
	if err != nil {
		return nil, err
	}
	if st.AccessToken == "" {
		if cfg.User == "" || cfg.Password == "" {
			return nil, fmt.Errorf("no saved token and no credentials to log in with")
		}
		ch.LoginAs = &mautrix.ReqLogin{
			Type:                     mautrix.AuthTypePassword,
			Identifier:               mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: cfg.User},
			Password:                 cfg.Password,
			InitialDeviceDisplayName: "momo",
		}
	}
	if err := ch.Init(ctx); err != nil {
		return nil, fmt.Errorf("crypto init: %w", err)
	}
	mx.Crypto = ch

	st.AccessToken, st.DeviceID, st.UserID = mx.AccessToken, mx.DeviceID.String(), mx.UserID.String()
	if err := st.save(); err != nil {
		return nil, err
	}
	return &Client{mx: mx, ch: ch, state: st}, nil
}

func (c *Client) Close() error         { return c.ch.Close() }
func (c *Client) UserID() id.UserID    { return c.mx.UserID }
func (c *Client) Raw() *mautrix.Client { return c.mx }
func (c *Client) Machine() *crypto.OlmMachine {
	return c.ch.Machine()
}

// OnDecryptError registers a callback for events that could not be decrypted.
func (c *Client) OnDecryptError(fn func(roomID, eventID, sender string, err error)) {
	c.ch.DecryptErrorCallback = func(evt *event.Event, err error) {
		fn(evt.RoomID.String(), evt.ID.String(), evt.Sender.String(), err)
	}
}

// ---- state file --------------------------------------------------------

func loadState(path string) (*stateFile, error) {
	st := &stateFile{path: path}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, fmt.Errorf("%s is unreadable: %w", path, err)
	}
	st.path = path
	return st, nil
}

func (s *stateFile) save() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	// Atomic: a crash mid-write must not leave a truncated token behind.
	return os.Rename(tmp, s.path)
}

func (s *stateFile) pickleKey() ([]byte, error) {
	if s.PickleKey == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		s.PickleKey = hex.EncodeToString(b)
		if err := s.save(); err != nil {
			return nil, err
		}
	}
	k, err := hex.DecodeString(s.PickleKey)
	if err != nil {
		return nil, fmt.Errorf("pickle_key is not hex; restore it or delete the crypto store too")
	}
	return k, nil
}

// ResetSession blanks the token and device so the next start logs in fresh. This is
// the way back from a lost crypto store: the server still holds keys for the old
// device, and mautrix refuses to start when it cannot match them locally.
func ResetSession(path string) error {
	st, err := loadState(path)
	if err != nil {
		return err
	}
	st.AccessToken, st.DeviceID = "", ""
	return st.save()
}

func newLogger(debug bool) zerolog.Logger {
	lvl := zerolog.WarnLevel
	if debug {
		lvl = zerolog.DebugLevel // key sharing and decryption failures only show up here
	}
	return zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.TimeOnly}).
		Level(lvl).With().Timestamp().Logger()
}
