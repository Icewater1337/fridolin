package app

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// plaudPhoneUpload applies the phone app's checks where the Plaud Sync
// app uploads through the site (plaud.phone_upload): the dashboard builds
// it then.
func plaudPhoneUpload(env *doctor.Env) bool {
	return env.Config != nil && env.Config.Plaud.PhoneUpload
}

// plaudDoctorCheck is the check of a way to build the Plaud phone app.
var plaudDoctorCheck = doctor.Check{
	ID: "plaud-app-build", Title: "a way to build the Plaud phone app", Applies: plaudPhoneUpload, Run: checkPlaudAppBuild,
}

// checkPlaudAppBuild: the Plaud card's Build phone app needs a JDK 17+
// and the Android SDK where the bridge runs, or a node that builds
// Android apps (android-build) that is approved and heard from. The app
// already on the phone keeps working without either; a new phone, or one
// that was unpaired, needs a build.
func checkPlaudAppBuild(ctx context.Context, env *doctor.Env) doctor.Result {
	local := errors.New("doctor cannot look for a JDK here")
	if env.AndroidToolchain != nil {
		local = env.AndroidToolchain()
	}
	if local == nil {
		return doctor.Result{Status: doctor.OK, Detail: "this computer has a JDK 17+ and the Android SDK: the dashboard builds the app here"}
	}
	var ready, waiting []string
	if env.Config.Nodes.Enabled {
		list, _ := readNodes(ctx, env)
		now := env.Time()
		for _, n := range list {
			if n.Device != nodeproto.DeviceApproved {
				continue
			}
			switch {
			case n.Approved.AndroidBuild == nil && n.Declared.AndroidBuild != nil:
				waiting = append(waiting, n.Name+" ("+n.ID+", offers android-build, not approved)")
			case n.Approved.AndroidBuild == nil:
			case n.LastSeen == nil || now.Sub(*n.LastSeen) > nodeStaleAfter:
				waiting = append(waiting, n.Name+" ("+n.ID+", not heard from)")
			case n.Declared.AndroidBuild == nil:
				waiting = append(waiting, n.Name+" ("+n.ID+", finds no JDK or Android SDK)")
			default:
				ready = append(ready, n.Name)
			}
		}
	}
	slices.Sort(ready)
	slices.Sort(waiting)
	if len(ready) > 0 {
		return doctor.Result{Status: doctor.OK, Detail: "the dashboard builds the app on a node: " + strings.Join(ready, ", ")}
	}
	why, _, _ := strings.Cut(local.Error(), ":")
	detail := "nothing can build the phone app: on this computer, " + why
	if len(waiting) > 0 {
		detail += "; nodes that could: " + strings.Join(waiting, "; ")
	} else if !env.Config.Nodes.Enabled {
		detail += "; nodes are off (nodes.enabled)"
	}
	return doctor.Result{Status: doctor.Warn, Detail: detail,
		Fix: "the app on the phone keeps working; to build it again, join a machine with a JDK 17+ and the Android SDK as a node and approve its commands (fridolin nodes approve <id> --commands; docs/NODES.md, android-build), or install them here (docs/PLAUD.md)"}
}
