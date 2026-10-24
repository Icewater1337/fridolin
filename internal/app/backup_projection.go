package app

import (
	"context"
	"fmt"
	"time"

	"github.com/Icewater1337/fridolin/internal/backup"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/google"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
)

func backupOptions(cfgs *config.Store, bm *BrainManager, notify func(context.Context, *brain.Brain, string) error, g *google.Auth) backup.Options {
	boot := cfgs.Boot()
	opts := backup.Options{BackupDir: boot.BackupDir(), RcloneConf: boot.RcloneConf(), Settings: func() backup.Settings { return BackupSettings(cfgs.Get()) },
		Targets: func() []backup.Target { return BackupTargets(bm.Brains(), notify) }, BridgeFiles: func() (backup.BridgeFiles, error) { return BackupFiles(cfgs) },
		FindRclone: findRclone, Environ: func(ctx context.Context) []string {
			return childEnv(helperScope(ctx), nil, "RCLONE_CONFIG="+boot.RcloneConf())
		},
		InstallHint: rcloneInstallHint, RefusalText: outboundmsg.BackupRefusedAlert,
		DriveClientPath: boot.Layout().DriveClient(), OAuth: driveOAuth(g)}
	if notify != nil {
		opts.Notify = func(ctx context.Context, target backup.Target, text string) error { return target.Notify(ctx, text) }
	}
	return opts
}

func newProjectedBackup(cfgs *config.Store, bm *BrainManager, notify func(context.Context, *brain.Brain, string) error, superseded func() bool, g *google.Auth) *backup.Manager {
	opts := backupOptions(cfgs, bm, notify, g)
	opts.Superseded = superseded
	opts.Zone = func() *time.Location { return cfgs.Get().Location() }
	m := backup.New(opts)
	// Publication succeeds before the schedule moves; failed saves never reach this callback.
	cfgs.Subscribe(func(prev, next *config.Settings) { m.SettingsChanged(BackupSettings(prev), BackupSettings(next)) })
	return m
}

// notifyBackup resolves the selected runtime's current reply destination.
func (b *App) notifyBackup(ctx context.Context, br *brain.Brain, text string) error {
	jid := b.replyChatFor(br, b.cfgs.Get())
	if jid == "" {
		return fmt.Errorf("brain %s has no chat to send it to — set its reply chat", br.ID())
	}
	return b.wa.SendMessage(ctx, jid, text)
}

// driveOAuth lets the Drive backup sign in through the bridge's Google
// sign-in (docs/BACKUP.md, Use your own Google client): the client under
// Settings → Google sign-in, its parser, and the bridge's own redirect.
// The browser comes back to the Backup page.
func driveOAuth(g *google.Auth) backup.OAuth {
	if g == nil {
		return backup.OAuth{}
	}
	convert := func(c *google.Client) *backup.OAuthClient {
		if c == nil {
			return nil
		}
		return &backup.OAuthClient{Type: c.Type, ClientID: c.ClientID, ClientSecret: c.ClientSecret}
	}
	return backup.OAuth{
		SignInClient: func() *backup.OAuthClient { return convert(g.Client()) },
		ParseClient: func(data []byte) (*backup.OAuthClient, error) {
			c, err := google.ParseClient(data)
			return convert(c), err
		},
		RedirectURI: g.RedirectURI,
		Start: func(c backup.OAuthClient, scopes []string, done func(*backup.OAuthToken, error)) (string, error) {
			client := &google.Client{Type: c.Type, ClientID: c.ClientID, ClientSecret: c.ClientSecret}
			return g.StartSignInFor(client, scopes, "/#/backup", func(t *google.Token, err error) {
				if err != nil {
					done(nil, err)
					return
				}
				expiry, perr := time.Parse(time.RFC3339Nano, t.Expiry)
				if perr != nil {
					expiry = time.Now().Add(time.Hour)
				}
				done(&backup.OAuthToken{AccessToken: t.Token, RefreshToken: t.RefreshToken, Expiry: expiry}, nil)
			})
		},
	}
}
