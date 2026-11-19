package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// core is the core brain's caller, which grants family delegate and
// read and work files; loaded are the brains that run.
func core(loaded ...string) Caller {
	return Caller{
		Brain: "core",
		Manifest: manifest.Manifest{ID: "core", Role: manifest.RoleCore, Grants: map[string][]string{
			"family": {manifest.GrantDelegate, manifest.GrantRead},
			"work":   {manifest.GrantFiles},
		}},
		Running: func(id string) bool { return slices.Contains(loaded, id) },
	}
}

func TestCaller(t *testing.T) {
	c := core("family", "work")
	if !c.Core() {
		t.Error("the core brain's own run is not a core run")
	}
	if got := c.Granted(manifest.GrantDelegate); !slices.Equal(got, []string{"family"}) {
		t.Errorf("delegate: %v", got)
	}
	if got := c.Granted(""); !slices.Equal(got, []string{"family", "work"}) {
		t.Errorf("every grant: %v", got)
	}
	if !c.MayRead("core") || !c.MayRead("family") || c.MayRead("work") || c.MayRead("other") {
		t.Error("a core run reads itself and what it has a read grant on, nothing else")
	}

	stopped := core("work")
	if got := stopped.Granted(manifest.GrantDelegate); got != nil {
		t.Errorf("a brain that is not loaded is granted: %v", got)
	}
	if stopped.MayRead("family") {
		t.Error("a core run reads a brain that is not loaded")
	}

	delegated := core("family", "work")
	delegated.Delegated = true
	if delegated.Core() || delegated.Granted("") != nil || delegated.MayRead("family") {
		t.Error("a task delegated to the core brain reaches other brains")
	}
	if !delegated.MayRead("core") {
		t.Error("a delegated run does not read its own brain")
	}

	sub := Caller{Brain: "family", Manifest: manifest.Manifest{ID: "family"}, Running: func(string) bool { return true }}
	if sub.Core() || sub.Granted("") != nil || sub.MayRead("core") || !sub.MayRead("family") {
		t.Error("a sub-brain's run reaches other brains, or not its own")
	}
}

func TestBrainList(t *testing.T) {
	names := map[string]string{"family": "Family", "work": "work"}
	name := func(id string) string { return names[id] }
	if got := BrainList([]string{"family", "work", "taxes"}, name); got != "family (Family), work, taxes" {
		t.Errorf("BrainList: %q", got)
	}
	if got := BrainList(nil, name); got != "" {
		t.Errorf("no brains: %q", got)
	}
}

// fakeGuard is a Guard whose state the test sets; it records the
// approvals it was asked to use.
type fakeGuard struct {
	locked bool
	budget skill.Risk
	refuse error
	used   []string
}

func (g *fakeGuard) Locked() bool             { return g.locked }
func (g *fakeGuard) Budget(string) skill.Risk { return g.budget }
func (g *fakeGuard) UseApproval(_ context.Context, c, id string) error {
	g.used = append(g.used, c+"/"+id)
	return g.refuse
}

// done is a tool that says it ran.
func done(_ context.Context, c string, args json.RawMessage) (*mcp.CallResult, error) {
	return mcp.TextResult("done for " + c + " with " + string(args)), nil
}

// text is the one text of a call's result, which must have no error.
func text(t *testing.T) func(*mcp.CallResult, error) string {
	return func(res *mcp.CallResult, err error) string {
		t.Helper()
		if err != nil || res == nil || len(res.Content) != 1 {
			t.Fatalf("result %+v, error %v", res, err)
		}
		return res.Content[0].Text
	}
}

