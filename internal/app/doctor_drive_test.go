package app

import (
	"context"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/doctor"
)

// TestDoctorDriveClient: doctor warns while a Drive remote in the bridge's
// rclone config uses rclone's shared client (no client_id), with the fix,
// and is fine without one or once every Drive remote has its own client.
func TestDoctorDriveClient(t *testing.T) {
	t.Parallel()
	const conf = "/home/you/.fridolin/rclone/rclone.conf"
	cases := []struct {
		name, file, status, detail string
	}{
		{"not connected", "", doctor.OK, "not connected"},
		{"own client", "[brain-gdrive]\ntype = drive\nclient_id = 1-x\nclient_secret = s\n", doctor.OK, "no Drive remote"},
		{"shared client", "[brain-gdrive]\ntype = drive\nscope = drive.file\ntoken = {}\n\n[alias]\ntype = alias\n", doctor.Warn, "brain-gdrive uses rclone's shared Google client"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			s.contents = map[string]string{}
			if tc.file != "" {
				s.contents[conf] = tc.file
			}
			rep := doctor.Run(context.Background(), s.doctorEnv(doctorConfig(nil), nil), DoctorRegistry.Checks())
			got := result(t, rep, "drive-client")
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) || got.Required {
				t.Errorf("%+v, want %s with %q", got, tc.status, tc.detail)
			}
			if tc.status == doctor.Warn && !strings.Contains(got.Fix, "Backup → Switch to your own Google client") {
				t.Errorf("fix = %q", got.Fix)
			}
		})
	}
}
