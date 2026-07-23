// Package core holds the domain types and the ports every adapter implements.
// It deliberately imports nothing from mautrix, SQLite or the CLI: the direction
// of dependency is always inward, so swapping the chat backend or the database
// touches an adapter and never this package.
package core

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

// Kind is what a message is, independent of any protocol's naming. Adapters map
// these onto their own wire types.
type Kind string

const (
	KindText   Kind = "text"
	KindNotice Kind = "notice" // bots use this so clients can style automated output
	KindEmote  Kind = "emote"
	KindImage  Kind = "image"
	KindAudio  Kind = "audio"
	KindVideo  Kind = "video"
	KindFile   Kind = "file"
)

// Message is one thing said in a room, in either direction.
type Message struct {
	EventID   string
	RoomID    string
	Sender    string
	Timestamp time.Time
	Kind      Kind
	Body      string
	HTML      string // empty when the message carries no formatted representation
	// ThreadRoot is the event that opened the thread this belongs to, if any.
	ThreadRoot string
	ReplyTo    string
	// URL points at attachment content for the media kinds.
	URL      string
	Filename string
	Redacted bool
	// Raw is the untouched protocol payload, so history keeps information the
	// domain model does not name yet.
	Raw []byte
}

// IsMedia reports whether the message carries an attachment.
func (m Message) IsMedia() bool {
	switch m.Kind {
	case KindImage, KindAudio, KindVideo, KindFile:
		return true
	}
	return false
}

// Reaction is an annotation on an event, keyed by the emoji used.
type Reaction struct {
	EventID   string
	RoomID    string
	Sender    string
	TargetID  string
	Key       string
	Timestamp time.Time
}

// Room is the subset of room metadata momo actually acts on.
type Room struct {
	ID          string
	Name        string
	Topic       string
	Encrypted   bool
	DirectWith  string // the other party's user ID when this is a DM
	MemberCount int
}

// Poll is MSC3381. Kept in the domain because the CLI exposes it, even though the
// wire format is still unstable.
type Poll struct {
	EventID  string
	RoomID   string
	Question string
	Answers  []string
	// MaxSelections is how many answers a voter may pick; 1 is a single-choice poll.
	MaxSelections int
	Disclosed     bool // whether running totals are visible before the poll ends
}

// PollAnswer is one option, with the opaque ID votes actually reference.
type PollAnswer struct {
	ID   string
	Text string
}

// PollRecord is a poll as observed in a room, which is more than we needed to send
// one: tallying requires the answer IDs and the moment it closed.
type PollRecord struct {
	EventID       string
	RoomID        string
	Sender        string
	Timestamp     time.Time
	Question      string
	Answers       []PollAnswer
	MaxSelections int
	// ClosedAt is zero while the poll is open. Votes after it do not count.
	ClosedAt time.Time
}

func (p PollRecord) Open() bool { return p.ClosedAt.IsZero() }

// PollVote is one person's answer. A voter may vote repeatedly; only their last
// vote counts, so these are kept rather than overwritten.
type PollVote struct {
	EventID   string
	PollID    string
	RoomID    string
	Sender    string
	Timestamp time.Time
	AnswerIDs []string
}

// PollCount is the result for a single answer.
type PollCount struct {
	Answer PollAnswer
	Votes  int
	Voters []string
}

// PollTally is a counted poll.
type PollTally struct {
	Poll   PollRecord
	Counts []PollCount
	Voters int // distinct people whose vote counted
}

