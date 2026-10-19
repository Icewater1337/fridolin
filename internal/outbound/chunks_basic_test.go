package outbound

import (
	"strings"

	"testing"

	"unicode/utf8"
)

func TestChunkMessageShortTextUntouched(t *testing.T) {
	chunks := Chunk("hello", 100)
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("got %v", chunks)
	}
}

func TestChunkMessageRespectsMaxSize(t *testing.T) {
	text := strings.Repeat("paragraph one is here.\n\n", 200)
	chunks := Chunk(text, 500)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if n := utf8.RuneCountInString(c); n > 500 {
			t.Errorf("chunk %d has %d characters, over the 500 limit", i, n)
		}
		if !strings.HasPrefix(c, "[") {
			t.Errorf("chunk %d missing [n/total] prefix: %q", i, c[:20])
		}
	}
}
