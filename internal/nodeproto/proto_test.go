package nodeproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func newKey(t testing.TB) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	pub, _ := newKey(t)
	hello := Hello{DeviceID: DeviceIDFor(pub), PublicKey: pub, Name: "laptop", OS: "windows", Arch: "amd64", Version: "1.0",
		Capabilities: Capabilities{Exec: &ExecCap{Allow: DefaultExecAllow("windows")}, Host: true}}
	data, err := Encode(TypeHello, "", hello)
	if err != nil {
		t.Fatal(err)
	}
	env, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeBody[Hello](env)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Check(); err != nil {
		t.Fatalf("a hello that went through the wire: %v", err)
	}
	if got.Name != "laptop" || len(got.Capabilities.Exec.Allow) != 3 || got.Capabilities.Exec.Allow[0].Args == nil {
		t.Errorf("round trip: %+v", got)
	}
}

func TestDecodeRefuses(t *testing.T) {
	for name, in := range map[string]string{
		"not json":         `{`,
		"other version":    `{"v":2,"type":"hello"}`,
		"no version":       `{"type":"hello"}`,
		"unknown type":     `{"v":1,"type":"exec"}`,
		"request no id":    `{"v":1,"type":"request"}`,
		"response no id":   `{"v":1,"type":"response"}`,
		"id too long":      `{"v":1,"type":"request","id":"` + strings.Repeat("x", MaxIDLen+1) + `"}`,
		"array":            `[]`,
		"type not string":  `{"v":1,"type":3}`,
		"body not message": `{"v":1,"type":"hello","body":`,
	} {
		if _, err := Decode([]byte(in)); err == nil {
			t.Errorf("%s: decoded %s", name, in)
		}
	}
	if _, err := Decode([]byte(`{"v":2,"type":"hello"}`)); !errors.Is(err, ErrVersion) {
		t.Errorf("another version: %v, want ErrVersion", err)
	}
}