// Tally counts a poll according to MSC3381: a voter's most recent valid vote
// replaces their earlier ones, votes cast after the poll closed are discarded, and
// selections beyond max_selections or naming unknown answers are ignored.
//
// Pure so the counting rules can be tested without a homeserver or a database.
func Tally(p PollRecord, votes []PollVote) PollTally {
	valid := map[string]PollAnswer{}
	for _, a := range p.Answers {
		valid[a.ID] = a
	}

	// Last vote per sender wins. Ties on timestamp fall back to event ID so the
	// result is stable rather than dependent on scan order.
	latest := map[string]PollVote{}
	for _, v := range votes {
		if !p.ClosedAt.IsZero() && v.Timestamp.After(p.ClosedAt) {
			continue
		}
		prev, seen := latest[v.Sender]
		if !seen || v.Timestamp.After(prev.Timestamp) ||
			(v.Timestamp.Equal(prev.Timestamp) && v.EventID > prev.EventID) {
			latest[v.Sender] = v
		}
	}

	max := p.MaxSelections
	if max < 1 {
		max = 1
	}
	counted := map[string][]string{}
	voters := 0
	for sender, v := range latest {
		picked := 0
		did := false
		for _, id := range v.AnswerIDs {
			if picked >= max {
				break
			}
			if _, ok := valid[id]; !ok {
				continue
			}
			counted[id] = append(counted[id], sender)
			picked++
			did = true
		}
		if did {
			voters++
		}
	}

	// Preserve the poll's own answer order; a tally that reorders itself between
	// runs is unreadable.
	out := PollTally{Poll: p, Voters: voters}
	for _, a := range p.Answers {
		v := counted[a.ID]
		sortStrings(v)
		out.Counts = append(out.Counts, PollCount{Answer: a, Votes: len(v), Voters: v})
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// SendOpts are the modifiers common to every outgoing message.
type SendOpts struct {
	ThreadRoot string
	ReplyTo    string
	Kind       Kind
	HTML       string
}

// Chat is the outbound half of a chat backend: everything momo can *do*.
//
// Split from Rooms and Sync so a caller that only sends does not depend on room
// administration or on the event loop — the interface-segregation half of SOLID,
// and the reason the CLI can construct far less than the daemon does.
type Chat interface {
	Send(ctx context.Context, roomID, body string, opts SendOpts) (eventID string, err error)
	SendMedia(ctx context.Context, roomID, path string, opts SendOpts) (eventID string, err error)
	React(ctx context.Context, roomID, targetID, key string) (eventID string, err error)
	Edit(ctx context.Context, roomID, targetID, body string, opts SendOpts) (eventID string, err error)
	Redact(ctx context.Context, roomID, targetID, reason string) (eventID string, err error)
	Typing(ctx context.Context, roomID string, typing bool) error
	MarkRead(ctx context.Context, roomID, eventID string) error
	StartPoll(ctx context.Context, roomID string, poll Poll) (eventID string, err error)
	EndPoll(ctx context.Context, roomID, pollID string) (eventID string, err error)
}

// Rooms is room membership and metadata.
type Rooms interface {
	List(ctx context.Context) ([]Room, error)
	Get(ctx context.Context, roomID string) (Room, error)
	Join(ctx context.Context, roomIDOrAlias string) (string, error)
	Leave(ctx context.Context, roomID string) error
	Invite(ctx context.Context, roomID, userID string) error
	WhoAmI(ctx context.Context) (userID, deviceID string, err error)
}

// HistoryFilter narrows a history query. Zero values mean "no constraint".
type HistoryFilter struct {
	RoomID     string
	ThreadRoot string
	Sender     string
	Since      time.Time
	Limit      int
}

// History is the durable record of what was said. It is deliberately separate from
// the chat backend: the protocol's own history is remote, paginated and, for our own
// messages, undecryptable. This one is local and queryable.
type History interface {
	SaveMessage(ctx context.Context, m Message) error
	SaveReaction(ctx context.Context, r Reaction) error
	MarkRedacted(ctx context.Context, roomID, eventID string) error
	Messages(ctx context.Context, f HistoryFilter) ([]Message, error)
	Reactions(ctx context.Context, roomID, targetID string) ([]Reaction, error)

	SavePoll(ctx context.Context, p PollRecord) error
	SavePollVote(ctx context.Context, v PollVote) error
	ClosePoll(ctx context.Context, roomID, pollID string, at time.Time) error
	Poll(ctx context.Context, roomID, pollID string) (PollRecord, error)
	PollVotes(ctx context.Context, pollID string) ([]PollVote, error)

	Close() error
}

// Engine turns a prompt into a reply. The bot does not care whether that is Claude
// Code, an echo stub, or something else later.
type Engine interface {
	Run(ctx context.Context, prompt string) string
	Name() string
}
