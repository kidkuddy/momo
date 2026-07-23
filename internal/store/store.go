// Package store is the SQLite implementation of core.History.
//
// It uses its own database file rather than joining mautrix's crypto store: that
// schema belongs to the library and is migrated by it, so adding tables there would
// mean fighting someone else's migrations forever.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

CREATE TABLE IF NOT EXISTS polls (
    event_id       TEXT PRIMARY KEY,
    room_id        TEXT NOT NULL,
    sender         TEXT NOT NULL,
    ts             INTEGER NOT NULL,
    question       TEXT NOT NULL,
    answers        TEXT NOT NULL,
    max_selections INTEGER NOT NULL DEFAULT 1,
    closed_at      INTEGER NOT NULL DEFAULT 0
);

-- Votes are kept, not overwritten: a voter may change their mind, and only their
-- last vote counts. Collapsing them on write would lose the ordering that decides.
CREATE TABLE IF NOT EXISTS poll_votes (
    event_id TEXT PRIMARY KEY,
    poll_id  TEXT NOT NULL,
    room_id  TEXT NOT NULL,
    sender   TEXT NOT NULL,
    ts       INTEGER NOT NULL,
    answers  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_poll_votes_poll ON poll_votes (poll_id);

-- One agent session per thread, so a conversation survives restarts.
CREATE TABLE IF NOT EXISTS engine_sessions (
    room_id     TEXT NOT NULL,
    thread_root TEXT NOT NULL,
    session_id  TEXT NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (room_id, thread_root)
);

-- Work momo raised with the user. Scheduled work has to survive being ignored,
-- which is the normal case, so a thread carries state rather than being fire-and-forget.
CREATE TABLE IF NOT EXISTS threads (
    room_id     TEXT NOT NULL,
    thread_root TEXT NOT NULL,
    kind        TEXT NOT NULL DEFAULT '',
    brief       TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL DEFAULT 'open',
    created_at  INTEGER NOT NULL,
    resolved_at INTEGER NOT NULL DEFAULT 0,
    nudged_at   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (room_id, thread_root)
);
CREATE INDEX IF NOT EXISTS idx_threads_open ON threads (kind, state);
`

// migrations bring an existing database up to the current schema. CREATE TABLE IF
// NOT EXISTS only creates; it never alters, so a column added to the schema above is
// invisible to a database that already exists — which is every database but the
// first one.
//
// SQLite has no "ADD COLUMN IF NOT EXISTS", so an already-applied migration reports a
// duplicate column and is skipped. That keeps this list append-only and idempotent
// without a version table to get out of step.
var migrations = []string{
	`ALTER TABLE threads ADD COLUMN nudged_at INTEGER NOT NULL DEFAULT 0`,
}

func migrate(db *sql.DB) error {
	for _, stmt := range migrations {
		if _, err := db.Exec(stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

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
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
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

// ---- polls -------------------------------------------------------------

func (s *Store) SavePoll(ctx context.Context, p core.PollRecord) error {
	answers, err := json.Marshal(p.Answers)
	if err != nil {
		return err
	}
	// The poll start may be seen again on a sync replay; keep whatever close time
	// we already recorded rather than resetting it to zero.
	_, err = s.db.ExecContext(ctx, `
        INSERT INTO polls (event_id, room_id, sender, ts, question, answers, max_selections, closed_at)
        VALUES (?,?,?,?,?,?,?,?)
        ON CONFLICT(event_id) DO UPDATE SET
            question = excluded.question, answers = excluded.answers,
            max_selections = excluded.max_selections`,
		p.EventID, p.RoomID, p.Sender, p.Timestamp.UnixMilli(), p.Question,
		string(answers), p.MaxSelections, millis(p.ClosedAt))
	return err
}

func (s *Store) SavePollVote(ctx context.Context, v core.PollVote) error {
	answers, err := json.Marshal(v.AnswerIDs)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
        INSERT INTO poll_votes (event_id, poll_id, room_id, sender, ts, answers)
        VALUES (?,?,?,?,?,?) ON CONFLICT(event_id) DO NOTHING`,
		v.EventID, v.PollID, v.RoomID, v.Sender, v.Timestamp.UnixMilli(), string(answers))
	return err
}