func TestGuarded(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		guard  fakeGuard
		risk   skill.Risk
		args   string
		want   string
		isErr  bool
		usedID string
	}{
		{name: "read in a lockdown", guard: fakeGuard{locked: true, budget: skill.RiskRead}, risk: skill.RiskRead, args: `{}`, want: "done for a with {}"},
		{name: "an unknown risk counts as read", guard: fakeGuard{locked: true}, risk: "", args: `{}`, want: "done for a with {}"},
		{name: "write in a lockdown", guard: fakeGuard{locked: true, budget: skill.RiskWrite}, risk: skill.RiskWrite, args: `{}`, want: "the bridge is in lockdown: only reading goes ahead. Do not try another way; say so in your reply.", isErr: true},
		{name: "write on a read budget", guard: fakeGuard{budget: skill.RiskRead}, risk: skill.RiskWrite, args: `{}`, want: "this run's risk level is read, which does not allow writing", isErr: true},
		{name: "write on a write budget", guard: fakeGuard{budget: skill.RiskWrite}, risk: skill.RiskWrite, args: `{"x":1}`, want: `done for a with {"x":1}`},
		{name: "irreversible in a lockdown", guard: fakeGuard{locked: true, budget: skill.RiskIrreversible}, risk: skill.RiskIrreversible, args: `{"approval_id":"a1"}`, want: "the bridge is in lockdown: only reading goes ahead. Do not try another way; say so in your reply.", isErr: true},
		{name: "irreversible without an approval", guard: fakeGuard{budget: skill.RiskIrreversible}, risk: skill.RiskIrreversible, args: `{}`, want: "this tool takes an irreversible step: call request_approval first, and pass the approval_id it answers approved with", isErr: true},
		{name: "irreversible with arguments that are not an object", guard: fakeGuard{budget: skill.RiskIrreversible}, risk: skill.RiskIrreversible, args: `[]`, want: "this tool takes an irreversible step: call request_approval first, and pass the approval_id it answers approved with", isErr: true},
		{name: "irreversible with a refused approval", guard: fakeGuard{budget: skill.RiskIrreversible, refuse: errors.New("approval a1 was used already")}, risk: skill.RiskIrreversible, args: `{"approval_id":"a1"}`, want: "approval a1 was used already. Do not take the step.", isErr: true, usedID: "a/a1"},
		{name: "irreversible with an approval, on a read budget", guard: fakeGuard{budget: skill.RiskRead}, risk: skill.RiskIrreversible, args: `{"approval_id":"a1"}`, want: `done for a with {"approval_id":"a1"}`, usedID: "a/a1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := tc.guard
			res, err := Guarded[string](&g, tc.risk, done)(ctx, "a", json.RawMessage(tc.args))
			if got := text(t)(res, err); got != tc.want || res.IsError != tc.isErr {
				t.Errorf("got %q (error %v), want %q (error %v)", got, res.IsError, tc.want, tc.isErr)
			}
			if want := []string{tc.usedID}; tc.usedID == "" && g.used != nil || tc.usedID != "" && !slices.Equal(g.used, want) {
				t.Errorf("approvals used: %v, want %q", g.used, tc.usedID)
			}
		})
	}
}

// key is a made-up OpenAI key, put together so that no line holds one.
const key = "sk-" + "proj-A1b2C3d4E5f6G7h8J9k0L1m2N3p4Q5r6S7t8"

