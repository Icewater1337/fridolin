package app

import (
	"net/http"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

func (b *App) buildAPI(mux *http.ServeMux) {
	(&httpapi.BuildChatAPI{
		Brain: func(id string) *brain.Brain {
			br := b.brainMgr.Get(id)
			if br == nil || !b.site.IsBuilder(id) {
				return nil
			}
			return br
		},
		View: b.buildChatView,
		AppendUser: func(id, text string) (int, error) {
			ch := b.site.BuildChat(id)
			return ch.Thread, b.site.AppendBuildMessage(id, "user", text)
		},
		Submit: func(br *brain.Brain, thread int, text string) error {
			m := brain.Inbound{Inbound: chat.Inbound{JID: buildChatJID(br.ID(), thread), Sender: "dashboard", Brain: br.ID(), Text: text, Source: "site-build", At: time.Now()}}
			if _, _, _, err := b.brainMgr.Submit(m); err != nil {
				_ = b.site.AppendBuildMessage(br.ID(), "assistant", outboundmsg.BuildChatError(err))
				return err
			}
			return nil
		},
		NewThread: func(id string) error { return b.site.NewBuildThread(id) },
		StopCurrent: func(br *brain.Brain) bool {
			ch := b.site.BuildChat(br.ID())
			rt := br.Snapshot()
			if rt.Running == nil || rt.Running.Chat != buildChatJID(br.ID(), ch.Thread) {
				return false
			}
			br.StopCurrent(false)
			return true
		},
	}).Register(mux)
}

func (b *App) buildChatView(br *brain.Brain) httpapi.BuildChatResponse {
	ch := b.site.BuildChat(br.ID())
	jid := buildChatJID(br.ID(), ch.Thread)
	rt := br.Snapshot()
	running := rt.Running != nil && rt.Running.Chat == jid
	queued := 0
	for _, q := range rt.Queue {
		if q.Chat == jid {
			queued++
		}
	}
	resp := httpapi.BuildChatResponse{Messages: ch.Messages, Thread: ch.Thread, Running: running, Queued: queued, Activity: []string{}}
	if running {
		resp.Activity = currentRunLog(br.StreamLogPath(), rt.Running.PID)
	}
	if urls := b.site.PreviewURLs(br.ID()); urls != nil {
		resp.Preview = &urls
	}
	for _, p := range b.site.Settings().Proposals {
		if p.Brain == br.ID() {
			proposal := p
			resp.Proposal = &proposal
		}
	}
	return resp
}