// ClosePoll records when voting stopped. Only the first close counts: a later
// duplicate must not extend the window and let stale votes in.
func (s *Store) ClosePoll(ctx context.Context, roomID, pollID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE polls SET closed_at = ? WHERE room_id = ? AND event_id = ? AND closed_at = 0`,
		at.UnixMilli(), roomID, pollID)
	return err
}

func (s *Store) Poll(ctx context.Context, roomID, pollID string) (core.PollRecord, error) {
	var p core.PollRecord
	var ts, closedAt int64
	var answers string
	err := s.db.QueryRowContext(ctx, `
        SELECT event_id, room_id, sender, ts, question, answers, max_selections, closed_at
        FROM polls WHERE room_id = ? AND event_id = ?`, roomID, pollID).
		Scan(&p.EventID, &p.RoomID, &p.Sender, &ts, &p.Question, &answers, &p.MaxSelections, &closedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, core.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(answers), &p.Answers); err != nil {
		return p, err
	}
	p.Timestamp = time.UnixMilli(ts)
	if closedAt != 0 {
		p.ClosedAt = time.UnixMilli(closedAt)
	}
	return p, nil
}

func (s *Store) PollVotes(ctx context.Context, pollID string) ([]core.PollVote, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, poll_id, room_id, sender, ts, answers FROM poll_votes
         WHERE poll_id = ? ORDER BY ts`, pollID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []core.PollVote
	for rows.Next() {
		var v core.PollVote
		var ts int64
		var answers string
		if err := rows.Scan(&v.EventID, &v.PollID, &v.RoomID, &v.Sender, &ts, &answers); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(answers), &v.AnswerIDs); err != nil {
			return nil, err
		}
		v.Timestamp = time.UnixMilli(ts)
		out = append(out, v)
	}
	return out, rows.Err()
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// ---- agent sessions ----------------------------------------------------

// SessionFor returns the session to resume. An idle window of zero means never
// expire; otherwise a session untouched for longer is treated as ended, because
// resuming an ever-growing context costs more every turn and a conversation
// abandoned hours ago is rarely worth continuing.
func (s *Store) SessionFor(ctx context.Context, roomID, threadRoot string, idle time.Duration) (string, error) {
	var id string
	var updated int64
	err := s.db.QueryRowContext(ctx,
		`SELECT session_id, updated_at FROM engine_sessions WHERE room_id = ? AND thread_root = ?`,
		roomID, threadRoot).Scan(&id, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil // no session yet is normal, not an error
	}
	if err != nil {
		return "", err
	}
	if idle > 0 && time.Since(time.UnixMilli(updated)) > idle {
		return "", nil
	}
	return id, nil
}

func (s *Store) SetSession(ctx context.Context, roomID, threadRoot, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO engine_sessions (room_id, thread_root, session_id, updated_at)
        VALUES (?,?,?,?)
        ON CONFLICT(room_id, thread_root) DO UPDATE SET
            session_id = excluded.session_id, updated_at = excluded.updated_at`,
		roomID, threadRoot, sessionID, time.Now().UnixMilli())
	return err
}

// ---- clearing ----------------------------------------------------------

// ClearRoom removes everything momo remembers about a room: messages, reactions,
// polls, votes, and the agent session mappings.
//
// One transaction, because a half-cleared room is worse than either state — the
// agent would resume a conversation whose messages are gone.
func (s *Store) ClearRoom(ctx context.Context, roomID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, stmt := range []string{
		`DELETE FROM poll_votes WHERE room_id = ?`,
		`DELETE FROM polls WHERE room_id = ?`,
		`DELETE FROM reactions WHERE room_id = ?`,
		`DELETE FROM messages WHERE room_id = ?`,
		`DELETE FROM engine_sessions WHERE room_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, roomID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClearSessions forgets the agent sessions for a room without touching history, so
// the next message starts a fresh conversation but the transcript survives.
func (s *Store) ClearSessions(ctx context.Context, roomID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM engine_sessions WHERE room_id = ?`, roomID)
	return err
}

// ---- threads -----------------------------------------------------------

// MarkNudged records that we have already pushed on this thread today, so a daily
// sweep run twice does not nag twice.
func (s *Store) MarkNudged(ctx context.Context, roomID, threadRoot string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE threads SET nudged_at = ? WHERE room_id = ? AND thread_root = ?`,
		at.UnixMilli(), roomID, threadRoot)
	return err
}

