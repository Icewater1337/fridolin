package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/connections"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// Fridolin's connections to OpenAI and ElevenLabs (docs/CONNECTIONS.md):
// internal/connections with the bridge's settings, data dir and audit
// log; the dashboard's routes; what the live site's modules declare, for
// the site bridge (site_bridge.go); the brains' tools are in
// mcp_connections.go.

// connectionCheckTimeout bounds the dashboard's Check.
const connectionCheckTimeout = 15 * time.Second

// newConnections is the bridge's connections service. Tests point its
// calls at fake providers (testHooks.connections).
func (b *App) newConnections() *connections.Service {
	o := connections.Options{
		Dir:      b.cfgs.Boot().Layout().Connections(),
		Settings: func() connections.Settings { return connectionSettingsOf(b.cfgs.Get()) },
		Audit:    b.auditConnectionCall,
	}
	if e := b.hooks.connections; e != nil {
		o.Endpoints = *e
	}
	return connections.New(o)
}

// connectionSettingsOf projects config.json's connections section.
func connectionSettingsOf(cfg *config.Settings) connections.Settings {
	c := cfg.Connections
	return connections.Settings{
		OpenAI:     connections.Policy{Brains: c.OpenAI.Brains, BrainMonthlyUSD: c.OpenAI.BrainMonthlyUSD},
		ElevenLabs: connections.Policy{Brains: c.ElevenLabs.Brains, BrainMonthlyUSD: c.ElevenLabs.BrainMonthlyUSD, Voice: c.ElevenLabs.Voice},
	}
}

// auditConnectionCall writes a call's line: who asked, for what, and how
// it ended; never what was sent or answered.
func (b *App) auditConnectionCall(e connections.Event) {
	entry := audit.Entry{Kind: audit.ConnectionCall, By: e.Caller.ID, Detail: strings.TrimSpace(e.Connection + " " + e.Op + " " + e.Model), Outcome: e.Outcome}
	if id, ok := strings.CutPrefix(e.Caller.ID, "brain:"); ok {
		entry.Brain, entry.Run = id, e.Caller.Run
	}
	b.audit.Append(entry)
}

// declarationsOf are what a module declares of the connections.
func declarationsOf(m sandbox.SiteModule) map[string]connections.Declaration {
	out := map[string]connections.Declaration{}
	for id, c := range m.Connections {
		out[id] = connections.Declaration{Use: c.Use, Models: c.Models, MonthlyUSD: c.MonthlyUSD}
	}
	return out
}

// liveDeclarations is what module id of the live commit declares: the
// site bridge checks the module's calls against it.
func (b *App) liveDeclarations(id string) (map[string]connections.Declaration, error) {
	if b.site == nil {
		return nil, errors.New("the site is not set up")
	}
	m, err := b.site.LiveModule(id)
	if err != nil {
		return nil, err
	}
	return declarationsOf(m), nil
}

// connectionModules are the live site's modules with what they declare,
// for the dashboard; none while the site is off.
func (b *App) connectionModules() []connections.Module {
	if b.site == nil {
		return nil
	}
	var out []connections.Module
	for _, m := range b.site.LiveModules() {
		out = append(out, connections.Module{ID: m.ID, Connections: declarationsOf(m)})
	}
	return out
}

func (b *App) newConnectionsAPI() *httpapi.ConnectionsAPI {
	return &httpapi.ConnectionsAPI{
		View:      func(context.Context) (any, error) { return b.conns.View(b.connectionModules()), nil },
		SetKey:    b.setConnectionKey,
		RemoveKey: b.removeConnectionKey,
		SetBrains: b.setConnectionBrains,
		Check:     b.checkConnection,
	}
}

func badRequest(format string, a ...any) error {
	return &connections.Error{Status: http.StatusBadRequest, Message: fmt.Sprintf(format, a...)}
}

func noConnection(id string) error {
	return &connections.Error{Status: http.StatusNotFound, Message: fmt.Sprintf("no connection %q", id)}
}

