package main

import (
	"fmt"
	"strings"
)

// ChunkMessage splits text into chunks of at most maxSize characters.
// When multiple chunks are produced each one is prefixed with "[N/Total] ".
func ChunkMessage(text string, maxSize int) []string {
	if maxSize <= 0 {
		maxSize = 3900
	}

	// Fast path: fits in one chunk.
	if len(text) <= maxSize {
		return []string{text}
	}

	chunks := splitText(text, maxSize)

	if len(chunks) <= 1 {
		return chunks
	}

	// Add [1/N] prefixes.
	total := len(chunks)
	prefixed := make([]string, 0, total)
	for i, c := range chunks {
		prefix := fmt.Sprintf("[%d/%d] ", i+1, total)
		// If adding prefix would exceed maxSize, hard-trim the body.
		body := c
		if len(prefix)+len(body) > maxSize {
			body = body[:maxSize-len(prefix)]
		}
		prefixed = append(prefixed, prefix+body)
	}
	return prefixed
}

// splitText does the actual splitting logic without prefixes.
func splitText(text string, maxSize int) []string {
	if len(text) <= maxSize {
		return []string{text}
	}

	// Strategy 1: split at double newlines (paragraphs).
	if chunks := trySplit(text, "\n\n", maxSize); chunks != nil {
		return chunks
	}

	// Strategy 2: split at single newlines.
	if chunks := trySplit(text, "\n", maxSize); chunks != nil {
		return chunks
	}

	// Strategy 3: split at sentence boundaries.
	for _, sep := range []string{". ", "! ", "? "} {
		if chunks := trySplitSentence(text, sep, maxSize); chunks != nil {
			return chunks
		}
	}

	// Strategy 4: hard split.
	return hardSplit(text, maxSize)
}

// trySplit attempts to split text at sep so that each chunk fits within maxSize.
// Returns nil if any individual segment already exceeds maxSize (caller should try finer split).
func trySplit(text, sep string, maxSize int) []string {
	parts := strings.Split(text, sep)

	var chunks []string
	current := ""

	for i, part := range parts {
		// The piece we would add (restoring the separator between parts).
		piece := part
		if i < len(parts)-1 {
			piece = part + sep
		}

		if len(piece) > maxSize {
			// This individual segment is too long — signal caller to try finer split.
			return nil
		}

		if len(current)+len(piece) > maxSize {
			if current != "" {
				chunks = append(chunks, strings.TrimRight(current, " \n"))
			}
			current = piece
		} else {
			current += piece
		}
	}

	if strings.TrimSpace(current) != "" {
		chunks = append(chunks, strings.TrimRight(current, " \n"))
	}
	return chunks
}

// trySplitSentence is like trySplit but re-attaches the separator to the preceding sentence.
func trySplitSentence(text, sep string, maxSize int) []string {
	parts := strings.Split(text, sep)
	if len(parts) == 1 {
		return nil
	}

	var chunks []string
	current := ""

	for i, part := range parts {
		piece := part
		if i < len(parts)-1 {
			// re-attach separator (e.g. ". ") to the sentence end.
			piece = part + sep
		}

		if len(piece) > maxSize {
			return nil
		}

		if len(current)+len(piece) > maxSize {
			if current != "" {
				chunks = append(chunks, strings.TrimSpace(current))
			}
			current = piece
		} else {
			current += piece
		}
	}

	if strings.TrimSpace(current) != "" {
		chunks = append(chunks, strings.TrimSpace(current))
	}

	if len(chunks) == 1 {
		return nil // no actual split occurred
	}
	return chunks
}

// hardSplit cuts text at exact maxSize boundaries.
func hardSplit(text string, maxSize int) []string {
	var chunks []string
	runes := []rune(text)
	for len(runes) > 0 {
		end := maxSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}
	return chunks
}