func TestHelloCheck(t *testing.T) {
	pub, _ := newKey(t)
	other, _ := newKey(t)
	ok := Hello{DeviceID: DeviceIDFor(pub), PublicKey: pub, Name: "n", OS: "linux", Arch: "arm64"}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Hello){
		"short key":        func(h *Hello) { h.PublicKey = h.PublicKey[:10] },
		"another's id":     func(h *Hello) { h.DeviceID = DeviceIDFor(other) },
		"no name":          func(h *Hello) { h.Name = "" },
		"long name":        func(h *Hello) { h.Name = strings.Repeat("n", MaxNameLen+1) },
		"control in name":  func(h *Hello) { h.Name = "a\nb" },
		"newline in os":    func(h *Hello) { h.OS = "linux\n" },
		"shell allowed":    func(h *Hello) { h.Capabilities.Exec = &ExecCap{Allow: []ExecRule{{Name: "bash"}}} },
		"relative root":    func(h *Hello) { h.Capabilities.FS = &FSCap{Roots: []FSRoot{{Path: "docs"}}} },
		"bad pattern":      func(h *Hello) { h.Capabilities.Exec = &ExecCap{Allow: []ExecRule{{Name: "git", Args: []string{"("}}}} },
		"relative program": func(h *Hello) { h.Capabilities.Exec = &ExecCap{Allow: []ExecRule{{Name: "bin/tool"}}} },
	} {
		h := ok
		mutate(&h)
		if err := h.Check(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestSignVerify(t *testing.T) {
	pub, priv := newKey(t)
	id := DeviceIDFor(pub)
	nonce := make([]byte, NonceSize)
	_, _ = rand.Read(nonce)
	sig := Sign(priv, id, nonce)
	if !Verify(pub, id, nonce, sig) {
		t.Fatal("a good signature does not verify")
	}
	other := make([]byte, NonceSize)
	if Verify(pub, id, other, sig) {
		t.Error("a signature verifies for another nonce")
	}
	if Verify(pub, "000000000000", nonce, sig) {
		t.Error("a signature verifies for another device")
	}
	if Verify(pub, id, nonce[:8], Sign(priv, id, nonce[:8])) {
		t.Error("a short nonce verifies")
	}
}

func TestIsShell(t *testing.T) {
	for _, s := range []string{"sh", "/bin/bash", `C:\Windows\System32\cmd.exe`, "PowerShell.exe", "pwsh", "wsl", "zsh", "fish", "/usr/bin/env", "sudo"} {
		if !IsShell(s) {
			t.Errorf("%s is not a shell", s)
		}
	}
	for _, s := range []string{"git", "go", "/usr/bin/uname", "hostname", "shasum"} {
		if IsShell(s) {
			t.Errorf("%s is a shell", s)
		}
	}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, r := range DefaultExecAllow(goos) {
			if err := r.Check(); err != nil {
				t.Errorf("%s default %s: %v", goos, r.Name, err)
			}
		}
	}
}

func TestPermitsExec(t *testing.T) {
	c := Capabilities{Exec: &ExecCap{Allow: []ExecRule{
		{Name: "git", Args: []string{"status", "log", "--oneline"}},
		{Name: "hostname", Args: []string{}},
		{Name: "/usr/local/bin/tool"},
	}}}
	for _, argv := range [][]string{{"git", "status"}, {"git", "log", "--oneline"}, {"git"}, {"hostname"}, {"/usr/local/bin/tool", "anything", "at all"}} {
		if err := c.PermitsExec(argv); err != nil {
			t.Errorf("%q: %v", argv, err)
		}
	}
	for _, argv := range [][]string{{"git", "push"}, {"git", "status", "-c", "x"}, {"hostname", "evil"}, {"tool"}, {"sh", "-c", "id"}, {}, {"Git", "status"}, {"git", "statusX"}} {
		if err := c.PermitsExec(argv); err == nil {
			t.Errorf("%q is allowed", argv)
		}
	}
	// A shell is refused even by a rule that names it (which Check refuses
	// to declare in the first place).
	bad := Capabilities{Exec: &ExecCap{Allow: []ExecRule{{Name: "bash"}}}}
	if err := bad.PermitsExec([]string{"bash", "-c", "id"}); err == nil {
		t.Error("a shell is allowed")
	}
}

func TestCoversAndIntersect(t *testing.T) {
	approved := Capabilities{
		Exec:   &ExecCap{Allow: []ExecRule{{Name: "git", Args: []string{"status"}}, {Name: "hostname", Args: []string{}}}},
		FS:     &FSCap{Roots: []FSRoot{{Path: "/srv/a", Write: true}, {Path: "/srv/b"}}},
		Host:   true,
		Notify: false,
	}
	if !Covers(approved, approved) {
		t.Fatal("approval does not cover itself")
	}
	narrower := Capabilities{Exec: &ExecCap{Allow: []ExecRule{{Name: "hostname", Args: []string{}}}}, FS: &FSCap{Roots: []FSRoot{{Path: "/srv/a"}}}}
	if !Covers(approved, narrower) {
		t.Error("a narrower declaration needs approval")
	}
	for name, grown := range map[string]Capabilities{
		"new command":      {Exec: &ExecCap{Allow: []ExecRule{{Name: "uname"}}}},
		"wider args":       {Exec: &ExecCap{Allow: []ExecRule{{Name: "git"}}}},
		"no args to none":  {Exec: &ExecCap{Allow: []ExecRule{{Name: "hostname"}}}},
		"longer timeout":   {Exec: &ExecCap{Allow: []ExecRule{{Name: "hostname", Args: []string{}}}, TimeoutSeconds: 600}},
		"write on b":       {FS: &FSCap{Roots: []FSRoot{{Path: "/srv/b", Write: true}}}},
		"new root":         {FS: &FSCap{Roots: []FSRoot{{Path: "/srv/c"}}}},
		"notify":           {Notify: true},
		"host on no exec":  {Host: true, Exec: &ExecCap{Allow: []ExecRule{{Name: "id"}}}},
		"root on no roots": {FS: &FSCap{Roots: []FSRoot{{Path: "/"}}}},
	} {
		if Covers(approved, grown) {
			t.Errorf("%s: covered", name)
		}
		if Covers(Capabilities{}, grown) {
			t.Errorf("%s: covered by nothing", name)
		}
	}
	grown := Capabilities{
		Exec: &ExecCap{Allow: []ExecRule{{Name: "git", Args: []string{"status"}}, {Name: "uname"}}},
		FS:   &FSCap{Roots: []FSRoot{{Path: "/srv/a", Write: true}, {Path: "/srv/b", Write: true}, {Path: "/srv/c"}}},
		Host: true, Notify: true,
	}
	got := Intersect(grown, approved)
	want := Capabilities{
		Exec: &ExecCap{Allow: []ExecRule{{Name: "git", Args: []string{"status"}}}, TimeoutSeconds: DefaultExecTimeoutSeconds, MaxOutputBytes: DefaultMaxOutputBytes},
		FS:   &FSCap{Roots: []FSRoot{{Path: "/srv/a", Write: true}, {Path: "/srv/b"}}},
		Host: true,
	}
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("intersect:\n got %s\nwant %s", g, w)
	}
	if !Intersect(grown, Capabilities{}).Empty() {
		t.Error("intersect with nothing is not empty")
	}
	if !got.Allows(MethodFSWrite) || !got.Allows(MethodExec) || got.Allows(MethodNotify) || !got.Allows(MethodHostInfo) || got.Allows("nope") {
		t.Errorf("Allows of %s", g)
	}
	if s := got.Summary(); s != "exec (git), fs (2 roots, 1 writable), host" {
		t.Errorf("summary %q", s)
	}
}

