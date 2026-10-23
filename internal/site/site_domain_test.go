package site

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckDomainSaysWhatIsWrong(t *testing.T) {
	m := newTestSite(t)
	if c := m.CheckDomain(context.Background()); c.OK || !strings.Contains(c.Error, "Enter the domain first") {
		t.Fatalf("without a domain: %+v", c)
	}
	if _, err := m.UpdateSettings(SiteSettingsUpdate{Domain: new("example.com")}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		status int
		body   string
		ok     bool
		want   string
	}{
		{"the site answers", 200, "ok\n", true, ""},
		{"the tunnel is down", 530, "error 1033", false, "cannot reach the site (530)"},
		{"another site answers", 200, "<html>", false, "not as this site"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/_fridolin/health" {
					t.Errorf("asked %s", r.URL.Path)
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			m.domainURL = func(string) string { return srv.URL + "/_fridolin/health" }
			got := m.CheckDomain(context.Background())
			if got.OK != c.ok || !strings.Contains(got.Error, c.want) || got.Status != c.status {
				t.Errorf("%+v", got)
			}
		})
	}
}
