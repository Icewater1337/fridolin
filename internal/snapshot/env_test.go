package snapshot

import (
	"slices"
	"strings"
	"testing"
)

func TestMassDeletion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		prev, cur int
		want      bool
	}{
		{0, 0, false},
		{1, 0, true}, // all of one file: more than half
		{2, 1, false},
		{2, 0, true},
		{10, 5, false}, // half is allowed
		{10, 4, true},
		{20, 11, false}, // 9 gone: max(9, 20/5)
		{20, 10, true},
		{45, 36, false}, // 9 gone is always fine below the half
		{45, 35, true},  // 10 gone > max(9, 9)
		{100, 80, false},
		{100, 79, true}, // 21 > 100/5
		{1000, 800, false},
		{1000, 799, true},
		{100000, 99751, false}, // 249 gone: the cap
		{100000, 99750, true},
		{5, 10, false}, // files added
	} {
		if got := MassDeletion(tc.prev, tc.cur); got != tc.want {
			t.Errorf("MassDeletion(%d, %d) = %v, want %v", tc.prev, tc.cur, got, tc.want)
		}
	}
}

func TestRedact(t *testing.T) {
	t.Parallel()
	env := []string{
		"RESTIC_PASSWORD=pw-secret", "B2_ACCOUNT_ID=acct-id", "B2_ACCOUNT_KEY=b2-key",
		"LITESTREAM_SECRET_ACCESS_KEY=ls-secret", "AWS_SESSION_TOKEN=tok", "HOME=/Users/you", "EMPTY_KEY=",
	}
	secrets := secretValues(env, "extra-secret", "pw-secret-longer")
	in := "pw-secret-longer pw-secret acct-id b2-key ls-secret tok extra-secret /Users/you"
	got := redact(in, secrets)
	if want := "*** *** *** *** *** *** *** /Users/you"; got != want {
		t.Errorf("redact = %q, want %q", got, want)
	}
	long := strings.Repeat("x", 3*stderrLimit) + "END"
	if got := redact(long, nil); len(got) > stderrLimit+len("…") || !strings.HasSuffix(got, "END") {
		t.Errorf("redact kept %d bytes, want the last %d", len(got), stderrLimit)
	}
	if got := urlPassword("rest:https://user:hunter2@host:8000/repo"); got != "hunter2" {
		t.Errorf("urlPassword = %q", got)
	}
	if got := urlPassword("b2:bucket:path"); got != "" {
		t.Errorf("urlPassword of a b2 repo = %q", got)
	}
}

func TestTailBufferDropsCutLine(t *testing.T) {
	t.Parallel()
	b := &tailBuffer{limit: 16}
	_, _ = b.Write([]byte("secret-value-here\nshort\n"))
	if got := b.String(); got != "short\n" {
		t.Errorf("tail = %q, want the cut line dropped", got)
	}
}

func TestMergeEnv(t *testing.T) {
	t.Parallel()
	dst := []string{"RESTIC_PASSWORD=dst", "PATH=/bin", "B2_ACCOUNT_ID=a"}
	src := []string{"RESTIC_PASSWORD=src", "PATH=/bin", "B2_ACCOUNT_ID=a", "RCLONE_CONFIG=/x"}
	got, err := mergeEnv(dst, src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"RESTIC_PASSWORD=dst", "PATH=/bin", "B2_ACCOUNT_ID=a", "RESTIC_FROM_PASSWORD=src", "RCLONE_CONFIG=/x"}
	if !slices.Equal(got, want) {
		t.Errorf("mergeEnv = %q, want %q", got, want)
	}
	_, err = mergeEnv(dst, []string{"RESTIC_PASSWORD=src", "B2_ACCOUNT_ID=other-account"})
	if err == nil || !strings.Contains(err.Error(), "B2_ACCOUNT_ID") || strings.Contains(err.Error(), "other-account") {
		t.Errorf("clash error = %v, want one naming B2_ACCOUNT_ID without its value", err)
	}

	opts, err := mergeOptions([]string{"rclone.program=/a"}, []string{"rclone.program=/a", "b2.connections=2"})
	if err != nil || !slices.Equal(opts, []string{"rclone.program=/a", "b2.connections=2"}) {
		t.Errorf("mergeOptions = %q, %v", opts, err)
	}
	if _, err := mergeOptions([]string{"rclone.program=/a"}, []string{"rclone.program=/b"}); err == nil {
		t.Error("mergeOptions accepted two values for one option")
	}
}
