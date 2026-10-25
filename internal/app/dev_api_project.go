package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"go.mau.fi/whatsmeow/types"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

func (b *App) newDevAPI() *httpapi.DevAPI {
	return &httpapi.DevAPI{
		HasBrain: func(id string) bool { return b.brainMgr.Get(id) != nil },
		Submit:   b.submitDevInbound,
		Messages: func() []httpapi.DevMessage {
			rows := b.dev.Outbox()
			if rows == nil {
				return nil
			}
			out := make([]httpapi.DevMessage, len(rows))
			for i, row := range rows {
				out[i] = httpapi.DevMessage(row)
			}
			return out
		},
		Vote: b.devVote,
	}
}

// devVote records a vote in a poll the dev chat shows, as a vote from
// WhatsApp is recorded (whatsapp.ProcessVote): kept with the poll, then
// handed on as recordVote takes it.
func (b *App) devVote(req httpapi.DevVoteRequest) error {
	ctx := context.Background()
	poll, err := b.store.SentPoll(ctx, req.Poll)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.New(apperr.NotFound, "no poll "+req.Poll)
	}
	if err != nil {
		return err
	}
	selected := []string{}
	for _, o := range req.Option {
		if !slices.Contains(poll.Options, o) {
			return apperr.New(apperr.Invalid, fmt.Sprintf("%q is not an option of that poll", o))
		}
		selected = append(selected, o)
	}
	voter := types.NewJID(chat.NormalizeJID(req.Voter), types.DefaultUserServer)
	if err := b.store.SavePollVote(req.Poll, voter.String(), selected); err != nil {
		return err
	}
	b.recordVote(pollVote{Poll: poll, Voter: voter, Selected: selected})
	return nil
}

// newPolicyAPI serves the policy's dashboard views.
func (b *App) newPolicyAPI() *httpapi.PolicyAPI {
	return &httpapi.PolicyAPI{
		Status:  b.policyStatus,
		Threads: func(ctx context.Context, limit int) (any, error) { return b.threads(ctx, limit) },
		Audit: func(r *http.Request) (any, error) {
			return b.auditLines(r.Context(), auditFilter(r))
		},
		Approvals: b.approvalsView,
		Decide:    b.dashboardDecide,
		Lockdown:  b.dashboardLockdown,
		Skills:    b.skillsView,
		ApproveSkill: func(ctx context.Context, id string, req httpapi.SkillApproveRequest) (any, error) {
			return b.approveSkill(ctx, id, req)
		},
		SetSkillPolicy: func(ctx context.Context, id string, req httpapi.SkillPolicyRequest) (any, error) {
			return b.setSkillPolicy(ctx, id, req)
		},
		Fride: func(ctx context.Context) (any, error) { return b.frideView(ctx) },
	}
}
