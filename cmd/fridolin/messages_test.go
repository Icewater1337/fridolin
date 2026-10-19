package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

// catalogue is every message of messages.go, by the name it has there,
// as a chat gets it: each function once for each way it reads.
func catalogue() []struct{ name, text string } {
	return []struct{ name, text string }{
		{"noBrainNotice", outboundmsg.NoBrainNotice},
		{"heldBrainNotice", outboundmsg.HeldBrainNotice},
		{"restartedNotice", outboundmsg.RestartedNotice},
		{"resetReply(false)", outboundmsg.ResetReply(false)},
		{"resetReply(true)", outboundmsg.ResetReply(true)},
		{"stopReply(true, 0)", outboundmsg.StopReply(true, 0)},
		{"stopReply(false, 2)", outboundmsg.StopReply(false, 2)},
		{"stopReply(false, 0)", outboundmsg.StopReply(false, 0)},
		{`helpReply("")`, outboundmsg.HelpReply("")},
		{`helpReply("@claude")`, outboundmsg.HelpReply("@claude")},
		{"queuedNotice(2)", outboundmsg.QueuedNotice(2)},
		{"queueFullNotice(\"@claude !stop\")", outboundmsg.QueueFullNotice("@claude !stop")},
		{"otpCaughtNotice", outboundmsg.OtpCaughtNotice},
		{"workingNotice", outboundmsg.WorkingNotice},
		{"questionPrefix", outboundmsg.QuestionPrefix + "Which day?"},
		{"chatErrNotSignedIn", outboundmsg.ChatErrNotSignedIn},
		{"chatErrCodexNotSignedIn", outboundmsg.ChatErrCodexNotSignedIn},
		{"chatErrCodexLimit", outboundmsg.ChatErrCodexLimit},
		{"chatErrLimit", outboundmsg.ChatErrLimit},
		{"chatErrTimeout", outboundmsg.ChatErrTimeout},
		{"chatErrGeneric", outboundmsg.ChatErrGeneric},
		{"chatErrSandbox", outboundmsg.ChatErrSandbox(errors.New("the sandbox could not be set up"))},
		{"chatErrClaude", outboundmsg.ChatErrClaude("Credit balance is too low")},
		{"chatErrCodex", outboundmsg.ChatErrCodex("Something failed")},
		{"buildChatError", outboundmsg.BuildChatError(errors.New("brain queue is full"))},
		{"viewOnceNotice", outboundmsg.ViewOnceNotice},
		{"mediaTooLargeNotice", outboundmsg.MediaTooLargeNotice(100 << 20)},
		{"mediaFailedNotice", outboundmsg.MediaFailedNotice},
		{"pollAsText", outboundmsg.PollAsText(outboundmsg.Poll{Question: "Lunch on Friday?", Options: []string{"Pizza", "Sushi"}})},
		{"startupCheckAlert", outboundmsg.StartupCheckAlert("missing ~/.fridolin/credentials/google_calendar_token_main.json")},
		{"backupRefusedAlert", outboundmsg.BackupRefusedAlert("Notes", "it would delete 12 of 40 files")},
		{"plaudGivenUpAlert", outboundmsg.PlaudGivenUpAlert("Team sync", "notes", "brain notes is not configured")},
		{"plaudExpiredAlert", outboundmsg.PlaudExpiredAlert},
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

	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoDir, "internal", "outbound", "messages.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range topLevelNames(f) {
		name = strings.ToLower(name[:1]) + name[1:]
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
	"⚠️ Skipped a line of %d bytes, longer than the %d MiB the bridge reads": "a line of a run's activity log",
	"❔ Event of a type the bridge does not read: %q":                         "a line of a run's activity log",
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
	var files []string
	err := filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && path != repoDir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "node_modules" || d.Name() == "vendor"):
			return filepath.SkipDir
		case !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go"):
			files = append(files, path)
		}
		return nil
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files in %s: %v", repoDir, err)
	}
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, path := range files {
		if path == filepath.Join(repoDir, "internal", "outbound", "messages.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		if ast.IsGenerated(f) {
			continue
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
