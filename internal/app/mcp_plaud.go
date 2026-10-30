package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
	"github.com/Icewater1337/fridolin/internal/plaud"
	"github.com/Icewater1337/fridolin/internal/skill"
)

// The core brain updates the Plaud Sync phone app (docs/PLAUD.md,
// "Updates"): plaud_app_update builds it as the Plaud card's button does,
// and the paired phones install the build at their next check-in;
// plaud_app_status says how that went. A core run has them while the
// phone app uploads through the site (plaud.phone_upload); a sub-brain
// and a delegated run never do.

func (b *App) plaudAppTools(c mcpCaller) bool {
	return c.Core() && b.plaud != nil && RuntimeConfig(b.cfgs).Plaud.PhoneUpload
}

// registerPlaudTools registers them: plaud_app_update builds on this
// machine or a node and changes what the phones run, so it is write,
// refused in a lockdown and to a read budget; plaud_app_status reads.
func (b *App) registerPlaudTools(r *tools.Set[mcpCaller]) {
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "plaud_app_update",
		Available: b.plaudAppTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "Update the Plaud Sync app on the owner's phone: build it again from the source Fridolin runs (on this machine or a node that builds Android apps), which takes a few minutes. Each paired phone then fetches the build at its next check-in, usually within 10 minutes, and installs it; the first update may wait for a tap on the phone. Only when the owner asks for it. Check how it went with plaud_app_status.",
				InputSchema: tools.Schema(nil),
			}
		},
		Call: r.Guarded(skill.RiskWrite, b.callPlaudAppUpdate),
	})
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "plaud_app_status",
		Available: b.plaudAppTools,
		Describe: func(mcpCaller) mcp.Tool {
			return mcp.Tool{
				Description: "How the Plaud Sync phone app stands: the latest build and one that runs or failed, and for each paired phone the version it runs and how its last update went.",
				InputSchema: tools.Schema(nil),
			}
		},
		Call: b.callPlaudAppStatus,
	})
}

func (b *App) callPlaudAppUpdate(_ context.Context, _ mcpCaller, _ json.RawMessage) (*mcp.CallResult, error) {
	err := b.plaud.BuildApp()
	if errors.Is(err, plaud.ErrAppBuilding) {
		return mcp.TextResult("A build of the app runs already; the phones update to it once it is done.\n\n" + plaudAppSummary(b.plaud.Status().App, time.Now())), nil
	}
	if err != nil {
		return mcp.ErrorResult("the app cannot be built: " + err.Error()), nil
	}
	return mcp.TextResult("Building the Plaud Sync app. It takes a few minutes; then each paired phone installs it at its next check-in.\n\n" + plaudAppSummary(b.plaud.Status().App, time.Now())), nil
}

func (b *App) callPlaudAppStatus(_ context.Context, _ mcpCaller, _ json.RawMessage) (*mcp.CallResult, error) {
	return mcp.TextResult(plaudAppSummary(b.plaud.Status().App, time.Now())), nil
}

// plaudAppSummary is the phone app's part of the Plaud card, in words.
func plaudAppSummary(s *plaud.AppStatus, now time.Time) string {
	if s == nil {
		return "The phone app does not upload through the site (plaud.phone_upload is off)."
	}
	ago := func(t time.Time) string { return now.Sub(t).Round(time.Minute).String() + " ago" }
	var lines []string
	if r := s.Release; r != nil {
		where := ""
		if r.Node != "" {
			where = " on node " + r.Node
		}
		lines = append(lines, fmt.Sprintf("Latest build: %s (%d), built %s%s.", r.VersionName, r.VersionCode, ago(r.Built), where))
	} else {
		lines = append(lines, "No build yet.")
	}
	if j := s.Build; j != nil {
		switch j.State {
		case "running":
			lines = append(lines, fmt.Sprintf("Building %d now: %s.", j.VersionCode, j.Stage))
		case "error":
			lines = append(lines, fmt.Sprintf("The last build (%d) failed: %s", j.VersionCode, j.Error))
		}
	}
	if s.NoBuilder != "" {
		lines = append(lines, "Nothing can build the app now: "+s.NoBuilder)
	}
	if len(s.Devices) == 0 {
		lines = append(lines, "No phone is paired.")
	}
	for _, d := range s.Devices {
		lines = append(lines, "Phone "+d.ID+": "+phoneAppLine(d, s.Release, ago))
	}
	return strings.Join(lines, "\n")
}

// phoneAppLine is what the card says of a phone's app.
func phoneAppLine(d plaud.PhoneRow, rel *plaud.Release, ago func(time.Time) string) string {
	a := d.App
	if a == nil {
		return "its app has not asked for updates yet (a build from before updates, or it has not checked in since)."
	}
	out := fmt.Sprintf("runs %s (%d), asked %s", a.VersionName, a.VersionCode, ago(a.Asked))
	switch {
	case d.OtherKey:
		out += "; the latest build is signed with another key, so Android will not install it over this app"
	case rel != nil && a.VersionCode >= rel.VersionCode:
		out += "; up to date"
	case rel != nil:
		out += fmt.Sprintf("; updates to %d at its next check-in", rel.VersionCode)
	}
	if u := a.Update; u != nil && (rel == nil || u.VersionCode <= rel.VersionCode) {
		out += fmt.Sprintf(". Last update, to %d: %s", u.VersionCode, updateStateText(u))
	}
	return out + "."
}

// updateStateText says what a phone's update state means.
func updateStateText(u *plaud.AppUpdate) string {
	switch u.State {
	case "waiting":
		return "downloaded, installs once the app is not syncing"
	case "installing":
		return "handed to Android to install"
	case "confirm":
		return "waits for a tap on the phone (the Plaud Sync notification)"
	case "not_allowed":
		return "waits until Plaud Sync may install apps (Settings → Apps → Special app access → Install unknown apps)"
	case "installed":
		return "installed"
	case "failed":
		return "failed: " + u.Error
	}
	return u.State
}