func TestArgsNullAndEmptyStayApart(t *testing.T) {
	data, err := json.Marshal(ExecCap{Allow: []ExecRule{{Name: "a"}, {Name: "b", Args: []string{}}}})
	if err != nil {
		t.Fatal(err)
	}
	var back ExecCap
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Allow[0].Args != nil || back.Allow[1].Args == nil {
		t.Errorf("after %s: %#v", data, back.Allow)
	}
}

// FuzzDecode feeds arbitrary bytes through what a peer does with a
// message it reads: the envelope, the body its type names, and the checks.
// None may panic, and what decodes must encode again.
func FuzzDecode(f *testing.F) {
	pub, _ := newKey(f)
	seed, _ := Encode(TypeHello, "", Hello{DeviceID: DeviceIDFor(pub), PublicKey: pub, Name: "n", OS: "linux", Arch: "amd64",
		Capabilities: Capabilities{Exec: &ExecCap{Allow: DefaultExecAllow("linux")}, FS: &FSCap{Roots: []FSRoot{{Path: "/srv"}}}, Host: true}})
	f.Add(seed)
	req, _ := Encode(TypeRequest, "1", Request{Method: MethodExec, Params: json.RawMessage(`{"argv":["uname","-a"]}`)})
	f.Add(req)
	f.Add([]byte(`{"v":1,"type":"response","id":"x","body":{"error":{"code":"denied","message":"no"}}}`))
	f.Add([]byte(`{"v":1,"type":"state","body":{"device":"approved","commands":"pending","approved":{"exec":{"allow":[{"name":"git","args":["(a|b)*"]}]}}}}`))
	f.Add([]byte(`{"v":1,"type":"close","body":{"reason":"revoked"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		env, err := Decode(data)
		if err != nil {
			return
		}
		var body any
		switch env.Type {
		case TypeHello:
			h, err := DecodeBody[Hello](env)
			if err == nil {
				_ = h.Check()
				body = h
			}
		case TypeWelcome, TypeState:
			s, err := DecodeBody[State](env)
			if err == nil {
				_ = s.Approved.Check()
				_ = s.Approved.PermitsExec([]string{"git", "status"})
				_ = Covers(s.Approved, s.Approved)
				_ = Intersect(s.Approved, s.Approved).Summary()
				body = s
			}
		case TypeRequest:
			r, err := DecodeBody[Request](env)
			if err == nil {
				if p, err := DecodeBody[ExecParams](Envelope{Type: env.Type, Body: r.Params}); err == nil {
					_ = Capabilities{Exec: &ExecCap{Allow: DefaultExecAllow("linux")}}.PermitsExec(p.Argv)
				}
				body = r
			}
		case TypeResponse:
			if r, err := DecodeBody[Response](env); err == nil {
				body = r
			}
		case TypeClose:
			if c, err := DecodeBody[Close](env); err == nil {
				_ = Final(c.Reason)
				body = c
			}
		case TypeHeartbeat:
			if h, err := DecodeBody[Heartbeat](env); err == nil {
				body = h
			}
		}
		if body != nil {
			if _, err := Encode(env.Type, env.ID, body); err != nil {
				t.Fatalf("decoded %q but cannot encode it again: %v", data, err)
			}
		}
	})
}
