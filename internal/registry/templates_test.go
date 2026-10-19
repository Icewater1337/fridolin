package registry

import (
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func scaffoldTestTemplates() Templates {
	return TemplatesFrom(fstest.MapFS{
		"brain-prompt.example.md": {Data: []byte("# {{BRAIN_NAME}} assistant\n")},
		"skills/a.md":             {Data: []byte("Skills for {{BRAIN_NAME}}\n")},
	}, "memories/people/whatsapp-identity-map.md")
}

func TestTemplatesFromUsesSuppliedAssets(t *testing.T) {
	t.Parallel()
	source := fstest.MapFS{
		"brain-prompt.example.md": {Data: []byte("# {{BRAIN_NAME}}\n")},
		"skills/z.md":             {Data: []byte("last")},
		"skills/a.md":             {Data: []byte("first")},
	}
	got := TemplatesFrom(source, "people/map.md")
	want := Templates{
		Prompt: "# {{BRAIN_NAME}}\n", IdentityMapPath: "people/map.md",
		Skills: []TemplateSkill{{"a.md", "first"}, {"z.md", "last"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("templates = %#v, want %#v", got, want)
	}
}

type templateReadFailure struct {
	fs.FS
	path string
}

func (f templateReadFailure) Open(name string) (fs.File, error) {
	if name == f.path {
		return nil, fs.ErrPermission
	}
	return f.FS.Open(name)
}

func TestTemplatesFromFailsBeforeScaffoldingIncompleteAssets(t *testing.T) {
	t.Parallel()
	source := fstest.MapFS{
		"brain-prompt.example.md": {Data: []byte("prompt")},
		"skills/a.md":             {Data: []byte("skill")},
	}
	for _, tc := range []struct{ path, prefix string }{
		{"brain-prompt.example.md", "embedded template brain-prompt.example.md: "},
		{"skills", "embedded skills: "},
		{"skills/a.md", "embedded template skills/a.md: "},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			defer func() {
				got := fmt.Sprint(recover())
				if !strings.HasPrefix(got, tc.prefix) || !strings.Contains(got, fs.ErrPermission.Error()) {
					t.Fatalf("panic = %q, want %q with permission error", got, tc.prefix)
				}
			}()
			TemplatesFrom(templateReadFailure{source, tc.path}, "people/map.md")
		})
	}
}
