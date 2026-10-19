package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// catalogue is every message of messages.go, by the name it has there,
// as a chat gets it: each function once for each way it reads.
func catalogue() []struct{ name, text string } {
	return []struct{ name, text string }{
		{"noBrainNotice", noBrainNotice},
		{"heldBrainNotice", heldBrainNotice},
		{"restartedNotice", restartedNotice},
		{"resetReply(false)", resetReply(false)},
		{"resetReply(true)", resetReply(true)},
		{"stopReply(true, 0)", stopReply(true, 0)},
		{"stopReply(false, 2)", stopReply(false, 2)},
		{"stopReply(false, 0)", stopReply(false, 0)},
		{`helpReply("")`, helpReply("")},
		{`helpReply("@claude")`, helpReply("@claude")},
		{"queuedNotice(2)", queuedNotice(2)},
		{"queueFullNotice(\"@claude !stop\")", queueFullNotice("@claude !stop")},
		{"otpCaughtNotice", otpCaughtNotice},
		{"workingNotice", workingNotice},
		{"questionPrefix", questionPrefix + "Which day?"},
		{"chatErrNotSignedIn", chatErrNotSignedIn},
		{"chatErrCodexNotSignedIn", chatErrCodexNotSignedIn},
		{"chatErrCodexLimit", chatErrCodexLimit},
		{"chatErrLimit", chatErrLimit},
		{"chatErrTimeout", chatErrTimeout},
		{"chatErrGeneric", chatErrGeneric},
		{"chatErrSandbox", chatErrSandbox(errors.New("the sandbox could not be set up"))},
		{"chatErrClaude", chatErrClaude("Credit balance is too low")},
		{"chatErrCodex", chatErrCodex("Something failed")},
		{"buildChatError", buildChatError(errors.New("brain queue is full"))},
		{"viewOnceNotice", viewOnceNotice},
		{"mediaTooLargeNotice", mediaTooLargeNotice(100 << 20)},
		{"mediaFailedNotice", mediaFailedNotice},
		{"pollAsText", pollAsText(pollData{Question: "Lunch on Friday?", Options: []string{"Pizza", "Sushi"}})},
		{"startupCheckAlert", startupCheckAlert("missing ~/.fridolin/credentials/google_calendar_token_main.json")},
		{"backupRefusedAlert", backupRefusedAlert("Notes", "it would delete 12 of 40 files")},
		{"plaudGivenUpAlert", plaudGivenUpAlert("Team sync", "notes", "brain notes is not configured")},
		{"plaudExpiredAlert", plaudExpiredAlert},
	}
}

// TestGoldenMessages pins every message the bridge sends into chats, so a
// change to one shows in review, and holds the catalogue to its rules:
// each message starts with an emoji and a space, and every name
// messages.go declares is in it.
func TestGoldenMessages(t *testing.T) {
	var out strings.Builder
	named := map[string]bool{}
	for _, m := range catalogue() {
		name, _, _ := strings.Cut(m.name, "(")
		named[name] = true
		out.WriteString(m.name + "\n")
		for _, line := range strings.Split(m.text, "\n") {
			if line != "" {
				line = "\t" + line
			}
			out.WriteString(line + "\n")
		}
		if r, size := utf8.DecodeRuneInString(m.text); !isEmojiStart(r) || !strings.HasPrefix(strings.TrimPrefix(m.text[size:], "️"), " ") {
			t.Errorf("%s does not start with an emoji and a space: %q", m.name, m.text)
		}
	}
	assertGolden(t, "messages.golden", []byte(out.String()))

	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoDir, "messages.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range topLevelNames(f) {
		if !named[name] {
			t.Errorf("messages.go declares %s, which catalogue() does not list", name)
		}
	}
}

// topLevelNames are the names a file declares at the top level.
func topLevelNames(f *ast.File) []string {
	var names []string
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			names = append(names, d.Name.Name)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if v, ok := spec.(*ast.ValueSpec); ok {
					for _, n := range v.Names {
						names = append(names, n.Name)
					}
				}
			}
		}
	}
	return names
}

// isEmojiStart reports whether r is one of the symbols the bridge's
// messages start with: miscellaneous technical (⌛, ⏳), symbols and
// dingbats (⚠, ✅, ❌, ❓), and the emoji planes.
func isEmojiStart(r rune) bool {
	return r >= 0x2300 && r <= 0x23ff || r >= 0x2600 && r <= 0x27bf || r >= 0x2b00 && r <= 0x2bff || r >= 0x1f000 && r <= 0x1faff
}

// notChatText are the strings outside messages.go that start like a
// message of the bridge but go nowhere near a chat.
var notChatText = map[string]string{
	"💬 ":         "a line of a run's activity log",
	"🔧 Tool: ":   "a line of a run's activity log",
	"❌ Error: ":  "a line of a run's activity log",
	"✅ Result: ": "a line of a run's activity log",
	"🔐":          "one of the OTP guard's markers, which it looks for in what a brain sends",
}

// TestChatMessagesLiveInTheCatalogue: no Go file but messages.go has a
// string that starts with an emoji, the mark of a message the bridge
// sends into chats, so that a new one goes into the catalogue.
func TestChatMessagesLiveInTheCatalogue(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoDir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "messages.go" {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			if r, _ := utf8.DecodeRuneInString(s); !isEmojiStart(r) {
				return true
			}
			if _, ok := notChatText[s]; ok {
				seen[s] = true
				return true
			}
			t.Errorf("%s: %q starts like a chat message; put it in messages.go", fset.Position(lit.Pos()), s)
			return true
		})
	}
	for _, s := range slices.Sorted(maps.Keys(notChatText)) {
		if !seen[s] {
			t.Errorf("notChatText has %q, which no file has any more", s)
		}
	}
}
