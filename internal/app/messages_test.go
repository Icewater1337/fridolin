package app

import (
	"errors"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

// catalogue is every message of messages.go, by the name it has there,
// as a chat gets it: each function once for each way it reads.
func catalogue() []struct{ name, text string } {
	return []struct{ name, text string }{
		{"noBrainNotice", outboundmsg.NoBrainNotice},
		{"heldBrainNotice", outboundmsg.HeldBrainNotice},
		{"restartedNotice", outboundmsg.RestartedNotice},
		{"resetReply(false)", outboundmsg.ResetReply(false)},
		{"resetReply(true)", outboundmsg.ResetReply(true)},
		{"stopReply(true, 0)", outboundmsg.StopReply(true, 0)},
		{"stopReply(false, 2)", outboundmsg.StopReply(false, 2)},
		{"stopReply(false, 0)", outboundmsg.StopReply(false, 0)},
		{`helpReply("")`, outboundmsg.HelpReply("")},
		{`helpReply("@claude")`, outboundmsg.HelpReply("@claude")},
		{"queuedNotice(2)", outboundmsg.QueuedNotice(2)},
		{"queueFullNotice(\"@claude !stop\")", outboundmsg.QueueFullNotice("@claude !stop")},
		{"otpCaughtNotice", outboundmsg.OtpCaughtNotice},
		{"workingNotice", outboundmsg.WorkingNotice},
		{"questionPrefix", outboundmsg.QuestionPrefix + "Which day?"},
		{"chatErrNotSignedIn", outboundmsg.ChatErrNotSignedIn},
		{"chatErrCodexNotSignedIn", outboundmsg.ChatErrCodexNotSignedIn},
		{"chatErrCodexLimit", outboundmsg.ChatErrCodexLimit},
		{"chatErrLimit", outboundmsg.ChatErrLimit},
		{"chatErrTimeout", outboundmsg.ChatErrTimeout},
		{"chatErrGeneric", outboundmsg.ChatErrGeneric},
		{"chatErrSandbox", outboundmsg.ChatErrSandbox(errors.New("the sandbox could not be set up"))},
		{"chatErrClaude", outboundmsg.ChatErrClaude("Credit balance is too low")},
		{"chatErrCodex", outboundmsg.ChatErrCodex("Something failed")},
		{"buildChatError", outboundmsg.BuildChatError(errors.New("brain queue is full"))},
		{"viewOnceNotice", outboundmsg.ViewOnceNotice},
		{"mediaTooLargeNotice", outboundmsg.MediaTooLargeNotice(100 << 20)},
		{"mediaFailedNotice", outboundmsg.MediaFailedNotice},
		{"pollAsText", outboundmsg.PollAsText(outboundmsg.Poll{Question: "Lunch on Friday?", Options: []string{"Pizza", "Sushi"}})},
		{"startupCheckAlert", outboundmsg.StartupCheckAlert("missing ~/.fridolin/credentials/google_calendar_token_main.json")},
		{"backupRefusedAlert", outboundmsg.BackupRefusedAlert("Notes", "it would delete 12 of 40 files")},
		{"plaudGivenUpAlert", outboundmsg.PlaudGivenUpAlert("Team sync", "notes", "brain notes is not configured")},
		{"plaudExpiredAlert", outboundmsg.PlaudExpiredAlert},
		{"calendarGrantBrokenAlert(revoked)", outboundmsg.CalendarGrantBrokenAlert("Family", true)},
		{"calendarGrantBrokenAlert(reconsent)", outboundmsg.CalendarGrantBrokenAlert("Family", false)},
		{"fleetGroupNotice", outboundmsg.FleetGroupNotice},
		{"fleetReply(false)", outboundmsg.FleetReply(false, nil, fleetNow)},
		{"fleetReply(true, none)", outboundmsg.FleetReply(true, nil, fleetNow)},
		{"fleetReply(true, nodes)", outboundmsg.FleetReply(true, fleetNodes, fleetNow)},
		{"nodesOffNotice", outboundmsg.NodesOffNotice},
		{"ownerCommandGroupNotice", outboundmsg.OwnerCommandGroupNotice},
		{"ownerCommandOwnerOnly", outboundmsg.OwnerCommandOwnerOnly},
		{"ownerCommandRateLimited", outboundmsg.OwnerCommandRateLimited("!join", 5)},
		{"nodeCommandUsage", outboundmsg.NodeCommandUsage},
		{"joinCodeReply", outboundmsg.JoinCodeReply("fj-abcdefghijklmnopqrstuvwx", "fridolin-core.example.ts.net:8766", outboundmsg.JoinLines{
			Sh:         "fridolin node join fj-abcdefghijklmnopqrstuvwx --core fridolin-core.example.ts.net:8766",
			PowerShell: `& "$env:ProgramFiles\fridolin\fridolin.exe" node join fj-abcdefghijklmnopqrstuvwx --core fridolin-core.example.ts.net:8766`}, 10)},
		{"joinCodeReply(published)", outboundmsg.JoinCodeReply("fj-abcdefghijklmnopqrstuvwx", "fridolin-core.example.ts.net:8766",
			joinLines("fj-abcdefghijklmnopqrstuvwx", "fridolin-core.example.ts.net:8766", httpapi.BoxSource{Source: "https://example.com/fridolin/0123abcd", Registry: "ghcr.io/owner", Version: "0123abcd"}), 10)},
		{"dashboardOffNotice", outboundmsg.DashboardOffNotice},
		{"dashboardLinkReply(tailnet)", outboundmsg.DashboardLinkReply("http://fridolin-core.example.ts.net/#login=CODE", "http://127.0.0.1:8765/#login=CODE", 10)},
		{"dashboardLinkReply(local)", outboundmsg.DashboardLinkReply("", "http://127.0.0.1:8765/#login=CODE", 10)},
		{"coreMoveEnded(done)", outboundmsg.CoreMoveEnded("m-20261024-1403-7f2a", "mac-mini", "icebase", "done", "the core runs on icebase (version v1.9.0, WhatsApp connected, lease held)",
			[]string{"Apple-only work (Xcode and iOS builds, osascript, launchctl) stays on this Mac: keep a native node here for it"})},
		{"coreMoveEnded(done, nothing left)", outboundmsg.CoreMoveEnded("m-20261024-1403-7f2a", "icebase", "mac-mini", "done", "the core runs on mac-mini (version v1.9.0, WhatsApp connected, lease held)", nil)},
		{"coreMoveEnded(rolled_back)", outboundmsg.CoreMoveEnded("m-20261024-1403-7f2a", "mac-mini", "icebase", "rolled_back", "mac-mini: 3c1e9a0b7d22 gave the core up (not ready in time); its state comes back with it; the core runs here again", nil)},
		{"coreMoveEnded(failed)", outboundmsg.CoreMoveEnded("m-20261024-1403-7f2a", "mac-mini", "icebase", "failed", "the rehearsal on icebase: the import failed; nothing was stopped", nil)},
		{"coreMoveEnded(unknown)", outboundmsg.CoreMoveEnded("m-20261024-1403-7f2a", "mac-mini", "icebase", "unknown", "neither host said how the move ended; see fridolin core status on both", nil)},
		{"coreMoveNow", outboundmsg.CoreMoveNow},
		{"coreMoveCancel", outboundmsg.CoreMoveCancel},
		{"coreMoveLockdownNotice", outboundmsg.CoreMoveLockdownNotice},
		{"coreMoveRunningNotice", outboundmsg.CoreMoveRunningNotice},
		{"coreMoveUsage", outboundmsg.CoreMoveUsage},
		{"coreMoveChecking", outboundmsg.CoreMoveChecking("icebase")},
		{"coreMoveTargets", outboundmsg.CoreMoveTargets("mac-mini", []outboundmsg.CoreMoveHost{
			{Name: "icebase", ID: "3c1e9a0b7d22", Online: true},
			{Name: "laptop", ID: "8233270f8a24", Online: true, Problem: "laptop runs version v1.8.0 and the core v1.9.0: update it first (fridolin nodes update 8233270f8a24)"},
			{Name: "pi", ID: "0a1b2c3d4e5f"},
			{Name: "nas", ID: "4f1e2d3c4b5a", Online: true, New: true},
		})},
		{"coreMoveTargets(none)", outboundmsg.CoreMoveTargets("mac-mini", nil)},
		{"coreMoveTargets(no source)", outboundmsg.CoreMoveTargets("", []outboundmsg.CoreMoveHost{{Name: "icebase", ID: "3c1e9a0b7d22", Online: true}})},
		{"coreMoveReport", outboundmsg.CoreMoveReport(outboundmsg.CoreMoveCheck{Source: "mac-mini", Target: "icebase", TargetID: "3c1e9a0b7d22", TargetOS: "windows", TargetArch: "amd64", Box: true,
			Fingerprint: "a1b2 c3d4 e5f6 0718 293a 4b5c 6d7e 8f90", Outage: "about 2 to 4 minutes",
			NotOnTarget: []string{"Apple-only work (Xcode and iOS builds, osascript, launchctl) stays on this Mac: keep a native node here for it"},
			Site:        "the site's tunnel runs inside the core and moves with it: example.com answers from icebase once the core runs there",
			Warnings:    []string{"icebase runs FrIDE: once the secrets are there, any FrIDE task can read them"}, PollMinutes: 30})},
		{"coreMoveReport(plain)", outboundmsg.CoreMoveReport(outboundmsg.CoreMoveCheck{Source: "icebase", Target: "mac-mini", TargetID: "7216c2648b04", TargetOS: "darwin", TargetArch: "arm64",
			Fingerprint: "a1b2 c3d4 e5f6 0718 293a 4b5c 6d7e 8f90", Outage: "about 2 to 4 minutes", PollMinutes: 30})},
		{"coreMoveReport(first time)", outboundmsg.CoreMoveReport(outboundmsg.CoreMoveCheck{Source: "mac-mini", Target: "icebase", TargetID: "3c1e9a0b7d22", TargetOS: "linux", TargetArch: "amd64", Box: true,
			Fingerprint: "a1b2 c3d4 e5f6 0718 293a 4b5c 6d7e 8f90", Outage: "about 2 to 4 minutes", PollMinutes: 30, FirstTime: true, FirstTimeNote: nodeproto.CoreHostApproval})},
		{"coreMoveReport(blocked)", outboundmsg.CoreMoveReport(outboundmsg.CoreMoveCheck{Source: "mac-mini", Target: "icebase", Blocking: []string{"mac-mini runs a fridolin that sends host secrets to pinned targets only: update it first (fridolin nodes update 7216c2648b04)"}})},
		{"coreMoveQuestion", outboundmsg.CoreMoveQuestion("mac-mini", "icebase")},
		{"coreMoveHereAlready", outboundmsg.CoreMoveHereAlready("mac-mini")},
		{"coreMoveNoSource", outboundmsg.CoreMoveNoSource},
		{"coreMoveStarted", outboundmsg.CoreMoveStarted("mac-mini", "icebase", "m-20261024-1403-7f2a")},
		{"coreMoveNotStarted", outboundmsg.CoreMoveNotStarted("icebase", "icebase is offline")},
		{"coreMoveCancelled(vote)", outboundmsg.CoreMoveCancelled("icebase", false)},
		{"coreMoveCancelled(expired)", outboundmsg.CoreMoveCancelled("icebase", true)},
		{"coreMoveStopping", outboundmsg.CoreMoveStopping("mac-mini", "icebase", "about 2 to 4 minutes")},
		{"coreSecretsQuestion", outboundmsg.CoreSecretsQuestion("mac-mini", 24)},
		{"coreSecretsKeep", outboundmsg.CoreSecretsKeep("mac-mini")},
		{"coreSecretsForget", outboundmsg.CoreSecretsForget("mac-mini")},
		{"coreSecretsKept", outboundmsg.CoreSecretsKept("mac-mini")},
		{"coreSecretsForgotten", outboundmsg.CoreSecretsForgotten("icebase", outboundmsg.CoreForgetReport{Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN", "FRIDOLIN_AGE_IDENTITY"}, Files: []string{"~/.config/fridolin/dashboard-users.json"}, Data: []string{"bridge.db", "brains-folders/Personal", "config.json", "core", "vault.db"}}, false)},
		{"coreSecretsForgotten(expired, none)", outboundmsg.CoreSecretsForgotten("mac-mini", outboundmsg.CoreForgetReport{}, true)},
		{"coreSecretsForgotten(own data)", outboundmsg.CoreSecretsForgotten("mac-mini", outboundmsg.CoreForgetReport{Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}, DataKept: "Its own data dir stays: it held the core's data before move m-20261024-1200-0001 imported into it"}, false)},
		{"coreSecretsForgotten(partly)", outboundmsg.CoreSecretsForgotten("icebase", outboundmsg.CoreForgetReport{Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}, Failure: "kept the core's data dir and the secret files a move brought: the core runs here"}, false)},
		{"coreSecretsForgotten(failed)", outboundmsg.CoreSecretsForgotten("icebase", outboundmsg.CoreForgetReport{Failure: "it is offline"}, false)},
		{"coreMoveSiteAnswers", outboundmsg.CoreMoveSiteAnswers("example.com", "icebase")},
		{"coreMoveSiteDown", outboundmsg.CoreMoveSiteDown("example.com", "icebase", "example.com does not resolve yet.")},
		{"nodeFrideApprovedNote", outboundmsg.NodeFrideApprovedNote(nodeproto.FrideApproval)},
		{"nodeRevokedCoreSecretsNote", outboundmsg.NodeRevokedCoreSecretsNote(outboundmsg.CoreForgetReport{Secrets: []string{"CLAUDE_CODE_OAUTH_TOKEN"}, Data: []string{"bridge.db", "config.json"}})},
		{"nodeRevokedCoreSecretsNote(none)", outboundmsg.NodeRevokedCoreSecretsNote(outboundmsg.CoreForgetReport{})},
		{"nodeRevokedCoreSecretsNote(failed)", outboundmsg.NodeRevokedCoreSecretsNote(outboundmsg.CoreForgetReport{Failure: "it is offline"})},
		{"nodeApprovedReply", outboundmsg.NodeApprovedReply("laptop", "8233270f8a24", nodeproto.Capabilities{Host: true, Notify: true, FS: &nodeproto.FSCap{Roots: []nodeproto.FSRoot{{Path: "/home/you/Shared", Write: true}}}}.Plain())},
		{"approvalAlert(node)", outboundmsg.ApprovalAlert(catalogueNodeApproval, fleetNow)},
		{"approvalAlert(fride, unexpected)", outboundmsg.ApprovalAlert(outboundmsg.OwnerApproval{
			Subject: "*icebase* (Linux) asks to let FrIDE's agents run anything on it (fride)", Label: "*icebase*", When: fleetNow.Add(-26 * time.Hour),
			Note:       nodeproto.FrideApproval,
			Facts:      []string{"From: your local network, 192.168.1.20"},
			Allows:     nodeproto.Capabilities{Host: true, Fride: &nodeproto.FrideCap{}}.Plain(),
			Unexpected: "the machine asked for this itself; nobody asked from WhatsApp or the dashboard.", Ref: "3c1e5a7b9d0f",
		}, fleetNow)},
		{"approvalAlert(schedule)", outboundmsg.ApprovalAlert(outboundmsg.OwnerApproval{
			Subject: "*Main* wants a new scheduled agent, *scrape*", When: fleetNow.Add(-3 * 24 * time.Hour),
			Facts:      []string{"Runs: daily 07:00, on its own", "Does: runs scrape on node laptop", "Program: approving the agent adds it to node laptop's allowlist, with exactly these arguments"},
			Allows:     []string{"run a program on node laptop: scrape --all"},
			Unexpected: "it was written into the brain's folder, not made in the dashboard.", Ref: "main/scrape",
		}, fleetNow)},
		{"approvalsWaitingAlert", outboundmsg.ApprovalsWaitingAlert([]outboundmsg.OwnerApproval{catalogueNodeApproval,
			{Subject: "*Main* has a new skill, *taxes*", When: fleetNow.Add(-90 * time.Minute), Unexpected: "it was written into the brain's folder, not made in the dashboard."}}, fleetNow)},
		{"approvalPickQuestion(approve)", outboundmsg.ApprovalPickQuestion(true, 3)},
		{"approvalPickQuestion(deny)", outboundmsg.ApprovalPickQuestion(false, 3)},
		{"approvalPickNotice(approve)", outboundmsg.ApprovalPickNotice(true, []string{"laptop (Linux) joins as a node: `n1a2b3`", "Main: new skill taxes: `main/skills/taxes/SKILL.md`"}, 0)},
		{"approvalPickNotice(deny)", outboundmsg.ApprovalPickNotice(false, []string{"laptop (Linux) joins as a node: `n1a2b3`"}, 2)},
		{"nothingPendingReply", outboundmsg.NothingPendingReply},
		{"approvalConfirmReply(unexpected)", outboundmsg.ApprovalConfirmReply(catalogueNodeApproval, "This is unexpected: its join code came neither from your !join nor from the dashboard.", 2)},
		{"approvalConfirmReply(expired)", outboundmsg.ApprovalConfirmReply(catalogueNodeApproval, outboundmsg.ApprovalExpiredWhy(fleetNow.Add(-50*time.Hour), fleetNow), 2)},
		{"approvalsConfirmReply", outboundmsg.ApprovalsConfirmReply([]outboundmsg.OwnerApproval{catalogueNodeApproval, {Subject: "*Main* has a new skill, *taxes*"}},
			[]string{"This is unexpected: its join code came neither from your !join nor from the dashboard.", outboundmsg.ApprovalUnseenWhy}, 2)},
		{"approvedReply", outboundmsg.ApprovedReply("the skill *taxes* of Main", "It applies as its file says from now on.")},
		{"deniedReply", outboundmsg.DeniedReply("the skill *taxes* of Main", "What you approved before stays (or, for a new one, it only reads); it waits on the brain's Skills page.")},
		{"approvalPollStale", outboundmsg.ApprovalPollStale},
		{"approvalPollGone", outboundmsg.ApprovalPollGone([]string{"mini (Linux) joins as a node", "Main: new skill taxes"})},
		{"approvalFailedReply", outboundmsg.ApprovalFailedReply("the new brain.json of *Main*", "the brain.json on disk changed again")},
		{"fleetWaitingNote(1)", strings.TrimSpace(outboundmsg.FleetWaitingNote(1))},
		{"fleetWaitingNote(3)", strings.TrimSpace(outboundmsg.FleetWaitingNote(3))},
		{"nodeRevokedReply", outboundmsg.NodeRevokedReply("laptop", "8233270f8a24")},
		{"nodeIsRevokedReply", outboundmsg.NodeIsRevokedReply("laptop", "8233270f8a24")},
		{"nodeUnknownReply", outboundmsg.NodeUnknownReply("nope")},
		{"nodeNothingWaitsReply", outboundmsg.NodeNothingWaitsReply("abc123")},
		{"nodeAmbiguousReply", outboundmsg.NodeAmbiguousReply("!revoke", "laptop", []outboundmsg.FleetNode{{ID: "8233270f8a24", Name: "Laptop", OS: "darwin"}, {ID: "b14c0de5e7a1", Name: "laptop", OS: "windows"}})},
		{"approvalYes", outboundmsg.ApprovalYes},
		{"approvalNo", outboundmsg.ApprovalNo},
		{"approvalDraft", outboundmsg.ApprovalDraft},
		{"approvalQuestion", outboundmsg.ApprovalQuestion("Submit the 2025 tax return through ELSTER")},
		{"askQuestion", outboundmsg.AskQuestion("Which account should the refund go to?")},
		{`approvalDraftReply("Send the email", "")`, outboundmsg.ApprovalDraftReply("Send the email", "")},
		{`approvalDraftReply("Send the email", draft)`, outboundmsg.ApprovalDraftReply("Send the email", "To: you@example.com\nSubject: Offer\n\nHello …")},
		{"lockdownReply(true, false)", outboundmsg.LockdownReply(true, false)},
		{"lockdownReply(true, true)", outboundmsg.LockdownReply(true, true)},
		{"lockdownReply(false, false)", outboundmsg.LockdownReply(false, false)},
		{"lockdownReply(false, true)", outboundmsg.LockdownReply(false, true)},
		{"lockdownOwnerOnly", outboundmsg.LockdownOwnerOnly},
		{"lockdownRefusedNotice", outboundmsg.LockdownRefusedNotice},
		{"rateLimitedNotice(20)", outboundmsg.RateLimitedNotice(20)},
		{"frideNewsMessage(started)", outboundmsg.FrideNewsMessage(outboundmsg.FrideNews{Thread: 3, Node: "laptop", Kind: "started", Text: "FrIDE inbox #1 · following · health\nStarting health.\n• create task health [api] on feat/health", Task: "health", Branch: "feat/health"})},
		{"frideNewsMessage(question)", outboundmsg.FrideNewsMessage(outboundmsg.FrideNews{Thread: 3, Node: "laptop", Kind: "question", Text: "FrIDE inbox #1 · needs your reply\nWhich empty state, the no-results one or the error one?"})},
		{"frideNewsMessage(update)", outboundmsg.FrideNewsMessage(outboundmsg.FrideNews{Thread: 3, Node: "laptop", Kind: "update", Text: "FrIDE inbox #1 · active · health\n→ health: the tests pass"})},
		{"frideNewsMessage(done)", outboundmsg.FrideNewsMessage(outboundmsg.FrideNews{Thread: 3, Node: "laptop", Kind: "done", Text: "FrIDE inbox #1 · done\n• Inbox #1 is done: health — task health is closed.", Task: "health", Branch: "feat/health", Rationale: "/Users/you/fride-workspaces/health/api/docs/rationale/health.md"})},
		{"frideNewsMessage(failed)", outboundmsg.FrideNewsMessage(outboundmsg.FrideNews{Thread: 3, Node: "laptop", Kind: "failed", Text: "FrIDE inbox #1 · failed\nthe message could not be read"})},
		{"frideNewsMessage(long)", outboundmsg.FrideNewsMessage(outboundmsg.FrideNews{Thread: 3, Node: "laptop", Kind: "question", Text: "Which way should I fix it?\nOption one\nOption two\nOption three\nOption four"})},
		{"frideFanoutMessage", outboundmsg.FrideFanoutMessage("mini", "update", []int64{9, 10, 15}, "FrIDE inbox #9 · update\nOrchestrator report: all three tasks are merged.")},
		{"frideQueuedNotice", outboundmsg.FrideQueuedNotice(3, "laptop: not enough memory for another stack")},
		{"frideNotLoggedInNotice(moved)", outboundmsg.FrideNotLoggedInNotice(3, "box", "laptop", nodeproto.FrideLoginFix)},
		{"frideNotLoggedInNotice(stays)", outboundmsg.FrideNotLoggedInNotice(3, "box", "", nodeproto.FrideLoginFix)},
		{"frideHostNoLoginNotice", outboundmsg.FrideHostNoLoginNotice("icebase", `no Claude token is stored for FrIDE's agents; in an administrator PowerShell there: fridolin node secrets set claude.oauth.token --state-dir "C:\Users\you\.fridolin-node", with the token claude setup-token prints`)},
		{"fridePlacedNotice", outboundmsg.FridePlacedNotice(3, "laptop")},
		{"frideInstanceQueuedNotice", outboundmsg.FrideInstanceQueuedNotice(4, "work", "no node has room for the FrIDE instance work (6144 MB, or 3072 MB at the least): mini has 1000 MB free beyond its reserve")},
		{"frideInstanceSmallerNotice", outboundmsg.FrideInstanceSmallerNotice("work", "mini", 3072, 6144)},
		{"frideMoveUsage", outboundmsg.FrideMoveUsage},
		{"frideMoveGroupNotice", outboundmsg.FrideMoveGroupNotice},
		{"frideMovingNotice", outboundmsg.FrideMovingNotice(3, "laptop")},
		{"frideMovedNotice", outboundmsg.FrideMovedNotice(3, "mini", "laptop", "health")},
		{"frideMoveFailedNotice", outboundmsg.FrideMoveFailedNotice(3, "laptop is offline")},
		{"nodeAgentNewsMessage(question)", outboundmsg.NodeAgentNewsMessage(outboundmsg.NodeAgentNews{Session: 2, Node: "laptop", Folder: "/home/you/Projects/foo", Kind: "question", Text: "Which test runner, jest or vitest?"})},
		{"nodeAgentNewsMessage(irreversible)", outboundmsg.NodeAgentNewsMessage(outboundmsg.NodeAgentNews{Session: 2, Node: "laptop", Folder: "/home/you/Projects/foo", Kind: "irreversible", Text: "git push origin main"})},
		{"nodeAgentNewsMessage(done)", outboundmsg.NodeAgentNewsMessage(outboundmsg.NodeAgentNews{Session: 2, Node: "laptop", Folder: "/home/you/Projects/foo", Kind: "done", Text: "The build works again: a missing import in main.go.", Changed: []string{"M main.go", "?? notes.txt"}})},
		{"nodeAgentNewsMessage(failed)", outboundmsg.NodeAgentNewsMessage(outboundmsg.NodeAgentNews{Session: 2, Node: "laptop", Folder: "/home/you/Projects/foo", Kind: "failed", Text: "claude ended: exit status 1"})},
		{"nodeSessionsStoppedNotice", outboundmsg.NodeSessionsStoppedNotice(2)},
		{"housekeepingSummary", outboundmsg.HousekeepingSummary(
			[]string{"run-orphan PID 4242 (claude) with 2 more, 0% CPU, 312 MB, up 26h0m0s: left running by a run of brain main that is over"},
			[]string{"zombie PID 4300 (sh): an ended child of the bridge that the bridge has not collected"})},
		{"housekeepingSummary(problems only)", outboundmsg.HousekeepingSummary(nil, []string{"restic-lock b2 1a2b3c4d: failed: restic unlock: the repository is locked"})},
		{"housekeepingStopResult", outboundmsg.HousekeepingStopResult("PID 812 (python3)", "")},
		{"housekeepingStopResult(failed)", outboundmsg.HousekeepingStopResult("PID 812 (python3)", "it is not the process it was any more")},
		{"scheduleFailedNotice", outboundmsg.ScheduleFailedNotice("morning-briefing", "main", "the run took too long and was stopped")},
		{"scheduleCommandResult", outboundmsg.ScheduleCommandResult("scrape", "laptop", 0, "fetched 12 transactions\nsummary sent\n")},
	}
}

// catalogueNodeApproval is a node that joined with a code from !join, as
// its alert tells it.
var catalogueNodeApproval = outboundmsg.OwnerApproval{
	Subject: "*laptop* (Windows) wants to join as a node", Label: "*laptop*", When: fleetNow.Add(-3 * time.Minute),
	Facts:  []string{"From: laptop (you@example.com) on your tailnet, 100.64.0.7", "Started: with a join code you asked for with !join"},
	Allows: nodeproto.Capabilities{Host: true, Fride: &nodeproto.FrideCap{URL: "http://127.0.0.1:17840"}}.Plain(),
	Ref:    "8233270f8a24", Option: "laptop (Windows) joins as a node",
}

// fleetNow and fleetNodes are the fleet !fleet lists in the catalogue:
// one node of each state.
var (
	fleetNow   = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	fleetNodes = []outboundmsg.FleetNode{
		{ID: "8233270f8a24", Name: "laptop", OS: "windows", Online: true, Device: "approved", Commands: "approved", Tags: []string{"gpu", "windows"}, Can: nodeproto.Capabilities{Host: true, Exec: &nodeproto.ExecCap{Allow: []nodeproto.ExecRule{{Name: "hostname"}, {Name: "whoami"}}}}.Plain()},
		{ID: "4f1e2d3c4b5a", Name: "mini", OS: "darwin", LastSeen: fleetNow.Add(-3 * time.Hour), Device: "approved", Commands: "pending", Tags: []string{"macos"}, Can: []string{"report its system details (OS, disk, memory)"}},
		{ID: "0a1b2c3d4e5f", Name: "pi", OS: "linux", LastSeen: fleetNow.Add(-90 * time.Second), Device: "pending", Commands: "pending"},
		{ID: "9d8c7b6a5f4e", Name: "pc", OS: "windows", Online: true, Device: "approved", Commands: "approved", Fride: true, Can: nodeproto.Capabilities{Host: true, Fride: &nodeproto.FrideCap{}}.Plain()},
		{ID: "5e6f7a8b9c0d", Name: "studio", OS: "darwin", Online: true, Device: "approved", Commands: "pending", FridePending: true, Can: []string{"report its system details (OS, disk, memory)"}},
		{ID: "ffeeddccbbaa", Name: "old", OS: "linux", Device: "revoked", Commands: "none"},
	}
)

// TestGoldenMessages pins every message the bridge sends into chats, so a
// change to one shows in review, and holds the catalogue to its rules:
// each message starts with an emoji and a space, and every name
// messages.go declares is in it.
func TestGoldenMessages(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	named := map[string]bool{}
	for _, m := range catalogue() {
		name, _, _ := strings.Cut(m.name, "(")
		named[name] = true
		out.WriteString(m.name + "\n")
		for _, line := range strings.Split(m.text, "\n") {
			if line != "" {
				line = "\t" + line
			}
			out.WriteString(line + "\n")
		}
		if r, size := utf8.DecodeRuneInString(m.text); !isEmojiStart(r) || !strings.HasPrefix(strings.TrimPrefix(m.text[size:], "️"), " ") {
			t.Errorf("%s does not start with an emoji and a space: %q", m.name, m.text)
		}
	}
	assertGolden(t, "messages.golden", []byte(out.String()))

	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoDir, "internal", "outbound", "messages.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range topLevelNames(f) {
		name = strings.ToLower(name[:1]) + name[1:]
		if !named[name] {
			t.Errorf("messages.go declares %s, which catalogue() does not list", name)
		}
	}
}

// topLevelNames are the names a file declares at the top level.
func topLevelNames(f *ast.File) []string {
	var names []string
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			names = append(names, d.Name.Name)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if v, ok := spec.(*ast.ValueSpec); ok {
					for _, n := range v.Names {
						names = append(names, n.Name)
					}
				}
			}
		}
	}
	return names
}

