package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTextWithQuote(t *testing.T) {
	q := &Quote{From: "15559876543", Text: `Lunch at "12"?`}
	cases := []struct {
		name string
		m    Inbound
		want string
	}{
		{"no quote", Inbound{Text: "hello"}, "hello"},
		{"quote and text", Inbound{Text: "yes", Quoted: q}, `[reply to 15559876543: "Lunch at \"12\"?"]` + "\nyes"},
		// A group reply that was only the trigger: the quote is the content.
		{"quote only", Inbound{Quoted: q}, `[reply to 15559876543: "Lunch at \"12\"?"]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.m.textWithQuote(); got != c.want {
				t.Errorf("textWithQuote() = %q, want %q", got, c.want)
			}
		})
	}
}

// Merging keeps the text the model sees the same as when each part carried
// its own "[reply to …]" line, and keeps the first quote apart.
func TestMergeKeepsQuotes(t *testing.T) {
	q1 := &Quote{From: "15559876543", Text: "Lunch?"}
	q2 := &Quote{From: "15551234567", Text: "Dinner?"}
	cases := []struct {
		name       string
		first      Inbound
		next       Inbound
		wantText   string
		wantQuoted *Quote
		wantModel  string
	}{
		{"plain parts", Inbound{Text: "a"}, Inbound{Text: "b"}, "a\n\nb", nil, "a\n\nb"},
		{"file, then a reply", Inbound{Media: []string{"/m/1.jpg"}}, Inbound{Text: "what is this?", Quoted: q1}, "what is this?", q1,
			"[reply to 15559876543: \"Lunch?\"]\nwhat is this?"},
		{"reply, then plain text", Inbound{Text: "yes", Quoted: q1}, Inbound{Text: "at 12"}, "yes\n\nat 12", q1,
			"[reply to 15559876543: \"Lunch?\"]\nyes\n\nat 12"},
		{"plain text, then a reply", Inbound{Text: "hi"}, Inbound{Text: "yes", Quoted: q1}, "hi\n\n[reply to 15559876543: \"Lunch?\"]\nyes", nil,
			"hi\n\n[reply to 15559876543: \"Lunch?\"]\nyes"},
		{"two replies", Inbound{Text: "yes", Quoted: q1}, Inbound{Text: "no", Quoted: q2}, "yes\n\n[reply to 15551234567: \"Dinner?\"]\nno", q1,
			"[reply to 15559876543: \"Lunch?\"]\nyes\n\n[reply to 15551234567: \"Dinner?\"]\nno"},
		{"the same reply twice", Inbound{Text: "yes", Quoted: q1}, Inbound{Text: "yes", Quoted: q1}, "yes", q1,
			"[reply to 15559876543: \"Lunch?\"]\nyes"},
		{"trigger-only reply, then text", Inbound{Quoted: q1}, Inbound{Text: "and this"}, "and this", q1,
			"[reply to 15559876543: \"Lunch?\"]\nand this"},
		{"the same trigger-only reply twice", Inbound{Quoted: q1}, Inbound{Quoted: q1}, "", q1,
			"[reply to 15559876543: \"Lunch?\"]"},
		{"trigger-only reply, then another", Inbound{Quoted: q1}, Inbound{Quoted: q2}, "[reply to 15551234567: \"Dinner?\"]", q1,
			"[reply to 15559876543: \"Lunch?\"]\n[reply to 15551234567: \"Dinner?\"]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.first.merge(c.next)
			if got.Text != c.wantText {
				t.Errorf("Text = %q, want %q", got.Text, c.wantText)
			}
			if got.Quoted != c.wantQuoted {
				t.Errorf("Quoted = %+v, want %+v", got.Quoted, c.wantQuoted)
			}
			if m := got.textWithQuote(); m != c.wantModel {
				t.Errorf("textWithQuote() = %q, want %q", m, c.wantModel)
			}
		})
	}
}

// Group chats are told apart, and DM chats built, by the server names
// whatsmeow uses.
func TestJIDHelpers(t *testing.T) {
	for jid, want := range map[string]bool{
		"120363000000000000@g.us":    true,
		"15551234567@s.whatsapp.net": false,
		"15551234567@lid":            false,
		"status@broadcast":           false,
		"g.us":                       false,
		"":                           false,
	} {
		if got := isGroupJID(jid); got != want {
			t.Errorf("isGroupJID(%q) = %v, want %v", jid, got, want)
		}
		if got := (Inbound{JID: jid}).IsGroup(); got != want {
			t.Errorf("Inbound{JID: %q}.IsGroup() = %v, want %v", jid, got, want)
		}
	}
	if got := userJID("15551234567"); got != "15551234567@s.whatsapp.net" {
		t.Errorf("userJID = %q", got)
	}
}

// Nothing else tells a group chat by the end of its JID, so no check can
// drift from isGroupJID's, which is whatsmeow's types.GroupServer. It reads
// every Go file of the module, not just this package's, so the check still
// covers them all once the code is split into packages.
func TestGroupJIDsAreToldApartInOnePlace(t *testing.T) {
	bySuffix := regexp.MustCompile(`HasSuffix\(.*("@g\.us"|types\.GroupServer)`)
	root := repoDir
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		if filepath.Dir(root) == root {
			t.Fatalf("no go.mod above %s", repoDir)
		}
		root = filepath.Dir(root)
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "node_modules"):
			return filepath.SkipDir
		case !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go"):
			files = append(files, path)
		}
		return nil
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files in %s: %v", root, err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if bySuffix.MatchString(line) && !strings.HasPrefix(line, "func isGroupJID(") {
				rel, _ := filepath.Rel(root, f)
				t.Errorf("%s:%d tells a group chat by its suffix; call isGroupJID: %s", rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}
