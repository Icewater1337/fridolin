package httpapi

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Keep the public method/path inventory reviewable independently of handlers.
// Command integration tests additionally verify each route's authentication class.
func TestRouteTableMatchesGolden(t *testing.T) {
	source, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`\.Handle(?:Func)?\("((?:[A-Z]+ )?/[^\"]*)"`)
	var routes []string
	for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
		routes = append(routes, match[1])
	}
	slices.Sort(routes)
	want, err := os.ReadFile("testdata/routes.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(routes, "\n") + "\n"; got != string(want) {
		t.Errorf("HTTP route inventory changed:\ngot:\n%s\nwant:\n%s", got, want)
	}
}
