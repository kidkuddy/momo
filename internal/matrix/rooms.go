package matrix

import (
	"context"
	"html"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/kidkuddy/momo/internal/core"
)

// Rooms implements core.Rooms.
type Rooms struct{ c *Client }

func NewRooms(c *Client) *Rooms { return &Rooms{c: c} }

func (r *Rooms) List(ctx context.Context) ([]core.Room, error) {
	resp, err := r.c.mx.JoinedRooms(ctx)
	if err != nil {
		return nil, err
	}
	direct := r.directChats(ctx)
	out := make([]core.Room, 0, len(resp.JoinedRooms))
	for _, roomID := range resp.JoinedRooms {
		room := r.describe(ctx, roomID, direct)
		out = append(out, room)
	}
	return out, nil
}

func (r *Rooms) Get(ctx context.Context, roomID string) (core.Room, error) {
	return r.describe(ctx, id.RoomID(roomID), r.directChats(ctx)), nil
}

// describe gathers metadata best-effort: a room the bot can see but whose state it
// cannot read should still be listed, just with blanks.
func (r *Rooms) describe(ctx context.Context, roomID id.RoomID, direct map[id.RoomID]id.UserID) core.Room {
	room := core.Room{ID: roomID.String()}

	var name event.RoomNameEventContent
	if err := r.c.mx.StateEvent(ctx, roomID, event.StateRoomName, "", &name); err == nil {
		room.Name = name.Name
	}
	var topic event.TopicEventContent
	if err := r.c.mx.StateEvent(ctx, roomID, event.StateTopic, "", &topic); err == nil {
		room.Topic = topic.Topic
	}
	if enc, err := r.c.mx.StateStore.IsEncrypted(ctx, roomID); err == nil {
		room.Encrypted = enc
	}
	if members, err := r.c.mx.JoinedMembers(ctx, roomID); err == nil {
		room.MemberCount = len(members.Joined)
	}
	if who, ok := direct[roomID]; ok {
		room.DirectWith = who.String()
	}
	return room
}

// directChats inverts m.direct, which maps a user to their DM rooms.
func (r *Rooms) directChats(ctx context.Context) map[id.RoomID]id.UserID {
	out := map[id.RoomID]id.UserID{}
	var content map[id.UserID][]id.RoomID
	if err := r.c.mx.GetAccountData(ctx, "m.direct", &content); err != nil {
		return out
	}
	for user, rooms := range content {
		for _, room := range rooms {
			out[room] = user
		}
	}
	return out
}

func (r *Rooms) Join(ctx context.Context, roomIDOrAlias string) (string, error) {
	resp, err := r.c.mx.JoinRoom(ctx, roomIDOrAlias, nil)
	if err != nil {
		return "", err
	}
	return resp.RoomID.String(), nil
}

func (r *Rooms) Leave(ctx context.Context, roomID string) error {
	if _, err := r.c.mx.LeaveRoom(ctx, id.RoomID(roomID)); err != nil {
		return err
	}
	// Forget too, so the room stops appearing in future syncs.
	_, err := r.c.mx.ForgetRoom(ctx, id.RoomID(roomID))
	return err
}

func (r *Rooms) Invite(ctx context.Context, roomID, userID string) error {
	_, err := r.c.mx.InviteUser(ctx, id.RoomID(roomID), &mautrix.ReqInviteUser{UserID: id.UserID(userID)})
	return err
}

func (r *Rooms) WhoAmI(ctx context.Context) (string, string, error) {
	return r.c.mx.UserID.String(), r.c.mx.DeviceID.String(), nil
}

// FormatHTML turns the small amount of markdown that engine output actually uses
// into HTML. Fenced blocks become <pre><code> so code reads as code; everything
// else is escaped, because engine output is untrusted text that lands in a room.
func FormatHTML(s string) string {
	if !strings.Contains(s, "```") {
		return ""
	}
	parts := strings.Split(s, "```")
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			p = strings.TrimPrefix(p, "\n")
			if nl := strings.IndexByte(p, '\n'); nl >= 0 && !strings.Contains(p[:nl], " ") {
				p = p[nl+1:] // drop the language tag line
			}
			b.WriteString("<pre><code>" + html.EscapeString(p) + "</code></pre>")
			continue
		}
		b.WriteString(strings.ReplaceAll(html.EscapeString(p), "\n", "<br/>"))
	}
	return b.String()
}