func (s *Store) CreateThread(ctx context.Context, t core.Thread) error {
	if t.State == "" {
		t.State = core.ThreadOpen
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO threads (room_id, thread_root, kind, brief, state, created_at)
        VALUES (?,?,?,?,?,?)
        ON CONFLICT(room_id, thread_root) DO UPDATE SET
            kind = excluded.kind, brief = excluded.brief`,
		t.RoomID, t.ThreadRoot, t.Kind, t.Brief, string(t.State), t.CreatedAt.UnixMilli())
	return err
}

func (s *Store) Thread(ctx context.Context, roomID, threadRoot string) (core.Thread, error) {
	var t core.Thread
	var created, resolved, nudged int64
	var state string
	err := s.db.QueryRowContext(ctx, `
        SELECT room_id, thread_root, kind, brief, state, created_at, resolved_at, nudged_at
        FROM threads WHERE room_id = ? AND thread_root = ?`, roomID, threadRoot).
		Scan(&t.RoomID, &t.ThreadRoot, &t.Kind, &t.Brief, &state, &created, &resolved, &nudged)
	if errors.Is(err, sql.ErrNoRows) {
		return t, core.ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.State = core.ThreadState(state)
	t.CreatedAt = time.UnixMilli(created)
	if resolved != 0 {
		t.ResolvedAt = time.UnixMilli(resolved)
	}
	if nudged != 0 {
		t.NudgedAt = time.UnixMilli(nudged)
	}
	return t, nil
}

// OpenThreads lists outstanding work, oldest first — the order it should be worked
// in, and the order that makes a backlog obvious.
func (s *Store) OpenThreads(ctx context.Context, roomID, kind string) ([]core.Thread, error) {
	q := `SELECT room_id, thread_root, kind, brief, state, created_at, resolved_at, nudged_at
          FROM threads WHERE state = 'open'`
	var args []any
	if roomID != "" {
		q += " AND room_id = ?"
		args = append(args, roomID)
	}
	if kind != "" {
		q += " AND kind = ?"
		args = append(args, kind)
	}
	q += " ORDER BY created_at"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []core.Thread
	for rows.Next() {
		var t core.Thread
		var created, resolved, nudged int64
		var state string
		if err := rows.Scan(&t.RoomID, &t.ThreadRoot, &t.Kind, &t.Brief, &state, &created, &resolved, &nudged); err != nil {
			return nil, err
		}
		t.State = core.ThreadState(state)
		t.CreatedAt = time.UnixMilli(created)
		if resolved != 0 {
			t.ResolvedAt = time.UnixMilli(resolved)
		}
		if nudged != 0 {
			t.NudgedAt = time.UnixMilli(nudged)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetThreadState closes a thread, and optionally every other open thread of the same
// kind. That second part is the point: three unanswered inbox reminders are one
// overdue task, so doing it late settles the whole backlog rather than leaving two
// stale reminders to nag about work already done.
func (s *Store) SetThreadState(ctx context.Context, roomID, threadRoot string, state core.ThreadState, sameKind bool) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	res, err := tx.ExecContext(ctx,
		`UPDATE threads SET state = ?, resolved_at = ? WHERE room_id = ? AND thread_root = ?`,
		string(state), now, roomID, threadRoot)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	closed := int(n)

	if sameKind {
		var kind string
		err := tx.QueryRowContext(ctx,
			`SELECT kind FROM threads WHERE room_id = ? AND thread_root = ?`, roomID, threadRoot).Scan(&kind)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return closed, err
		}
		if kind != "" {
			res, err := tx.ExecContext(ctx, `
                UPDATE threads SET state = ?, resolved_at = ?
                WHERE room_id = ? AND kind = ? AND state = 'open' AND thread_root <> ?`,
				string(core.ThreadSuperseded), now, roomID, kind, threadRoot)
			if err != nil {
				return closed, err
			}
			n, _ := res.RowsAffected()
			closed += int(n)
		}
	}
	return closed, tx.Commit()
}
