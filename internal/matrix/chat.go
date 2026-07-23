package matrix

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/kidkuddy/momo/internal/core"
)

// Chat implements core.Chat.
type Chat struct{ c *Client }

func NewChat(c *Client) *Chat { return &Chat{c: c} }

// MaxBody is the largest text we put in one event. Matrix caps events near 64KB;
// staying well under leaves room for formatting and relation metadata.
const MaxBody = 32 << 10

func (ch *Chat) Send(ctx context.Context, roomID, body string, opts core.SendOpts) (string, error) {
	content := textContent(body, opts)
	return ch.send(ctx, roomID, event.EventMessage, content)
}

// SendMedia uploads a file and posts it. In an encrypted room the bytes are
// encrypted client-side first, because the homeserver must never see them.
func (ch *Chat) SendMedia(ctx context.Context, roomID, path string, opts core.SendOpts) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	name := filepath.Base(path)
	mimeType := detectMIME(path, data)

	content := &event.MessageEventContent{
		MsgType: mediaMsgType(mimeType, opts.Kind),
		Body:    name,
		Info: &event.FileInfo{
			MimeType: mimeType,
			Size:     len(data),
		},
	}
	setRelations(content, opts)

	encrypted, err := ch.roomIsEncrypted(ctx, roomID)
	if err != nil {
		return "", err
	}
	if encrypted {
		file := attachment.NewEncryptedFile()
		file.EncryptInPlace(data)
		resp, err := ch.c.mx.UploadBytesWithName(ctx, data, "application/octet-stream", name)
		if err != nil {
			return "", fmt.Errorf("upload: %w", err)
		}
		content.File = &event.EncryptedFileInfo{
			EncryptedFile: *file,
			URL:           resp.ContentURI.CUString(),
		}
	} else {
		resp, err := ch.c.mx.UploadBytesWithName(ctx, data, mimeType, name)
		if err != nil {
			return "", fmt.Errorf("upload: %w", err)
		}
		content.URL = resp.ContentURI.CUString()
	}
	return ch.send(ctx, roomID, event.EventMessage, content)
}

func (ch *Chat) React(ctx context.Context, roomID, targetID, key string) (string, error) {
	// Reactions are never encrypted: the key would leak from the aggregation anyway,
	// and the spec keeps them in the clear.
	resp, err := ch.c.mx.SendMessageEvent(ctx, id.RoomID(roomID), event.EventReaction,
		&event.ReactionEventContent{
			RelatesTo: event.RelatesTo{
				Type:    event.RelAnnotation,
				EventID: id.EventID(targetID),
				Key:     key,
			},
		})
	if err != nil {
		return "", err
	}
	return resp.EventID.String(), nil
}

// Edit replaces an earlier message. The top-level body carries a "* " fallback for
// clients that do not understand replacements; the real new content is in m.new_content.
func (ch *Chat) Edit(ctx context.Context, roomID, targetID, body string, opts core.SendOpts) (string, error) {
	newContent := textContent(body, core.SendOpts{Kind: opts.Kind, HTML: opts.HTML})
	content := textContent("* "+body, core.SendOpts{Kind: opts.Kind})
	content.NewContent = newContent
	content.RelatesTo = &event.RelatesTo{
		Type:    event.RelReplace,
		EventID: id.EventID(targetID),
	}
	return ch.send(ctx, roomID, event.EventMessage, content)
}

func (ch *Chat) Redact(ctx context.Context, roomID, targetID, reason string) (string, error) {
	resp, err := ch.c.mx.RedactEvent(ctx, id.RoomID(roomID), id.EventID(targetID),
		mautrix.ReqRedact{Reason: reason})
	if err != nil {
		return "", err
	}
	return resp.EventID.String(), nil
}

func (ch *Chat) Typing(ctx context.Context, roomID string, typing bool) error {
	_, err := ch.c.mx.UserTyping(ctx, id.RoomID(roomID), typing, 30*time.Second)
	return err
}

func (ch *Chat) MarkRead(ctx context.Context, roomID, eventID string) error {
	return ch.c.mx.MarkRead(ctx, id.RoomID(roomID), id.EventID(eventID))
}

// ---- polls (MSC3381, unstable) -----------------------------------------

func (ch *Chat) StartPoll(ctx context.Context, roomID string, poll core.Poll) (string, error) {
	if len(poll.Answers) < 2 {
		return "", fmt.Errorf("a poll needs at least two answers")
	}
	// Undisclosed hides running totals until the poll ends, which is the safer
	// default for an approval prompt.
	kind := pollKindUndisclosed
	if poll.Disclosed {
		kind = pollKindDisclosed
	}
	answers := make([]event.PollOption, len(poll.Answers))
	for i, a := range poll.Answers {
		answers[i] = event.PollOption{
			ID:             fmt.Sprintf("answer-%d", i),
			MSC1767Message: event.MSC1767Message{Text: a},
		}
	}
	max := poll.MaxSelections
	if max < 1 {
		max = 1
	}
	content := pollStartContent{
		PollStartEventContent: &event.PollStartEventContent{
			PollStart: event.PollStart{
				Kind:          kind,
				MaxSelections: max,
				Question:      event.MSC1767Message{Text: poll.Question},
				Answers:       answers,
			},
		},
		Text: fallbackPollText(poll),
	}
	return ch.send(ctx, roomID, event.EventUnstablePollStart, content)
}

