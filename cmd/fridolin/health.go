package main

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// The startup checks: files the assistant's tools need, configured in
// startup_checks.required_files (credentials, typically). They are
// checked as they are now, not once at startup: the dashboard shows the
// files missing at the moment it asks, and a watcher logs each change
// and, with notify_whatsapp, sends an alert when the set of missing files
// changes, through the outbox, which waits for WhatsApp to connect.

// requiredFilesEvery is how often the watcher looks at the files.
const requiredFilesEvery = 5 * time.Minute

// missingFiles returns the required files that do not exist now.
func missingFiles(cfg *config.Config) []string {
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
func missingFilesText(cfg *config.Config, missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	msg := fmt.Sprintf("%d required file(s) are missing: %s", len(missing), strings.Join(missing, ", "))
	if cfg.StartupChecks.Hint != "" {
		msg += " — " + cfg.StartupChecks.Hint
	}
	return msg
}

// Warnings returns what the startup checks find now.
func (b *Bridge) Warnings() []string {
	cfg := b.cfgs.Get()
	if msg := missingFilesText(cfg, missingFiles(cfg)); msg != "" {
		return []string{msg}
	}
	return nil
}

// watchRequiredFiles looks at the required files at once and then every
// interval until ctx ends. It logs each change, and with notify_whatsapp
// sends the alert when the files missing are others than it last told
// about: once per change, not on every look, and again after they were
// all back and some went missing once more.
func (b *Bridge) watchRequiredFiles(ctx context.Context, every time.Duration) {
	var seen, told []string
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		cfg := b.cfgs.Get()
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
				jid = ownerChat(cfg)
			}
			if jid != "" {
				if err := b.outbox.Text(ctx, jid, outboundmsg.StartupCheckAlert(msg), cfg.MaxChunkSize); err != nil {
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
