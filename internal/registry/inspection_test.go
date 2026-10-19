package registry

import (
	"encoding/json"
	"testing"
)

func TestInspectionPreservesAbsentAndFalseMetadata(t *testing.T) {
	no := false
	zero := 0
	for _, inspected := range []bool{false, true} {
		v := Inspection{AbsPath: "candidate", Problems: []string{}, Warnings: []string{}}
		old := map[string]any{"abs_path": v.AbsPath, "problems": v.Problems, "warnings": v.Warnings}
		if inspected {
			v.Exists = &no
			v.IsDir = &no
			v.IsGit = &no
			v.HasHistory = &no
			v.HasPrompt = &no
			v.Entries = &zero
			v.Suggested = &SuggestedBrain{ID: "candidate", Name: "Candidate"}
			old["exists"] = false
			old["is_dir"] = false
			old["is_git"] = false
			old["has_history"] = false
			old["has_prompt"] = false
			old["entries"] = 0
			old["suggested"] = map[string]string{"id": "candidate", "name": "Candidate"}
		}
		got, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("inspected=%v\ngot %s\nwant %s", inspected, got, want)
		}
	}
}
