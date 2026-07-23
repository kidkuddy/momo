package matrix

import (
	"strings"
	"testing"
)

func TestFormatHTML(t *testing.T) {
	if got := FormatHTML("plain text"); got != "" {
		t.Fatalf("plain text should produce no html: %q", got)
	}
	got := FormatHTML("see:\n```go\nif x < 1 {}\n```")
	if !strings.Contains(got, "<pre><code>") {
		t.Fatalf("no code block: %q", got)
	}
	// Engine output is untrusted text landing in a room; it must be escaped.
	if !strings.Contains(got, "if x &lt; 1 {}") {
		t.Fatalf("code not escaped: %q", got)
	}
	if strings.Contains(got, "```") {
		t.Fatalf("fence leaked: %q", got)
	}
}

func TestChunk(t *testing.T) {
	if got := Chunk("short", 100); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short text split: %v", got)
	}
	body := strings.Repeat("0123456789\n", 100)
	got := Chunk(body, 100)
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(got))
	}
	for i, c := range got {
		if len(c) > 100 {
			t.Fatalf("chunk %d over limit: %d bytes", i, len(c))
		}
	}
	// Each cut consumes exactly one newline, which the join puts back.
	if rejoined := strings.Join(got, "\n"); rejoined != body {
		t.Fatalf("content lost: %d vs %d bytes", len(rejoined), len(body))
	}
	// A single line longer than the limit must be cut, not dropped.
	if long := Chunk(strings.Repeat("x", 250), 100); len(long) != 3 {
		t.Fatalf("hard split: %d chunks", len(long))
	}
}
