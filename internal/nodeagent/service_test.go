package nodeagent

import "testing"

func TestStateDirArg(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{`C:\Program Files\fridolin\fridolin.exe`, "node", "run", "--state-dir", `C:\Users\you\AppData\Local\fridolin-node`}, `C:\Users\you\AppData\Local\fridolin-node`},
		{[]string{"fridolin", "node", "run", `--state-dir=C:\Users\you\.fridolin-node`}, `C:\Users\you\.fridolin-node`},
		{[]string{"fridolin", "node", "run", "--state-dir"}, ""},
		{[]string{"fridolin", "node", "run"}, ""},
		{nil, ""},
	} {
		if got := stateDirArg(c.args); got != c.want {
			t.Errorf("stateDirArg(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}