// isEmojiStart reports whether r is one of the symbols the bridge's
// messages start with: miscellaneous technical (⌛, ⏳), symbols and
// dingbats (⚠, ✅, ❌, ❓), and the emoji planes.
func isEmojiStart(r rune) bool {
	return r >= 0x2300 && r <= 0x23ff || r >= 0x2600 && r <= 0x27bf || r >= 0x2b00 && r <= 0x2bff || r >= 0x1f000 && r <= 0x1faff
}

// notChatText are the strings outside messages.go that start like a
// message of the bridge but go nowhere near a chat.
var notChatText = map[string]string{
	"⚠️ Skipped a line of %d bytes, longer than the %d MiB the bridge reads": "a line of a run's activity log",
	"❔ Event of a type the bridge does not read: %q":                         "a line of a run's activity log",
	"💬 ":         "a line of a run's activity log",
	"🔧 Tool: ":   "a line of a run's activity log",
	"❌ Error: ":  "a line of a run's activity log",
	"✅ Result: ": "a line of a run's activity log",
	"🔐":          "one of the OTP guard's markers, which it looks for in what a brain sends",
	"👍":          "a reaction that answers an approval poll, not a message",
	"👎":          "a reaction that answers an approval poll, not a message",
}

// TestChatMessagesLiveInTheCatalogue: no production Go file but messages.go has a
// string that starts with an emoji, the mark of a message the bridge
// sends into chats, so that a new one goes into the catalogue.
func TestChatMessagesLiveInTheCatalogue(t *testing.T) {
	t.Parallel()
	var files []string
	err := filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && path == filepath.Join(repoDir, "internal", "testutil"):
			// Fake CLI diagnostics moved out of _test.go so leaf-package tests
			// can reuse them. Archtest forbids this package in production.
			return filepath.SkipDir
		case d.IsDir() && path != repoDir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "node_modules" || d.Name() == "vendor"):
			return filepath.SkipDir
		case !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go"):
			files = append(files, path)
		}
		return nil
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files in %s: %v", repoDir, err)
	}
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, path := range files {
		if path == filepath.Join(repoDir, "internal", "outbound", "messages.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		if ast.IsGenerated(f) {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			if r, _ := utf8.DecodeRuneInString(s); !isEmojiStart(r) {
				return true
			}
			if _, ok := notChatText[s]; ok {
				seen[s] = true
				return true
			}
			t.Errorf("%s: %q starts like a chat message; put it in messages.go", fset.Position(lit.Pos()), s)
			return true
		})
	}
	for _, s := range slices.Sorted(maps.Keys(notChatText)) {
		if !seen[s] {
			t.Errorf("notChatText has %q, which no file has any more", s)
		}
	}
}
