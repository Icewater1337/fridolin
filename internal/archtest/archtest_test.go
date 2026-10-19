package archtest

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// imports is the table of allowed imports (docs/ARCHITECTURE.md, section
// 4): for every package of the module, by its path below the module, the
// module's packages it may import. A package without a row fails
// TestImportRules, and so does an import its row does not list: a new
// edge is a change to this table, made and reviewed in the same pull
// request. Imports from outside the module are not checked here.
var imports = map[string][]string{
	"cmd/fridolin":       {"examples", "internal/access", "internal/activity", "internal/apperr", "internal/backup", "internal/brain", "internal/chat", "internal/claudecli", "internal/config", "internal/devchat", "internal/fsx", "internal/google", "internal/history", "internal/httpapi", "internal/jsonx", "internal/logx", "internal/manifest", "internal/media", "internal/otp", "internal/outbound", "internal/pipeline", "internal/platform", "internal/plaud", "internal/prompt", "internal/registry", "internal/route", "internal/sandbox", "internal/store", "internal/testutil", "internal/textx", "internal/trigger", "internal/version", "internal/whatsapp", "site", "web"},
	"examples":           nil,
	"internal/access":    {"internal/chat", "internal/trigger"},
	"internal/activity":  {"internal/history", "internal/textx"},
	"internal/apperr":    nil,
	"internal/archtest":  nil,
	"internal/backup":    {"internal/apperr", "internal/fsx", "internal/jsonx", "internal/testutil", "internal/textx"},
	"internal/brain":     {"internal/chat", "internal/claudecli", "internal/fsx", "internal/history", "internal/manifest", "internal/otp", "internal/platform", "internal/prompt", "internal/route", "internal/testutil", "internal/textx"},
	"internal/chat":      nil,
	"internal/claudecli": {"internal/platform", "internal/testutil", "internal/textx"},
	"internal/config":    {"internal/chat", "internal/fsx", "internal/jsonx", "internal/manifest", "internal/textx", "internal/trigger"},
	"internal/devchat":   {"internal/chat", "internal/textx"},
	"internal/fsx":       nil,
	"internal/google":    {"internal/manifest", "internal/fsx", "internal/apperr", "internal/jsonx", "internal/testutil"},
	"internal/history":   {"internal/fsx"},
	"internal/httpapi":   {"internal/brain", "internal/google", "internal/manifest", "internal/apperr", "internal/backup", "internal/config", "internal/activity", "internal/chat", "internal/fsx", "internal/logx"},
	"internal/jsonx":     nil,
	"internal/logx":      nil,
	"internal/manifest":  {"internal/chat", "internal/fsx", "internal/jsonx"},
	"internal/media":     {"internal/fsx", "internal/textx"},
	"internal/otp":       {"internal/fsx", "internal/history"},
	"internal/outbound":  {"internal/store", "internal/textx"},
	"internal/pipeline":  {"internal/brain", "internal/chat", "internal/claudecli", "internal/otp", "internal/outbound", "internal/testutil"},
	"internal/platform":  nil,
	"internal/plaud":     {"internal/fsx", "internal/jsonx", "internal/logx", "internal/media", "internal/outbound", "internal/platform", "internal/testutil", "internal/textx"},
	"internal/prompt":    {"internal/chat", "internal/history", "internal/testutil"},
	"internal/registry":  {"internal/apperr", "internal/config", "internal/fsx", "internal/manifest", "internal/route"},
	"internal/route":     {"internal/fsx", "internal/manifest"},
	"internal/sandbox":   {"internal/fsx", "internal/testutil", "internal/textx"},
	"internal/store":     {"internal/fsx", "internal/logx"},
	"internal/testutil":  nil,
	"internal/textx":     nil,
	"internal/trigger":   nil,
	"internal/version":   nil,
	"internal/whatsapp":  {"internal/access", "internal/chat", "internal/logx", "internal/media", "internal/platform", "internal/textx", "internal/trigger"},
	"site":               nil,
	"web":                nil,
}

// ambientAllowed are the packages below internal/ that may read the
// process's environment, home folder, executable or working directory
// (TestNoAmbientEnv).
var ambientAllowed = map[string]bool{
	"internal/platform": true,
	"internal/testutil": true,
}

// ambient are the calls that read process-wide state. Code below internal/
// gets these values passed in, computed once at startup.
var ambient = map[string][]string{
	"os":      {"Environ", "Executable", "ExpandEnv", "Getenv", "Getwd", "LookupEnv", "UserHomeDir"},
	"os/exec": {"LookPath"},
}

// moduleRoot is the folder of go.mod above the test's working directory,
// which go test makes this package's folder.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for root := dir; ; root = filepath.Dir(root) {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		if filepath.Dir(root) == root {
			t.Fatalf("no go.mod above %s", dir)
		}
	}
}

// modulePath is the module's path, from go.mod.
func modulePath(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(p), `"`)
		}
	}
	t.Fatal("go.mod names no module")
	return ""
}

