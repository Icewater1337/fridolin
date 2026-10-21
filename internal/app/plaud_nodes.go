package app

import (
	"cmp"
	"context"
	"errors"
	"io"
	"slices"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/plaud"
)

// plaudFleet is how the Plaud card builds the phone app on a node that
// builds Android apps (android-build), where the bridge has no JDK and
// Android SDK itself: the fridolin-core image has neither (docs/PLAUD.md).
type plaudFleet struct {
	builders func() []plaud.AppBuilder
	build    func(ctx context.Context, id string, source []byte, props map[string]string, apk io.Writer) (string, error)
}

// appBuilderTags are the tags that make a node the first choice to build
// the app, before any other that may.
var appBuilderTags = []string{"android", "android-build"}

// appBuilders is the nodes that may build the app now, best first: online
// with android-build approved and offered; those tagged android or
// android-build first, then the one with the most free memory.
func appBuilders(views []nodes.View) []plaud.AppBuilder {
	views = slices.DeleteFunc(slices.Clone(views), func(v nodes.View) bool { return !v.Online || v.Effective.AndroidBuild == nil })
	tagged := func(v nodes.View) bool {
		return slices.ContainsFunc(v.Tags, func(t string) bool { return slices.Contains(appBuilderTags, t) })
	}
	free := func(v nodes.View) uint64 {
		if v.Resources == nil {
			return 0
		}
		return v.Resources.MemAvailable
	}
	slices.SortStableFunc(views, func(a, b nodes.View) int {
		if ta, tb := tagged(a), tagged(b); ta != tb {
			if ta {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(free(b), free(a)); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	out := make([]plaud.AppBuilder, 0, len(views))
	for _, v := range views {
		out = append(out, plaud.AppBuilder{ID: v.ID, Name: v.Name})
	}
	return out
}

// newPlaudFleet builds the app on the fleet's nodes. In a lockdown it
// refuses, as the node calls that act on a node are refused then.
func newPlaudFleet(fleet *nodes.Service, locked func() bool) plaudFleet {
	return plaudFleet{
		builders: func() []plaud.AppBuilder {
			views, _ := fleet.Views()
			return appBuilders(views)
		},
		build: func(ctx context.Context, id string, source []byte, props map[string]string, apk io.Writer) (string, error) {
			if locked != nil && locked() {
				return "", errors.New("the bridge is in lockdown: unlock it in the dashboard or with !unlock, then build again")
			}
			res, err := fleet.AndroidBuild(ctx, id, nodeproto.AndroidBuildParams{Source: source, Properties: props}, apk)
			if err != nil {
				return res.Log, err
			}
			if res.Error != "" {
				return res.Log, errors.New(res.Error)
			}
			return "", nil
		},
	}
}