func TestGuardedOutbound(t *testing.T) {
	ctx := context.Background()
	refusal := func(arg string) string {
		return "the argument " + arg + " looks like a secret (an API key or access token), and this tool sends it off this machine: do not send it. " +
			"If the owner wants exactly this sent, ask them with request_approval, saying what goes to whom without the value itself, " +
			"and call again with the approval_id it answers approved with; otherwise leave the value out."
	}
	for _, tc := range []struct {
		name   string
		guard  fakeGuard
		risk   skill.Risk
		args   string
		want   string
		isErr  bool
		usedID string
	}{
		{name: "ordinary text", guard: fakeGuard{budget: skill.RiskWrite}, risk: skill.RiskWrite,
			args: `{"message":"See you at 8, commit 3f2a9c1e5b7d4f6a8c0e2b4d6f8a0c2e4b6d8f0a, run 123e4567-e89b-12d3-a456-426614174000"}`,
			want: `done for a with {"message":"See you at 8, commit 3f2a9c1e5b7d4f6a8c0e2b4d6f8a0c2e4b6d8f0a, run 123e4567-e89b-12d3-a456-426614174000"}`},
		{name: "a key in the message", guard: fakeGuard{budget: skill.RiskWrite}, risk: skill.RiskWrite,
			args: `{"message":"here it is: ` + key + `"}`, want: refusal("message"), isErr: true},
		{name: "a password in a nested list", guard: fakeGuard{budget: skill.RiskWrite}, risk: skill.RiskWrite,
			args: `{"message":"hi","repos":["web",{"note":"password: hunter2"}]}`, isErr: true,
			want: "the argument repos[1].note looks like a secret (a password, token, secret, API key or PIN with its value), and this tool sends it off this machine: do not send it. " +
				"If the owner wants exactly this sent, ask them with request_approval, saying what goes to whom without the value itself, " +
				"and call again with the approval_id it answers approved with; otherwise leave the value out."},
		{name: "the first argument by name", guard: fakeGuard{budget: skill.RiskWrite}, risk: skill.RiskWrite,
			args: `{"text":"` + key + `","brief":"` + key + `"}`, want: refusal("brief"), isErr: true},
		{name: "a key with an approval", guard: fakeGuard{budget: skill.RiskWrite}, risk: skill.RiskWrite,
			args: `{"message":"` + key + `","approval_id":"a1"}`, want: `done for a with {"message":"` + key + `","approval_id":"a1"}`, usedID: "a/a1"},
		{name: "a key with a refused approval", guard: fakeGuard{budget: skill.RiskWrite, refuse: errors.New("approval a1 was used already: ask again for another action")}, risk: skill.RiskWrite,
			args: `{"message":"` + key + `","approval_id":"a1"}`, isErr: true, usedID: "a/a1",
			want: "the argument message looks like a secret (an API key or access token), and approval a1 was used already: ask again for another action. Do not send it."},
		{name: "an approval_id without a secret is not used", guard: fakeGuard{budget: skill.RiskWrite}, risk: skill.RiskWrite,
			args: `{"message":"hi","approval_id":"a1"}`, want: `done for a with {"message":"hi","approval_id":"a1"}`},
		{name: "a read tool is scanned too", guard: fakeGuard{budget: skill.RiskRead, locked: true}, risk: skill.RiskRead,
			args: `{"brief":"` + key + `"}`, want: refusal("brief"), isErr: true},
		{name: "the lockdown comes first", guard: fakeGuard{budget: skill.RiskWrite, locked: true}, risk: skill.RiskWrite,
			args: `{"message":"` + key + `"}`, want: "the bridge is in lockdown: only reading goes ahead. Do not try another way; say so in your reply.", isErr: true},
		{name: "an irreversible tool's approval covers it", guard: fakeGuard{budget: skill.RiskWrite}, risk: skill.RiskIrreversible,
			args: `{"message":"` + key + `","approval_id":"a1"}`, want: `done for a with {"message":"` + key + `","approval_id":"a1"}`, usedID: "a/a1"},
		{name: "arguments that are not an object", guard: fakeGuard{budget: skill.RiskWrite}, risk: skill.RiskWrite,
			args: `["` + key + `"]`, want: `done for a with ["` + key + `"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := tc.guard
			res, err := GuardedOutbound[string](&g, tc.risk, done)(ctx, "a", json.RawMessage(tc.args))
			if got := text(t)(res, err); got != tc.want || res.IsError != tc.isErr {
				t.Errorf("got %q (error %v), want %q (error %v)", got, res.IsError, tc.want, tc.isErr)
			}
			if want := []string{tc.usedID}; tc.usedID == "" && g.used != nil || tc.usedID != "" && !slices.Equal(g.used, want) {
				t.Errorf("approvals used: %v, want %q", g.used, tc.usedID)
			}
		})
	}
}

func TestSetAuditsOutboundSecrets(t *testing.T) {
	g := &fakeGuard{budget: skill.RiskWrite}
	s, lines := newTestSet(g)
	s.Register(mcp.Entry[string]{
		Name:     "send",
		Describe: func(string) mcp.Tool { return mcp.Tool{} },
		Call:     s.GuardedOutbound(skill.RiskWrite, done),
	})
	s.Register(mcp.Entry[string]{
		Name:     "write",
		Describe: func(string) mcp.Tool { return mcp.Tool{} },
		Call:     s.Guarded(skill.RiskWrite, done),
	})
	ctx := context.Background()
	secret := json.RawMessage(`{"text":"` + key + `"}`)
	if res, _ := s.Call(ctx, "a", "send", secret); res == nil || !res.IsError {
		t.Errorf("a secret went out: %+v", res)
	}
	if got := text(t)(s.Call(ctx, "a", "send", json.RawMessage(`{"text":"hello"}`))); got != `done for a with {"text":"hello"}` {
		t.Errorf("ordinary text: %q", got)
	}
	if got := text(t)(s.Call(ctx, "a", "send", json.RawMessage(`{"text":"`+key+`","approval_id":"a7"}`))); !strings.HasPrefix(got, "done for a") {
		t.Errorf("an approved secret: %q", got)
	}
	g.refuse = errors.New("approval a7 was used already")
	if res, _ := s.Call(ctx, "a", "send", json.RawMessage(`{"text":"`+key+`","approval_id":"a7"}`)); res == nil || !res.IsError {
		t.Errorf("a used approval let a secret out: %+v", res)
	}
	if got := text(t)(s.Call(ctx, "a", "write", secret)); !strings.HasPrefix(got, "done for a") {
		t.Errorf("a tool that is not outbound was scanned: %q", got)
	}
	want := []auditLine{
		{"a", "send", "refused: secret-like value"},
		{"a", "send", "ok"},
		{"a", "send", "ok (a secret-like value, approval a7)"},
		{"a", "send", "refused: secret-like value"},
		{"a", "write", "ok"},
	}
	if !slices.Equal(*lines, want) {
		t.Errorf("audit lines:\n%v\nwant\n%v", *lines, want)
	}
}

// auditLine is one line Set.Call handed to its audit appender.
type auditLine struct{ caller, tool, outcome string }

func newTestSet(g *fakeGuard) (*Set[string], *[]auditLine) {
	var lines []auditLine
	s := NewSet[string](g, func(c, tool, outcome string) { lines = append(lines, auditLine{c, tool, outcome}) })
	return s, &lines
}

func TestSetListsAndCallsOnlyAvailableTools(t *testing.T) {
	s, lines := newTestSet(&fakeGuard{budget: skill.RiskWrite})
	s.Register(mcp.Entry[string]{
		Name: "echo",
		Describe: func(string) mcp.Tool {
			return mcp.Tool{Description: "Echo.", InputSchema: Schema(map[string]any{"text": Str("What to echo.")}, "text")}
		},
		Call: done,
	})
	s.Register(mcp.Entry[string]{
		Name:      "core_only",
		Available: func(c string) bool { return c == "core" },
		Describe:  func(string) mcp.Tool { return mcp.Tool{Description: "Only the core's."} },
		Call:      done,
	})
	names := func(c string) []string {
		var out []string
		for _, tool := range s.List(c) {
			out = append(out, tool.Name)
		}
		return out
	}
	if got := names("core"); !slices.Equal(got, []string{"echo", "core_only"}) {
		t.Errorf("the core's tools: %v", got)
	}
	if got := names("family"); !slices.Equal(got, []string{"echo"}) {
		t.Errorf("a sub-brain's tools: %v", got)
	}

	ctx := context.Background()
	for _, args := range []string{"", "null"} {
		if got := text(t)(s.Call(ctx, "family", "echo", json.RawMessage(args))); got != "done for family with {}" {
			t.Errorf("arguments %q came as: %q", args, got)
		}
	}
	if got := text(t)(s.Call(ctx, "family", "echo", json.RawMessage(`{"text":"hi"}`))); got != `done for family with {"text":"hi"}` {
		t.Errorf("arguments: %q", got)
	}
	if _, err := s.Call(ctx, "family", "core_only", nil); !errors.Is(err, mcp.ErrUnknownTool) {
		t.Errorf("a tool the caller does not have: %v", err)
	}
	if _, err := s.Call(ctx, "core", "nope", nil); !errors.Is(err, mcp.ErrUnknownTool) {
		t.Errorf("a tool that does not exist: %v", err)
	}
	want := []auditLine{
		{"family", "echo", "ok"},
		{"family", "echo", "ok"},
		{"family", "echo", "ok"},
		{"family", "core_only", "refused: not this run's tool"},
		{"core", "nope", "refused: not this run's tool"},
	}
	if !slices.Equal(*lines, want) {
		t.Errorf("audit lines:\n%v\nwant\n%v", *lines, want)
	}
}

func TestSetGuardsAndAudits(t *testing.T) {
	g := &fakeGuard{budget: skill.RiskRead}
	s, lines := newTestSet(g)
	s.Register(mcp.Entry[string]{
		Name:     "write",
		Describe: func(string) mcp.Tool { return mcp.Tool{} },
		Call:     s.Guarded(skill.RiskWrite, done),
	})
	ctx := context.Background()
	if got := text(t)(s.Call(ctx, "a", "write", nil)); !strings.Contains(got, "does not allow writing") {
		t.Errorf("a write tool on a read budget: %q", got)
	}
	g.budget = skill.RiskWrite
	if got := text(t)(s.Call(ctx, "a", "write", nil)); got != "done for a with {}" {
		t.Errorf("a write tool on a write budget: %q", got)
	}
	want := []auditLine{
		{"a", "write", "error: this run's risk level is read, which does not allow writing"},
		{"a", "write", "ok"},
	}
	if !slices.Equal(*lines, want) {
		t.Errorf("audit lines: %v, want %v", *lines, want)
	}
}

func TestOutcome(t *testing.T) {
	for _, tc := range []struct {
		res  *mcp.CallResult
		err  error
		want string
	}{
		{mcp.TextResult("done"), nil, "ok"},
		{mcp.TextResult("approved: go ahead"), nil, "approved"},
		{mcp.TextResult("denied: no"), nil, "denied"},
		{mcp.TextResult("pending: ask again"), nil, "pending"},
		{mcp.TextResult("answered: yes"), nil, "answered"},
		{mcp.TextResult("expired: too late"), nil, "expired"},
		{mcp.TextResult("approval: not a word of the list"), nil, "ok"},
		{mcp.ErrorResult("no such file"), nil, "error: no such file"},
		{&mcp.CallResult{IsError: true}, nil, "error"},
		{&mcp.CallResult{}, nil, "ok"},
		{nil, nil, "ok"},
		{nil, errors.New("disk full"), "failed"},
		{nil, mcp.ErrUnknownTool, "refused: not this run's tool"},
	} {
		if got := outcome(tc.res, tc.err); got != tc.want {
			t.Errorf("outcome(%+v, %v) = %q, want %q", tc.res, tc.err, got, tc.want)
		}
	}
}

func TestSchema(t *testing.T) {
	if got := string(Schema(map[string]any{"path": Str("Where.")}, "path")); got != `{"additionalProperties":false,"properties":{"path":{"description":"Where.","type":"string"}},"required":["path"],"type":"object"}` {
		t.Errorf("with a required property: %s", got)
	}
	if got := string(Schema(nil)); got != `{"additionalProperties":false,"properties":{},"type":"object"}` {
		t.Errorf("without properties: %s", got)
	}
}
