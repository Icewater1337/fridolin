package app

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

// RequiredFilesOptions wires the current configuration and delivery owner.
// Constructing it starts no watcher; WatchRequiredFiles runs until cancellation.
type RequiredFilesOptions struct {
	Config    func() *config.Settings
	OwnerChat func(*config.Settings) string
	Notify    func(context.Context, string, string, int) error
}

// RequiredFileWarnings reports missing required files at the time of the call.
func RequiredFileWarnings(cfg *config.Settings) []string {
	if msg := missingFilesText(cfg, missingFiles(cfg)); msg != "" {
		return []string{msg}
	}
	return nil
}

// missingFiles returns the required files that do not exist now.
func missingFiles(cfg *config.Settings) []string {
	var missing []string
	for _, f := range cfg.StartupChecks.RequiredFiles {
		if !fsx.Exists(f) {
			missing = append(missing, f)
		}
	}
	return missing
}

// missingFilesText says which required files are missing, with the
// configured hint; "" when none is.
func missingFilesText(cfg *config.Settings, missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	msg := fmt.Sprintf("%d required file(s) are missing: %s", len(missing), strings.Join(missing, ", "))
	if cfg.StartupChecks.Hint != "" {
		msg += " — " + cfg.StartupChecks.Hint
	}
	return msg
}

// WatchRequiredFiles looks at the required files at once and then every
// interval until ctx ends. It logs each change, and with notify_whatsapp
// sends the alert when the files missing are others than it last told
// about: once per change, not on every look, and again after they were
// all back and some went missing once more.
func WatchRequiredFiles(ctx context.Context, every time.Duration, options RequiredFilesOptions) {
	var seen, told []string
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		cfg := options.Config()
		missing := missingFiles(cfg)
		msg := missingFilesText(cfg, missing)
		switch {
		case !slices.Equal(missing, seen) && msg != "":
			log.Printf("STARTUP CHECK FAILED: %s", msg)
		case !slices.Equal(missing, seen):
			log.Printf("startup check: the required files are all there now")
			told = nil
		}
		seen = missing
		if msg != "" && cfg.StartupChecks.NotifyWhatsApp && !slices.Equal(missing, told) {
			jid := cfg.StartupChecks.NotifyJID
			if jid == "" {
				jid = options.OwnerChat(cfg)
			}
			if jid != "" {
				if err := options.Notify(ctx, jid, outboundmsg.StartupCheckAlert(msg), cfg.MaxChunkSize); err != nil {
					log.Printf("startup check: %v", err)
				} else {
					told = missing
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
