package backup

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStatusSnapshotPreservesMapEncoding(t *testing.T) {
	empty := ""
	now := time.Unix(123, 0).UTC()
	for _, populated := range []bool{false, true} {
		v := StatusSnapshot{Brains: []BrainStatus{}, Rclone: RcloneStatus{InstallHint: "install"}, SecretExcludes: []string{}}
		rc := map[string]any{"found": false, "install_hint": "install"}
		var job map[string]any
		if populated {
			v.Rclone = RcloneStatus{Found: true, InstallHint: "install", Path: &empty, Version: &empty, Error: &empty}
			rc = map[string]any{"found": true, "install_hint": "install", "path": "", "version": "", "error": ""}
			v.Job = &JobStatus{Brain: &empty, Current: "", Done: []string{}, Kind: jobRestore, Queue: nil, Started: now, Target: &empty}
			job = map[string]any{"brain": "", "current": "", "done": []string{}, "kind": jobRestore, "queue": []string(nil), "started": now, "target": ""}
			v.NextRun = &now
		}
		old := map[string]any{"brains": v.Brains, "bridge": v.Bridge, "connect": v.Connect, "connected": v.Connected, "include_bridge_config": v.IncludeBridgeConfig, "job": job, "last_restore": v.LastRestore, "rclone": rc, "secret_excludes": v.SecretExcludes, "settings": v.Settings}
		if populated {
			old["next_run"] = now
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
			t.Fatalf("populated=%v\ngot %s\nwant %s", populated, got, want)
		}
	}
}
