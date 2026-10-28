package app

import (
	"context"
	"slices"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
)

// frideNewsView is one piece of a thread's news on the dashboard's FrIDE
// page, whole.
type frideNewsView struct {
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// frideThreadView is one thread of GET /api/fride.
type frideThreadView struct {
	Thread      int64           `json:"thread"`
	State       string          `json:"state"`
	Chat        string          `json:"chat"`
	Brain       string          `json:"brain"`
	Message     string          `json:"message"`
	Node        string          `json:"node,omitempty"`
	Instance    string          `json:"instance,omitempty"`
	Task        string          `json:"task,omitempty"`
	Branch      string          `json:"branch,omitempty"`
	FrideStatus string          `json:"fride_status,omitempty"`
	Archive     string          `json:"archive,omitempty"`
	Updated     time.Time       `json:"updated"`
	News        []frideNewsView `json:"news"`
}

// maxFrideThreadsShown caps the threads GET /api/fride lists.
const maxFrideThreadsShown = 100

// frideView answers GET /api/fride: the FrIDE threads, the newest first,
// each with its news in full, which the chat gets only a few lines of;
// and the FrIDE instances, by name, with where each runs.
func (b *App) frideView(ctx context.Context) (map[string]any, error) {
	all, err := b.store.FrideThreads(ctx, "")
	if err != nil {
		return nil, err
	}
	slices.Reverse(all)
	if len(all) > maxFrideThreadsShown {
		all = all[:maxFrideThreadsShown]
	}
	out := []frideThreadView{}
	for _, t := range all {
		v := frideThreadView{Thread: t.ID, State: t.State, Chat: t.Chat, Brain: t.Brain, Message: t.Message, Instance: t.Instance, Task: t.TaskID, Branch: t.Branch,
			FrideStatus: t.FrideStatus, Archive: t.Archive, Updated: t.UpdatedAt, News: []frideNewsView{}}
		if t.Node != "" {
			v.Node = b.fride.nodeName(t.Node)
		}
		news, err := b.store.FrideNews(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		for _, n := range news {
			v.News = append(v.News, frideNewsView{Kind: n.Kind, Text: n.Text, At: n.At})
		}
		out = append(out, v)
	}
	open, err := b.fride.inst.openThreads(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"threads": out, "instances": b.fride.inst.instanceViews(b.fride.inst.views(), open)}, nil
}

// stopFrideInstance answers POST /api/fride/instances/{name}/stop: one
// with open threads only with force.
func (b *App) stopFrideInstance(ctx context.Context, name string, force bool) error {
	why := "stopped on the dashboard"
	if force {
		why += ", with its open threads"
	}
	_, err := b.fride.inst.stop(ctx, name, "dashboard", why, force)
	return err
}

// startFrideInstance answers POST /api/fride/instances/{name}/start with
// the instance's view: placed as fride_spawn places it. Not in a lockdown.
func (b *App) startFrideInstance(ctx context.Context, name, size string) (any, error) {
	if b.locked() {
		return nil, apperr.New(apperr.Forbidden, "the bridge is in lockdown, which starts no FrIDE instance; unlock it in the dashboard or with !unlock")
	}
	if _, ok := b.fride.inst.configured()[name]; !ok {
		return nil, b.fride.inst.errUnknown(name)
	}
	b.fride.start()
	if _, err := b.fride.inst.start(ctx, name, size, "dashboard", "started on the dashboard"); err != nil {
		return nil, err
	}
	b.fride.kickQueue()
	open, err := b.fride.inst.openThreads(ctx)
	if err != nil {
		return nil, err
	}
	return b.fride.inst.instanceView(name, b.fride.inst.views(), open), nil
}
