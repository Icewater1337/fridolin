package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupJSONDiagnosticsKeepLegacyNames(t *testing.T) {
	inner := backupJSONUsesInnerStructContext(t)
	for _, tc := range []struct{ kind, body, outer, inner string }{
		{"STATE", "[]", "json: cannot unmarshal array into Go value of type main.backupState", "json: cannot unmarshal array into Go value of type main.backupState"},
		{"STATE", "{\"brains\":{\"notes\":[]}}", "json: cannot unmarshal array into Go struct field backupState.brains.notes of type main.BackupResult", "json: cannot unmarshal array into Go struct field backupState.brains of type main.BackupResult"},
		{"STATE", "{\"bridge\":[]}", "json: cannot unmarshal array into Go struct field backupState.bridge of type main.BackupResult", "json: cannot unmarshal array into Go struct field backupState.bridge of type main.BackupResult"},
		{"STATE", "{\"last_restore\":[]}", "json: cannot unmarshal array into Go struct field backupState.last_restore of type main.RestoreResult", "json: cannot unmarshal array into Go struct field backupState.last_restore of type main.RestoreResult"},
		{"STATE", "{\"brains\":{\"notes\":{\"ok\":[]}}}", "json: cannot unmarshal array into Go struct field backupState.brains.notes.ok of type bool", "json: cannot unmarshal array into Go struct field BackupResult.brains.ok of type bool"},
		{"STATE", "{\"last_restore\":{\"ok\":[]}}", "json: cannot unmarshal array into Go struct field backupState.last_restore.ok of type bool", "json: cannot unmarshal array into Go struct field RestoreResult.last_restore.ok of type bool"},
		{"QUESTION", "[]", "json: cannot unmarshal array into Go value of type main.rcloneQuestion", "json: cannot unmarshal array into Go value of type main.rcloneQuestion"},
		{"QUESTION", "{\"State\":[]}", "json: cannot unmarshal array into Go struct field rcloneQuestion.State of type string", "json: cannot unmarshal array into Go struct field rcloneQuestion.State of type string"},
		{"QUESTION", "{\"Option\":[]}", "json: cannot unmarshal array into Go struct field rcloneQuestion.Option of type struct { Name string \"json:\\\"Name\\\"\"; DefaultStr string \"json:\\\"DefaultStr\\\"\" }", "json: cannot unmarshal array into Go struct field rcloneQuestion.Option of type struct { Name string \"json:\\\"Name\\\"\"; DefaultStr string \"json:\\\"DefaultStr\\\"\" }"},
		{"QUESTION", "{\"Option\":{\"Name\":[]}}", "json: cannot unmarshal array into Go struct field rcloneQuestion.Option.Name of type string", "json: cannot unmarshal array into Go struct field .Option.Name of type string"},
	} {
		t.Run(tc.kind+tc.body, func(t *testing.T) {
			var target any = &backupState{}
			if tc.kind == "QUESTION" {
				target = &rcloneQuestion{}
			}
			raw := json.Unmarshal([]byte(tc.body), target)
			err := backupJSONError(raw)
			want := tc.outer
			if inner {
				want = tc.inner
			}
			if err == nil || err.Error() != want {
				t.Fatalf("diagnostic = %v, want %s", err, want)
			}
			var before, after *json.UnmarshalTypeError
			if !errors.As(raw, &before) || !errors.As(err, &after) || before != after {
				t.Fatal("original typed decoder error was lost")
			}
		})
	}
}

func TestBackupJSONDiagnosticsReachVisibleBoundaries(t *testing.T) {
	fx := newBackupFixture(t, nil)
	if err := os.MkdirAll(filepath.Dir(fx.manager.statePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fx.manager.statePath(), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	logs := captureBackupLog(t)
	_ = New(fx.options)
	if got := strings.Join(logs(), "\n"); !strings.Contains(got, "json: cannot unmarshal array into Go value of type main.backupState") {
		t.Fatalf("state warning: %s", got)
	}
	fx.runner = func(_ context.Context, args []string, _ io.Writer) ([]byte, error) { return []byte("[]"), nil }
	err := fx.manager.connectDrive(t.Context(), &connectJob{State: "running"})
	if err == nil || err.Error() != "unexpected rclone output: json: cannot unmarshal array into Go value of type main.rcloneQuestion " {
		t.Fatalf("connection diagnostic = %v", err)
	}
}

func backupJSONUsesInnerStructContext(t *testing.T) bool {
	t.Helper()
	type inner struct {
		Value bool `json:"value"`
	}
	type outer struct {
		Inner inner `json:"inner"`
	}
	var v outer
	var typed *json.UnmarshalTypeError
	if err := json.Unmarshal([]byte(`{"inner":{"value":"wrong"}}`), &v); !errors.As(err, &typed) {
		t.Fatalf("decoder context probe: %v", err)
	}
	switch typed.Struct {
	case "inner":
		return true
	case "outer":
		return false
	default:
		t.Fatalf("unexpected decoder struct context %q", typed.Struct)
		return false
	}
}
