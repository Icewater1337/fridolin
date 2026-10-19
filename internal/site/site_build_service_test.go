package site

import (
	"path/filepath"

	"testing"
)

// A chat's file is named only by a valid brain id.
func TestBuildChatPathNeedsABrainID(t *testing.T) {
	c := &siteChats{m: newTestSite(t)}
	for _, bad := range []string{"../../etc/passwd", "..", "web/../x", "", "Web", "a b"} {
		if _, err := c.path(bad); err == nil {
			t.Errorf("path(%q) was accepted", bad)
		}
		if err := c.add(bad, "user", "hi"); err == nil {
			t.Errorf("add(%q) wrote a chat", bad)
		}
	}
	p, err := c.path("web")
	if err != nil || filepath.Dir(p) != c.m.dir("build") {
		t.Errorf("path(web) = %q, %v", p, err)
	}
}
