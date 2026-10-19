package textx

import "testing"

func TestValidEnvName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"A", "a", "_", "PATH", "A_0", "a1B2"} {
		if !ValidEnvName(name) {
			t.Errorf("ValidEnvName(%q) = false", name)
		}
	}
	for _, name := range []string{"", "0", "1A", "A=B", "A\x00B", "A B", "A-B", "A\n", "é", "变量"} {
		if ValidEnvName(name) {
			t.Errorf("ValidEnvName(%q) = true", name)
		}
	}
}

// TestSBPLString: paths go into the profile as raw UTF-8 with only backslash
// and double quote escaped. Go's %q wrote \u00a0 and \u200d escapes the
// profile reader does not decode, so those rules never matched.
func TestSBPLString(t *testing.T) {
	cases := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"plain", "/Users/you/a", `"/Users/you/a"`, false},
		{"space", "/Users/you/My Brain", `"/Users/you/My Brain"`, false},
		{"no-break space", "/Users/you/My\u00a0Brain", "\"/Users/you/My\u00a0Brain\"", false},
		{"narrow no-break space", "/x/10.00\u202fAM.png", "\"/x/10.00\u202fAM.png\"", false},
		{"emoji with joiners", "/x/\U0001F468\u200d\U0001F469\u200d\U0001F467", "\"/x/\U0001F468\u200d\U0001F469\u200d\U0001F467\"", false},
		{"tab", "/x/a\tb", "\"/x/a\tb\"", false},
		{"double quote", `/x/say "hi"`, `"/x/say \"hi\""`, false},
		{"backslash", `/x/a\b`, `"/x/a\\b"`, false},
		{"NUL", "/x/a\x00b", "", true},
		{"newline", "/x/a\nb", "", true},
		{"carriage return", "/x/a\rb", "", true},
		{"invalid UTF-8", "/x/a\xffb", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SBPLString(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("SBPLString(%q) error = %v, want error %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("SBPLString(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}
