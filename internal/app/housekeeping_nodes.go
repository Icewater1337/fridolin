package app

import (
	"context"
	"fmt"
	"time"

	"github.com/Icewater1337/fridolin/internal/housekeeping"
)

// nodeSessionFindings are the agent sessions and terminals that run on
// nodes and that the core has no record of (#159): a restart of the core
// forgot them, so nobody hears their news or closes them. Unless dry, it
// stops each one on its node (workRuntime.stopOrphan, which audits it).
// One younger than minAge (at least a minute) is left, since the core
// records a session only once its node answered the start.
func (h *housekeepingService) nodeSessionFindings(ctx context.Context, dry bool, minAge time.Duration) ([]housekeeping.Finding, []string) {
	if h.b.work == nil || !h.b.nodesEnabled {
		return nil, nil
	}
	minAge = max(minAge, h.b.waits.nodeSessionMinAge)
	list, errs := h.b.work.orphans(ctx)
	var out []housekeeping.Finding
	for _, o := range list {
		age := time.Since(o.Started)
		f := housekeeping.Finding{Kind: housekeeping.KindNodeSession, Action: housekeeping.ActionStop, Owner: "node " + o.NodeName,
			Target: fmt.Sprintf("%s %s on %s in %s", o.Kind, o.ID, o.NodeName, o.Folder), AgeSeconds: int64(age / time.Second),
			Reason: "the core has no record of it (a restart forgot it), so nobody follows or closes it"}
		switch {
		case age < minAge:
			f.Outcome = "left (too young)"
		case dry:
			f.Outcome = housekeeping.OutcomeWouldStop
		default:
			f.Outcome = housekeeping.OutcomeStopped
			if err := h.b.work.stopOrphan(ctx, o); err != nil {
				f.Outcome, f.Problem = housekeeping.OutcomeFailed+": "+err.Error(), true
			}
		}
		out = append(out, f)
	}
	return out, errs
}
