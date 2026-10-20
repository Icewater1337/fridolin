package manifest

import (
	"slices"
	"strings"
	"testing"
)

func TestGrantsAreValidatedOnTheirOwn(t *testing.T) {
	t.Parallel()
	core := func(grants map[string][]string) Manifest {
		return Manifest{ID: "core", Name: "Core", Role: RoleCore, Grants: grants}
	}
	for _, tc := range []struct {
		name string
		m    Manifest
		want string // "" for valid
	}{
		{"core with grants", core(map[string][]string{"family": {"delegate", "read"}, "taxes": {"files"}}), ""},
		{"core without grants", core(nil), ""},
		{"sub-brain without grants", Manifest{ID: "family", Name: "Family"}, ""},
		{"unknown role", Manifest{ID: "family", Name: "Family", Role: "boss"}, `role "boss" must be "core"`},
		{"grants on a sub-brain", Manifest{ID: "family", Name: "Family", Grants: map[string][]string{"taxes": {"read"}}}, "only the core brain"},
		{"grant on itself", core(map[string][]string{"core": {"read"}}), "cannot grant itself"},
		{"unknown kind", core(map[string][]string{"family": {"write"}}), `grant "write" for "family" must be one of delegate, files, read`},
		{"kind twice", core(map[string][]string{"family": {"read", "read"}}), "listed twice"},
		{"target not an id", core(map[string][]string{"Family!": {"read"}}), "not a brain id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.m.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("Validate = %v, want nil", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("Validate = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGrantsNormalizeAndQuery(t *testing.T) {
	t.Parallel()
	a := Manifest{ID: "core", Name: "Core", Role: RoleCore, Grants: map[string][]string{"family": {"read", "delegate"}, "empty": {}}}
	b := Manifest{ID: "core", Name: "Core", Role: RoleCore, Grants: map[string][]string{"family": {"delegate", "read"}}}
	if !a.Equal(b) {
		t.Error("the same grants in another order, or with an empty entry, must compare equal")
	}
	if n := a.Normalized(); len(n.Grants) != 1 || len(a.Grants) != 2 {
		t.Errorf("Normalized changed the caller's map or kept the empty entry: %v / %v", n.Grants, a.Grants)
	}
	if !a.Granted("family", GrantDelegate) || a.Granted("family", GrantFiles) || a.Granted("taxes", GrantRead) {
		t.Error("Granted answers wrong")
	}
	if got := a.GrantedTo(""); !slices.Equal(got, []string{"family"}) {
		t.Errorf("GrantedTo(\"\") = %v", got)
	}
	sub := Manifest{ID: "x", Name: "X", Grants: map[string][]string{"family": {"read"}}}
	if sub.Granted("family", GrantRead) || len(sub.GrantedTo(GrantRead)) != 0 {
		t.Error("a sub-brain's grants must never count")
	}
	data, err := Encode(&b)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(data)
	if err != nil || !back.Equal(b) {
		t.Fatalf("round trip: %v %+v", err, back)
	}
	plain, _ := Encode(&Manifest{ID: "x", Name: "X"})
	if strings.Contains(string(plain), "grants") || strings.Contains(string(plain), "role") {
		t.Errorf("a sub-brain's brain.json gains role or grants:\n%s", plain)
	}
}
