package main

import (
	"bytes"
	"crypto/sha256"
)

// matchVote returns the options a vote chose. WhatsApp sends the SHA-256
// hashes of the chosen options' text, not the text; a hash that matches
// none is dropped.
func matchVote(hashes [][]byte, options []string) []string {
	var selected []string
	for _, hash := range hashes {
		for _, opt := range options {
			h := sha256.Sum256([]byte(opt))
			if bytes.Equal(hash, h[:]) {
				selected = append(selected, opt)
				break
			}
		}
	}
	return selected
}