// packageDirs lists the folders below root that hold Go files, by their
// slash-separated path below root. Like the go command it skips testdata
// and folders whose name starts with a dot or an underscore, and it skips
// node_modules, which only npm fills.
func packageDirs(t *testing.T, root string) []string {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (name == "testdata" || name == "node_modules" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(name, ".go") {
			rel, err := filepath.Rel(root, filepath.Dir(p))
			if err != nil {
				return err
			}
			if rel = filepath.ToSlash(rel); !slices.Contains(dirs, rel) {
				dirs = append(dirs, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(dirs)
	return dirs
}

// TestImportRules checks every package's imports of the module's own
// packages against the table above, for macOS and Linux, its tests'
// imports included. Nothing may import a command.
func TestImportRules(t *testing.T) {
	root := moduleRoot(t)
	mod := modulePath(t, root)
	reported := map[string]bool{}
	report := func(format string, args ...any) {
		if msg := fmt.Sprintf(format, args...); !reported[msg] {
			reported[msg] = true
			t.Error(msg)
		}
	}
	for _, dir := range packageDirs(t, root) {
		allowed, ok := imports[dir]
		if dir == "." {
			continue // TestRootHasNoGoFiles
		}
		if !ok {
			t.Errorf("%s has no row in the import table: add one, listing the module's packages it may import", dir)
			continue
		}
		for _, goos := range []string{"darwin", "linux"} {
			ctx := build.Default
			ctx.GOOS, ctx.CgoEnabled = goos, true
			pkg, err := ctx.ImportDir(filepath.Join(root, filepath.FromSlash(dir)), 0)
			var noGo *build.NoGoError
			if errors.As(err, &noGo) {
				continue
			}
			if err != nil {
				t.Fatalf("%s (%s): %v", dir, goos, err)
			}
			if dir == "internal/plaud" && slices.Contains(pkg.Imports, "net/http") {
				report("plaud owns service operations; HTTP handling belongs to httpapi")
			}
			for _, imp := range slices.Concat(pkg.Imports, pkg.TestImports, pkg.XTestImports) {
				rel, ok := strings.CutPrefix(imp, mod+"/")
				if !ok {
					continue
				}
				switch {
				case rel == dir:
					// An external test of the package itself.
				case rel == "internal/testutil" && slices.Contains(pkg.Imports, imp):
					report("%s imports testutil from production code; only tests may import it", dir)
				case strings.HasPrefix(rel, "cmd/"):
					report("%s imports %s: nothing imports a command", dir, imp)
				case !slices.Contains(allowed, rel):
					report("%s imports %s, which its row in the import table does not list", dir, rel)
				}
			}
		}
	}
	for dir := range imports {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); err != nil {
			t.Errorf("the import table has a row for %s, which is no folder of the module: %v", dir, err)
		}
	}
}

// TestNoAmbientEnv scans the code below internal/ for calls that read the
// process's environment, home folder, executable or working directory.
// Such values are computed once at startup and passed in, so a package
// can be tested with values of its own and does not change behaviour
// when the process's state does. Test files are not scanned, and the
// packages in ambientAllowed may make these calls.
func TestNoAmbientEnv(t *testing.T) {
	root := moduleRoot(t)
	for _, dir := range packageDirs(t, root) {
		if !strings.HasPrefix(dir, "internal/") || ambientAllowed[dir] {
			continue
		}
		files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			for _, call := range ambientCalls(t, file) {
				rel, _ := filepath.Rel(root, file)
				t.Errorf("%s:%d calls %s, which reads the process's state: take the value as a parameter", filepath.ToSlash(rel), call.line, call.name)
			}
		}
	}
}

type ambientCall struct {
	name string
	line int
}

// ambientCalls lists the uses of the functions in ambient in file, however
// the file names the packages it imports.
func ambientCalls(t *testing.T, file string) []ambientCall {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{} // local name → import path
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil || ambient[p] == nil {
			continue
		}
		name := path.Base(p)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = p
	}
	var calls []ambientCall
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if p, ok := names[x.Name]; ok && slices.Contains(ambient[p], sel.Sel.Name) {
			calls = append(calls, ambientCall{p + "." + sel.Sel.Name, fset.Position(sel.Pos()).Line})
		}
		return true
	})
	return calls
}

// TestRootHasNoGoFiles: the main package is in cmd/fridolin. With a Go
// file of a package other than main at the repository's root, an old
// `go build -o fridolin .` would exit 0 and write an ar archive over the
// binary a service may still start; with none it fails with "no Go
// files" and leaves the binary alone.
func TestRootHasNoGoFiles(t *testing.T) {
	root := moduleRoot(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") {
			t.Errorf("%s is a Go file at the repository's root; Go code goes into cmd/fridolin or a package below internal/", e.Name())
		}
	}
}

// TestPublicModulePath keeps remote go install and version stamping on the
// canonical repository module rather than a checkout-only import name.
func TestPublicModulePath(t *testing.T) {
	const want = "github.com/Icewater1337/fridolin"
	if got := modulePath(t, moduleRoot(t)); got != want {
		t.Errorf("module path = %q, want %q", got, want)
	}
}

// TestPlatformHasNoMutableGlobals keeps process state on the owning value.
// The unsupported-platform file only declares an intentional build error.
func TestPlatformHasNoMutableGlobals(t *testing.T) {
	root := moduleRoot(t)
	files, err := filepath.Glob(filepath.Join(root, "internal", "platform", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || filepath.Base(file) == "unsupported.go" {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
				t.Errorf("%s:%d declares mutable package state; keep it on the owning value", filepath.Base(file), fset.Position(gen.Pos()).Line)
			}
		}
	}
}

// TestTestutilUsesOnlyStandardLibrary keeps the shared fake executable usable
// from the internal tests of every package without hidden dependency cycles.
func TestTestutilUsesOnlyStandardLibrary(t *testing.T) {
	root := moduleRoot(t)
	p, err := build.Default.ImportDir(filepath.Join(root, "internal", "testutil"), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range p.Imports {
		dep, err := build.Default.Import(imp, "", build.FindOnly)
		if err != nil || !dep.Goroot {
			t.Errorf("testutil imports non-standard-library package %s (%v)", imp, err)
		}
	}
}