// setConnectionKey answers PUT /api/connections/{id}: the key, and
// OpenAI's organization and project. The audit says what changed, never
// the key.
func (b *App) setConnectionKey(_ context.Context, id string, req httpapi.ConnectionKeyRequest) (any, error) {
	had := b.conns.Configured(id)
	if err := b.conns.SetKey(id, connections.KeyUpdate{APIKey: req.APIKey, Organization: req.Organization, Project: req.Project}); err != nil {
		return nil, err
	}
	var what []string
	switch {
	case strings.TrimSpace(req.APIKey) == "":
	case had:
		what = append(what, "key replaced")
	default:
		what = append(what, "key set")
	}
	var fields []string
	if req.Organization != nil {
		fields = append(fields, "organization")
	}
	if req.Project != nil {
		fields = append(fields, "project")
	}
	if len(fields) > 0 {
		what = append(what, strings.Join(fields, " and ")+" saved")
	}
	if len(what) == 0 {
		what = append(what, "nothing changed")
	}
	b.audit.Append(audit.Entry{Kind: audit.ConnectionKey, By: "dashboard", Detail: id + ": " + strings.Join(what, "; "), Outcome: "ok"})
	return b.conns.Entry(id)
}

// removeConnectionKey answers DELETE /api/connections/{id}.
func (b *App) removeConnectionKey(_ context.Context, id string) error {
	if err := b.conns.RemoveKey(id); err != nil {
		return err
	}
	b.audit.Append(audit.Entry{Kind: audit.ConnectionKey, By: "dashboard", Detail: id + ": key removed", Outcome: "ok"})
	return nil
}

// setConnectionBrains answers PUT /api/connections/{id}/brains: it writes
// connections.<id> in config.json as every config write does. A brain
// that is not running may be named only if it is listed already, so that
// one that went away does not keep the rest from being saved.
func (b *App) setConnectionBrains(_ context.Context, id string, req httpapi.ConnectionBrainsRequest) (any, error) {
	if !connections.Known(id) {
		return nil, noConnection(id)
	}
	if req.Voice != nil && id != connections.ElevenLabs {
		return nil, badRequest("%s has no voice", connections.Title(id))
	}
	if req.Brains != nil {
		listed := connectionSettingsOf(b.cfgs.Get()).Of(id).Brains
		for _, brain := range *req.Brains {
			if !slices.Contains(listed, brain) && b.brainMgr.Get(brain) == nil {
				return nil, badRequest("there is no brain %q", brain)
			}
		}
	}
	_, err := b.cfgs.Update("", func(c *config.File) error {
		brains, usd := &c.Connections.OpenAI.Brains, &c.Connections.OpenAI.BrainMonthlyUSD
		if id == connections.ElevenLabs {
			brains, usd = &c.Connections.ElevenLabs.Brains, &c.Connections.ElevenLabs.BrainMonthlyUSD
			if req.Voice != nil {
				c.Connections.ElevenLabs.Voice = strings.TrimSpace(*req.Voice)
			}
		}
		if req.Brains != nil {
			var list []string
			for _, brain := range *req.Brains {
				if !slices.Contains(list, brain) {
					list = append(list, brain)
				}
			}
			*brains = list
		}
		if req.BrainMonthlyUSD != nil {
			*usd = *req.BrainMonthlyUSD
		}
		if err := c.Connections.Validate(); err != nil {
			return badRequest("%s", err.Error())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return b.conns.Entry(id)
}

// connectionCheck is what POST /api/connections/{id}/check answers.
type connectionCheck struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// checkConnection answers POST /api/connections/{id}/check: whether the
// provider takes the key.
func (b *App) checkConnection(ctx context.Context, id string) (any, error) {
	if !connections.Known(id) {
		return nil, noConnection(id)
	}
	detail, err := b.conns.Check(ctx, id, connectionCheckTimeout)
	if err != nil {
		return connectionCheck{Error: err.Error()}, nil
	}
	return connectionCheck{OK: true, Detail: detail}, nil
}
