package main

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
)

func siteRandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func sitePathExists(p string) bool { _, err := os.Lstat(p); return err == nil }
func siteNonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
func siteProcessPath(bin string) string {
	return filepath.Dir(bin) + ":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
}
func siteShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
