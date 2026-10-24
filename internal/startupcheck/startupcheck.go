// Package startupcheck watches the required files of startup_checks
// (docs/CONFIGURATION.md): it reports the ones missing now, logs each
// change of what is missing and, with notify_whatsapp, alerts the owner
// once per change. It takes its own settings and the delivery as a
// function; internal/app projects the configuration for it.
package startupcheck

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/outbound"
)

// Settings are startup_checks as the check uses them, projected from the
// configuration on every look.
type Settings struct {
	// RequiredFiles must exist; ~ is already expanded.
	RequiredFiles []string
	// Hint follows the list of missing files, e.g. how to restore them.
	Hint string
	// Notify sends the alert to Chat (notify_whatsapp).
	Notify bool
	// Chat receives the alert: notify_jid, else the owner's chat; "" when
	// there is neither.
	Chat string
	// MaxChunkSize is the chunk size the alert is delivered in.
	MaxChunkSize int
}

// Options wires the current settings and the delivery of the alert.
// Constructing it starts no watcher; Watch runs until cancellation.
type Options struct {
	Settings func() Settings
	Send     func(ctx context.Context, chat, text string, maxChunkSize int) error
}

// Warnings reports the required files missing at the time of the call.
func Warnings(s Settings) []string {
	if msg := missingText(s, missing(s)); msg != "" {
		return []string{msg}
	}
	return nil
}

// missing returns the required files that do not exist now.
func missing(s Settings) []string {
	var missing []string
	for _, f := range s.RequiredFiles {
		if !fsx.Exists(f) {
			missing = append(missing, f)
		}
	}
	return missing
}

// missingText says which required files are missing, with the configured
// hint; "" when none is.
func missingText(s Settings, missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	msg := fmt.Sprintf("%d required file(s) are missing: %s", len(missing), strings.Join(missing, ", "))
	if s.Hint != "" {
		msg += " — " + s.Hint
	}
	return msg
}

// Watch looks at the required files at once and then every interval
// until ctx ends. It logs each change, and with notify_whatsapp sends the
// alert when the files missing are others than it last told about: once
// per change, not on every look, and again after they were all back and
// some went missing once more.
func Watch(ctx context.Context, every time.Duration, options Options) {
	w := watcher{options: options}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		w.look(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// watcher remembers what one Watch saw and told between its looks.
type watcher struct {
	options    Options
	seen, told []string
}

// look is one look of Watch.
func (w *watcher) look(ctx context.Context) {
	s := w.options.Settings()
	missing := missing(s)
	msg := missingText(s, missing)
	switch {
	case !slices.Equal(missing, w.seen) && msg != "":
		log.Printf("STARTUP CHECK FAILED: %s", msg)
	case !slices.Equal(missing, w.seen):
		log.Printf("startup check: the required files are all there now")
		w.told = nil
	}
	w.seen = missing
	if msg != "" && s.Notify && !slices.Equal(missing, w.told) && s.Chat != "" {
		if err := w.options.Send(ctx, s.Chat, outbound.StartupCheckAlert(msg), s.MaxChunkSize); err != nil {
			log.Printf("startup check: %v", err)
		} else {
			w.told = missing
		}
	}
}
