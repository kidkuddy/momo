// Package store is the SQLite implementation of core.History.
//
// It uses its own database file rather than joining mautrix's crypto store: that
// schema belongs to the library and is migrated by it, so adding tables there would
// mean fighting someone else's migrations forever.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/kidkuddy/momo/internal/core"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS messages (
    event_id    TEXT PRIMARY KEY,
    room_id     TEXT NOT NULL,
    sender      TEXT NOT NULL,
    ts          INTEGER NOT NULL,
    kind        TEXT NOT NULL,
    body        TEXT NOT NULL,
    html        TEXT NOT NULL DEFAULT '',
    thread_root TEXT NOT NULL DEFAULT '',
    reply_to    TEXT NOT NULL DEFAULT '',
    url         TEXT NOT NULL DEFAULT '',
    filename    TEXT NOT NULL DEFAULT '',
    redacted    INTEGER NOT NULL DEFAULT 0,
    raw         BLOB
);
CREATE INDEX IF NOT EXISTS idx_messages_room_ts ON messages (room_id, ts);
CREATE INDEX IF NOT EXISTS idx_messages_thread  ON messages (thread_root) WHERE thread_root <> '';
CREATE INDEX IF NOT EXISTS idx_messages_sender  ON messages (sender);

CREATE TABLE IF NOT EXISTS reactions (
    event_id  TEXT PRIMARY KEY,
    room_id   TEXT NOT NULL,
    sender    TEXT NOT NULL,
    target_id TEXT NOT NULL,
    key       TEXT NOT NULL,
    ts        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_reactions_target ON reactions (target_id);
`

// Open creates the database if needed and applies the schema.
func Open(path string) (*Store, error) {
	// WAL keeps the daemon writing while the CLI reads; busy_timeout covers the
	// overlap instead of failing the command outright.
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// SaveMessage is idempotent: a message re-delivered by a sync replay must not
// duplicate, and a later copy may legitimately fill in fields the first lacked.
func (s *Store) SaveMessage(ctx context.Context, m core.Message) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO messages (event_id, room_id, sender, ts, kind, body, html,
                              thread_root, reply_to, url, filename, redacted, raw)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
        ON CONFLICT(event_id) DO UPDATE SET
            body = excluded.body, html = excluded.html, redacted = excluded.redacted`,
		m.EventID, m.RoomID, m.Sender, m.Timestamp.UnixMilli(), string(m.Kind), m.Body, m.HTML,
		m.ThreadRoot, m.ReplyTo, m.URL, m.Filename, boolInt(m.Redacted), m.Raw)
	return err
}

func (s *Store) SaveReaction(ctx context.Context, r core.Reaction) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO reactions (event_id, room_id, sender, target_id, key, ts)
        VALUES (?,?,?,?,?,?) ON CONFLICT(event_id) DO NOTHING`,
		r.EventID, r.RoomID, r.Sender, r.TargetID, r.Key, r.Timestamp.UnixMilli())
	return err
}

// MarkRedacted keeps the row but drops the content, mirroring what redaction means
// on the wire: the event still happened, its body no longer exists.
func (s *Store) MarkRedacted(ctx context.Context, roomID, eventID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE messages SET redacted = 1, body = '', html = '', raw = NULL
         WHERE room_id = ? AND event_id = ?`, roomID, eventID)
	return err
}

func (s *Store) Messages(ctx context.Context, f core.HistoryFilter) ([]core.Message, error) {
	var where []string
	var args []any
	add := func(clause string, val any) {
		where = append(where, clause)
		args = append(args, val)
	}
	if f.RoomID != "" {
		add("room_id = ?", f.RoomID)
	}
	if f.ThreadRoot != "" {
		add("thread_root = ?", f.ThreadRoot)
	}
	if f.Sender != "" {
		add("sender = ?", f.Sender)
	}
	if !f.Since.IsZero() {
		add("ts >= ?", f.Since.UnixMilli())
	}
	q := `SELECT event_id, room_id, sender, ts, kind, body, html, thread_root,
                 reply_to, url, filename, redacted, raw FROM messages`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	// Newest first is what a reader wants; callers that need chronological order
	// reverse a bounded slice, which is cheaper than an unbounded ascending scan.
	q += " ORDER BY ts DESC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []core.Message
	for rows.Next() {
		var m core.Message
		var ts int64
		var redacted int
		var kind string
		if err := rows.Scan(&m.EventID, &m.RoomID, &m.Sender, &ts, &kind, &m.Body, &m.HTML,
			&m.ThreadRoot, &m.ReplyTo, &m.URL, &m.Filename, &redacted, &m.Raw); err != nil {
			return nil, err
		}
		m.Timestamp = time.UnixMilli(ts)
		m.Kind = core.Kind(kind)
		m.Redacted = redacted != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Reactions(ctx context.Context, roomID, targetID string) ([]core.Reaction, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, room_id, sender, target_id, key, ts FROM reactions
         WHERE room_id = ? AND target_id = ? ORDER BY ts`, roomID, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []core.Reaction
	for rows.Next() {
		var r core.Reaction
		var ts int64
		if err := rows.Scan(&r.EventID, &r.RoomID, &r.Sender, &r.TargetID, &r.Key, &ts); err != nil {
			return nil, err
		}
		r.Timestamp = time.UnixMilli(ts)
		out = append(out, r)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
