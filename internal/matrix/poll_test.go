package matrix

import (
	"encoding/json"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
)

// The fallback text must sit beside the poll object, not inside the question.
// Getting this wrong makes every client repeat the answer list inside the question.
func TestPollStartShape(t *testing.T) {
	c := pollStartContent{
		PollStartEventContent: &event.PollStartEventContent{
			PollStart: event.PollStart{
				Kind:          pollKindUndisclosed,
				MaxSelections: 1,
				Question:      event.MSC1767Message{Text: "Ship it?"},
				Answers: []event.PollOption{
					{ID: "answer-0", MSC1767Message: event.MSC1767Message{Text: "yes"}},
					{ID: "answer-1", MSC1767Message: event.MSC1767Message{Text: "no"}},
				},
			},
		},
		Text: "Ship it?\n1. yes\n2. no",
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	// The embedded struct's fields must be promoted to the top level, not nested
	// under a "PollStartEventContent" key.
	start, ok := got["org.matrix.msc3381.poll.start"].(map[string]any)
	if !ok {
		t.Fatalf("poll.start missing from content: %s", raw)
	}
	if _, ok := got["org.matrix.msc1767.text"].(string); !ok {
		t.Fatalf("top-level fallback text missing: %s", raw)
	}
	question := start["question"].(map[string]any)["org.matrix.msc1767.text"].(string)
	if question != "Ship it?" {
		t.Fatalf("question is %q, want just the question", question)
	}
	if strings.Contains(question, "yes") {
		t.Fatalf("answers leaked into the question: %q", question)
	}
	if n := len(start["answers"].([]any)); n != 2 {
		t.Fatalf("got %d answers, want 2", n)
	}
}
