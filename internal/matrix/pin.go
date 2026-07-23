package matrix

import (
	"context"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Pin adds an event to the room's pinned list, so outstanding work is one tap away
// rather than buried under later conversation.
//
// Pinning is room state, so it needs the power level for m.room.pinned_events —
// usually 50. momo is an admin in a DM it was invited to, but not necessarily in a
// group room, so this fails softly and the caller carries on.
func (c *Client) Pin(ctx context.Context, roomID, eventID string) error {
	pinned, err := c.pinnedEvents(ctx, roomID)
	if err != nil {
		return err
	}
	for _, e := range pinned {
		if e.String() == eventID {
			return nil // already pinned
		}
	}
	return c.setPinned(ctx, roomID, append(pinned, id.EventID(eventID)))
}

func (c *Client) Unpin(ctx context.Context, roomID, eventID string) error {
	pinned, err := c.pinnedEvents(ctx, roomID)
	if err != nil {
		return err
	}
	out := pinned[:0]
	for _, e := range pinned {
		if e.String() != eventID {
			out = append(out, e)
		}
	}
	if len(out) == len(pinned) {
		return nil
	}
	return c.setPinned(ctx, roomID, out)
}

func (c *Client) pinnedEvents(ctx context.Context, roomID string) ([]id.EventID, error) {
	var content event.PinnedEventsEventContent
	err := c.mx.StateEvent(ctx, id.RoomID(roomID), event.StatePinnedEvents, "", &content)
	if err != nil {
		// No pinned list yet is the common case in a fresh room, not a failure.
		return nil, nil
	}
	return content.Pinned, nil
}

func (c *Client) setPinned(ctx context.Context, roomID string, pinned []id.EventID) error {
	_, err := c.mx.SendStateEvent(ctx, id.RoomID(roomID), event.StatePinnedEvents, "",
		&event.PinnedEventsEventContent{Pinned: pinned})
	return err
}
