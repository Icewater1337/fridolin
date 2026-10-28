package nodeagent

import (
	"context"
	"sync"
	"time"
)

// sbxSettingsEvery is how often the agent makes sbx's global settings
// hold again (at most; it looks at each check of FrIDE that is due).
const sbxSettingsEvery = 5 * time.Minute

// sbxSettingsTimeout bounds one look at sbx's settings.
const sbxSettingsTimeout = 30 * time.Second

// sbxSettingsState is when the agent last looked at sbx's settings, and
// what it last logged of a failure.
type sbxSettingsState struct {
	mu      sync.Mutex
	checked time.Time
	lastErr string
}

// sbxSettingsCheck makes fridolin's global settings hold again, when it
// is due, on a node that runs FrIDE instances (fride_host): it sets a
// setting that drifted and initializes a missing network policy. Another
// policy it leaves alone, since replacing it stops every sandbox.
func (a *Agent) sbxSettingsCheck(ctx context.Context) {
	d := a.sbx()
	if d == nil || a.Dir == "" || !sbxOS() {
		return
	}
	s := &a.sbxSettings
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.checked.IsZero() && time.Since(s.checked) < sbxSettingsEvery {
		return
	}
	s.checked = time.Now()
	if h, err := LoadFrideHost(a.Dir); err != nil || h == nil || a.Config.Box != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, sbxSettingsTimeout)
	defer cancel()
	ok, why := a.sandboxSettings(ctx, d)
	if !ok && why != s.lastErr {
		a.logf("node: sbx's global settings are not fridolin's: %s", why)
	}
	if ok {
		why = ""
	}
	s.lastErr = why
}
