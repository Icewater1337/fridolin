package manifest

import (
	"slices"
	"strings"
	"testing"
)

func TestBackupTargets(t *testing.T) {
	// A brain.json without targets means what it always did: the cloud,
	// no node target.
	old, err := Decode([]byte(`{"id":"p","name":"P","backup":{"enabled":true,"exclude":["/work/"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !old.Backup.Cloud() || old.Backup.Node("nas") || !slices.Equal(old.Backup.CloudExcludes(), []string{"/work/"}) {
		t.Fatalf("default: %+v", old.Backup)
	}
	data, err := Encode(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "targets") || strings.Contains(string(data), "node_only") {
		t.Fatalf("an unchanged brain.json gains no keys:\n%s", data)
	}

	m := Manifest{ID: "p", Name: "P", Backup: Backup{Enabled: true, Exclude: []string{"*.log", "/work/"},
		Targets: map[string]bool{"cloud": true, "node:nas": true}, NodeOnly: []string{"/work/", "/clients/"}}}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	b := m.Backup
	if !b.Cloud() || !b.Node("nas") || b.Node("other") {
		t.Fatalf("targets: %+v", b)
	}
	if got := b.CloudExcludes(); !slices.Equal(got, []string{"*.log", "/work/", "/work/", "/clients/"}) {
		t.Fatalf("cloud excludes %v: node_only never goes to the cloud", got)
	}
	if got := b.NodeExcludes(); !slices.Equal(got, []string{"*.log"}) {
		t.Fatalf("node excludes %v: node_only lifts an exclude for the node targets", got)
	}
	b.Targets["cloud"] = false
	if b.Cloud() {
		t.Fatal("cloud: false keeps the folder out of the cloud")
	}
	b.Enabled = false
	if b.Node("nas") {
		t.Fatal("backup off is off everywhere")
	}
	for _, bad := range []Backup{
		{Enabled: true, Targets: map[string]bool{"b2": true}},
		{Enabled: true, Targets: map[string]bool{"node:NAS": true}},
		{Enabled: true, NodeOnly: []string{"+/work/"}},
		{Enabled: true, NodeOnly: []string{" "}},
	} {
		m := Manifest{ID: "p", Name: "P", Backup: bad}
		if err := m.Validate(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
