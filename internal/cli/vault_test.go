package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVaultUsageGolden(t *testing.T) {
	assertGolden(t, "cli/vault-usage.golden", []byte(vaultUsage))
}

func TestVaultCmdArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{nil, 2, "fridolin vault status"},
		{[]string{"--help"}, 0, "fridolin vault get"},
		{[]string{"bogus", "--config", "/nonexistent/config.json"}, 1, "loading config"},
	} {
		var out, errb bytes.Buffer
		code := vaultCmd(tc.args, &out, &errb)
		if code != tc.code || !strings.Contains(out.String()+errb.String(), tc.want) {
			t.Errorf("vault %q: exit %d, out %q, err %q", tc.args, code, out.String(), errb.String())
		}
	}
}
