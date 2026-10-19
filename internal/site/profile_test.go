package site

import (
	"path/filepath"
	"regexp"

	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/sandbox"

	"github.com/Icewater1337/fridolin/internal/textx"
)

// TestSiteProfileClosesBrowserProfiles: no declaration opens a browser's
// profile to module code, the browsers whose release channels the brains'
// profile denies by the start of the folder's name included, and a
// missing declared folder there is left missing. Other programs' folders
// in Application Support stay open to a declaration.
func TestSiteProfileClosesBrowserProfiles(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, config.DataDirName)
	appSupport := filepath.Join(home, "Library", "Application Support")
	q := func(p string) string { s, _ := textx.SBPLString(p); return s }
	// The prefix rule, as the profile writes it; its strings escape a
	// backslash, as the regex's \. needs.
	prefixRule := strings.ReplaceAll("^"+regexp.QuoteMeta(appSupport)+sandbox.BrowserProfilePattern(), `\`, `\\`)
	for _, c := range []struct {
		folder string
		closed bool
		deny   string // the rule that keeps it closed
	}{
		{"Microsoft Edge", true, `(deny file-write* (regex "` + prefixRule + `"))`},
		{"Microsoft Edge Beta", true, `(deny file-write* (regex "` + prefixRule + `"))`},
		{"Vivaldi Snapshot", true, `(deny file-write* (regex "` + prefixRule + `"))`},
		{"com.operasoftware.OperaGX", true, `(deny file-write* (regex "` + prefixRule + `"))`},
		{"Google/Chrome", true, `(deny file-write* (subpath ` + q(filepath.Join(appSupport, "Google")) + `))`},
		{"BraveSoftware/Brave-Browser", true, `(deny file-write* (subpath ` + q(filepath.Join(appSupport, "BraveSoftware")) + `))`},
		{"SomeTool", false, ""},
	} {
		t.Run(c.folder, func(t *testing.T) {
			dir := filepath.Join(appSupport, c.folder, "Default")
			in := sandbox.SitePolicy{
				Home: home, DataDir: data,
				Shell: filepath.Join(data, "site", "shell"), Deploy: filepath.Join(data, "site", "deploys", "abc"),
				Tmp: filepath.Join(data, "site", "tmp"),
				Modules: []sandbox.SiteModule{{ID: "m", Dir: filepath.Join(data, "site", "deploys", "abc", "modules", "m"), Files: []sandbox.FileCapability{
					{Name: "profile", Path: dir, Access: "write", Folder: true},
				}}},
			}
			if got := in.Unwritable(dir); got != c.closed {
				t.Errorf("unwritable = %v, want %v", got, c.closed)
			}
			prepareDeclaredFolders(in.Modules, in.Unwritable)
			if sitePathExists(dir) == c.closed {
				t.Errorf("the declared folder exists = %v, want %v", sitePathExists(dir), !c.closed)
			}
			profile, err := siteSandboxProfile(in)
			if err != nil {
				t.Fatal(err)
			}
			allow := strings.LastIndex(profile, `(allow file-read* file-write* (subpath `+q(dir)+`))`)
			if allow < 0 {
				t.Fatalf("the declaration is not in the profile:\n%s", profile)
			}
			if !c.closed {
				return
			}
			if deny := strings.LastIndex(profile, c.deny); deny < allow {
				t.Errorf("%s does not come after the declaration (%d, %d)", c.deny, deny, allow)
			}
			if read := strings.Replace(c.deny, "file-write*", "file-read*", 1); strings.LastIndex(profile, read) < allow {
				t.Errorf("%s does not come after the declaration", read)
			}
		})
	}
}
