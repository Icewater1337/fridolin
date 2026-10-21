package nodeproto

import (
	"strings"
	"testing"
)

func TestAndroidBuildCapability(t *testing.T) {
	ab := &AndroidBuildCap{Java: "21", Platforms: []string{"android-35"}}
	declared := Capabilities{Host: true, AndroidBuild: ab}
	if err := declared.Check(); err != nil {
		t.Fatal(err)
	}
	if Covers(Capabilities{Host: true}, declared) {
		t.Error("an approval without android-build covers it")
	}
	// Approved once, it covers whatever JDK and platforms it reports.
	approved := Capabilities{Host: true, AndroidBuild: &AndroidBuildCap{Java: "17"}}
	if !Covers(approved, declared) {
		t.Error("an approved android-build does not cover a newer JDK")
	}
	eff := Intersect(declared, approved)
	if eff.AndroidBuild == nil || eff.AndroidBuild.Java != "21" || !eff.Allows(MethodAndroidBuild) || !eff.Allows(MethodAndroidRead) {
		t.Errorf("effective %+v", eff)
	}
	if Intersect(declared, Capabilities{Host: true}).Allows(MethodAndroidBuild) {
		t.Error("android.build allowed without the approval")
	}
	if Intersect(Capabilities{Host: true}, approved).AndroidBuild != nil {
		t.Error("android-build served while the node no longer offers it")
	}
	if (Capabilities{AndroidBuild: ab}).Empty() {
		t.Error("android-build alone is empty")
	}
	if s := declared.Summary(); s != "host, android-build" {
		t.Errorf("summary %q", s)
	}
	for _, bad := range []*AndroidBuildCap{{Java: "21a"}, {Platforms: []string{"../x"}}, {Platforms: []string{"android-35\n"}}} {
		if (Capabilities{AndroidBuild: bad}).Check() == nil {
			t.Errorf("%+v passed", bad)
		}
	}
}

func TestAndroidBuildParamsCheck(t *testing.T) {
	ok := AndroidBuildParams{Source: []byte("x"), Properties: map[string]string{"client_id": "abc-123", "site_url": "https://example.com"}}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]AndroidBuildParams{
		"no source":          {},
		"too large":          {Source: make([]byte, MaxAndroidSourceBytes+1)},
		"a newline in value": {Source: []byte("x"), Properties: map[string]string{"k": "a\nb"}},
		"a backslash":        {Source: []byte("x"), Properties: map[string]string{"k": "a" + string(rune(92)) + "n"}},
		"a quote":            {Source: []byte("x"), Properties: map[string]string{"k": `a"b`}},
		"a bad name":         {Source: []byte("x"), Properties: map[string]string{"K=x": "a"}},
	} {
		if err := p.Check(); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
	if !strings.Contains(strings.Join(Methods, " "), MethodAndroidBuild) {
		t.Error("android.build is not among the methods")
	}
}
