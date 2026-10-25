package app

import (
	"context"
	"slices"
	"time"
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
// each with its news in full, which the chat gets only a few lines of.
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
		v := frideThreadView{Thread: t.ID, State: t.State, Chat: t.Chat, Brain: t.Brain, Message: t.Message, Task: t.TaskID, Branch: t.Branch,
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
	return map[string]any{"threads": out}, nil
}