// pollStartContent adds the top-level fallback text MSC3381 asks for, which
// mautrix's struct has no field for. It must sit beside the poll object, not inside
// the question: a client that understands polls renders the question, and one that
// does not falls back to this. Putting the fallback in the question makes every
// client repeat the answers inside the question text.
type pollStartContent struct {
	*event.PollStartEventContent
	Text string `json:"org.matrix.msc1767.text,omitempty"`
}

// EndPoll closes a poll. mautrix has no struct for the end event, so this is the
// raw MSC3381 shape.
func (ch *Chat) EndPoll(ctx context.Context, roomID, pollID string) (string, error) {
	content := map[string]any{
		"m.relates_to": map[string]any{
			"rel_type": string(event.RelReference),
			"event_id": pollID,
		},
		"org.matrix.msc3381.poll.end": map[string]any{},
		"org.matrix.msc1767.text":     "The poll has closed.",
	}
	return ch.send(ctx, roomID, event.EventUnstablePollEnd, content)
}

const (
	pollKindDisclosed   = "org.matrix.msc3381.poll.disclosed"
	pollKindUndisclosed = "org.matrix.msc3381.poll.undisclosed"
)

// pollEndContent exists only so mautrix can parse the event. It has no struct for
// poll ends, so every one decrypts with a WRN "Unsupported event type", which reads
// like a fault and is not. The relation is still read from the raw content.
type pollEndContent struct {
	RelatesTo *event.RelatesTo `json:"m.relates_to,omitempty"`
	Text      string           `json:"org.matrix.msc1767.text,omitempty"`
}

func init() {
	event.TypeMap[event.EventUnstablePollEnd] = reflect.TypeOf(pollEndContent{})
}

func fallbackPollText(p core.Poll) string {
	var b strings.Builder
	b.WriteString(p.Question)
	for i, a := range p.Answers {
		fmt.Fprintf(&b, "\n%d. %s", i+1, a)
	}
	return b.String()
}

// ---- internals ---------------------------------------------------------

func (ch *Chat) send(ctx context.Context, roomID string, evtType event.Type, content any) (string, error) {
	resp, err := ch.c.mx.SendMessageEvent(ctx, id.RoomID(roomID), evtType, content)
	if err != nil {
		return "", err
	}
	return resp.EventID.String(), nil
}

func (ch *Chat) roomIsEncrypted(ctx context.Context, roomID string) (bool, error) {
	return ch.c.mx.StateStore.IsEncrypted(ctx, id.RoomID(roomID))
}

func textContent(body string, opts core.SendOpts) *event.MessageEventContent {
	content := &event.MessageEventContent{MsgType: msgType(opts.Kind), Body: body}
	html := opts.HTML
	if html == "" {
		html = FormatHTML(body)
	}
	if html != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = html
	}
	setRelations(content, opts)
	return content
}

func setRelations(content *event.MessageEventContent, opts core.SendOpts) {
	switch {
	case opts.ThreadRoot != "":
		rel := &event.RelatesTo{}
		rel.SetThread(id.EventID(opts.ThreadRoot), id.EventID(opts.ReplyTo))
		content.RelatesTo = rel
	case opts.ReplyTo != "":
		content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(id.EventID(opts.ReplyTo))
	}
}

func msgType(k core.Kind) event.MessageType {
	switch k {
	case core.KindNotice:
		return event.MsgNotice
	case core.KindEmote:
		return event.MsgEmote
	default:
		return event.MsgText
	}
}

// mediaMsgType prefers an explicit request, else infers from the MIME type so an
// image arrives as an image rather than a generic attachment.
func mediaMsgType(mimeType string, want core.Kind) event.MessageType {
	switch want {
	case core.KindImage:
		return event.MsgImage
	case core.KindAudio:
		return event.MsgAudio
	case core.KindVideo:
		return event.MsgVideo
	case core.KindFile:
		return event.MsgFile
	}
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return event.MsgImage
	case strings.HasPrefix(mimeType, "audio/"):
		return event.MsgAudio
	case strings.HasPrefix(mimeType, "video/"):
		return event.MsgVideo
	default:
		return event.MsgFile
	}
}

func detectMIME(path string, data []byte) string {
	if t := mime.TypeByExtension(filepath.Ext(path)); t != "" {
		return strings.SplitN(t, ";", 2)[0]
	}
	// Sniffing needs at most 512 bytes and never fails, it just falls back to
	// application/octet-stream.
	return strings.SplitN(http.DetectContentType(head(data, 512)), ";", 2)[0]
}

func head(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}

// Chunk splits text on line boundaries so no event exceeds max bytes. A single line
// longer than the limit is cut hard rather than dropped.
func Chunk(s string, max int) []string {
	if len(s) <= max {
		return []string{s}
	}
	var out []string
	for len(s) > max {
		cut := bytes.LastIndexByte([]byte(s[:max]), '\n')
		if cut <= 0 {
			cut = max
		}
		out = append(out, s[:cut])
		s = strings.TrimPrefix(s[cut:], "\n")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
