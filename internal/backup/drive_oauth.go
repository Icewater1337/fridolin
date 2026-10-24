package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// Connecting Google Drive with the owner's own OAuth client.
//
// rclone's shared Google Drive client is retired during 2026, so the
// bridge no longer lets rclone sign in with it. It signs in itself, the
// way a brain's calendar does: through the owner's Google OAuth client —
// the one under Settings → Google sign-in, or one pasted for Drive alone —
// and the bridge's own redirect (http://<http_listen>/), which works in
// the core's container too, where rclone could not open a browser. The
// token goes into the rclone remote with the client's id and secret
// (client_id, client_secret), so rclone refreshes it with that client.
//
// The scope stays drive.file: the link sees only what its client
// created, never the rest of the owner's Drive. That is also why a new
// client cannot see, let alone delete, what rclone's shared client made
// (Drive answers appNotAuthorizedToChild): the link therefore lives in a
// folder of its own that its client creates, DriveFolder, through
// rclone's root_folder_id. backup.root and the vault's fridolin-restic
// are made inside it, and the folders the shared client made stay at the
// top of the Drive, untouched, until the owner deletes them there.

// DriveScope is the one scope the Drive link asks Google for.
const DriveScope = "https://www.googleapis.com/auth/drive.file"

// DriveFolder is the folder in the top of the owner's Drive that a link
// with the owner's client keeps everything in (root_folder_id). A folder
// of this name that the client can see is one it made, and is used again.
const DriveFolder = "Fridolin"

// OAuthClient is a Google OAuth client: "installed" (Desktop app) or "web".
type OAuthClient struct {
	Type, ClientID, ClientSecret string
	// Builtin marks the client baked into the build, which SignInClient
	// returns when none was pasted under Settings → Google sign-in.
	Builtin bool
}

// OAuthToken is what a Drive sign-in returns.
type OAuthToken struct {
	AccessToken, RefreshToken string
	Expiry                    time.Time
}

// OAuth is Google sign-in as the bridge runs it, injected by the command
// (internal/google), which the backup package may not import.
type OAuth struct {
	// SignInClient is the client under Settings → Google sign-in, or nil.
	SignInClient func() *OAuthClient
	// ParseClient reads a client JSON file as Google Cloud Console offers it.
	ParseClient func([]byte) (*OAuthClient, error)
	// RedirectURI is where Google sends the browser back.
	RedirectURI func() string
	// Start begins a sign-in with c for scopes and returns Google's URL.
	// done gets the token, or why there is none, at most once.
	Start func(c OAuthClient, scopes []string, done func(*OAuthToken, error)) (string, error)
}

// ClientStatus is the client a Drive sign-in would use, for the Backup page.
type ClientStatus struct {
	// Source is "drive" (pasted for Drive alone), "google" (the client
	// under Settings → Google sign-in) or "builtin" (the build's own,
	// without one there).
	Source       string `json:"source"`
	Type         string `json:"type"`
	ClientIDHint string `json:"client_id_hint"`
}

// clientIDHint shows a client id's project number only.
func clientIDHint(id string) string {
	if i := strings.Index(id, "-"); i > 0 {
		return id[:i] + "-…"
	}
	return "…"
}

// driveClient is the client a Drive sign-in uses: the one pasted for
// Drive, else the Google sign-in one, else none.
func (m *Manager) driveClient() (*OAuthClient, string) {
	if m.opts.DriveClientPath != "" && m.opts.OAuth.ParseClient != nil {
		if data, err := os.ReadFile(m.opts.DriveClientPath); err == nil {
			if c, err := m.opts.OAuth.ParseClient(data); err == nil {
				return c, "drive"
			}
		}
	}
	if m.opts.OAuth.SignInClient != nil {
		if c := m.opts.OAuth.SignInClient(); c != nil {
			if c.Builtin {
				return c, "builtin"
			}
			return c, "google"
		}
	}
	return nil, ""
}

func (m *Manager) clientStatus() *ClientStatus {
	c, source := m.driveClient()
	if c == nil {
		return nil
	}
	return &ClientStatus{Source: source, Type: c.Type, ClientIDHint: clientIDHint(c.ClientID)}
}

// SetDriveClient keeps a client pasted for Drive alone. It is used from the
// next sign-in on; the link already there keeps the client it has.
func (m *Manager) SetDriveClient(data []byte) (*OAuthClient, error) {
	if m.opts.OAuth.ParseClient == nil || m.opts.DriveClientPath == "" {
		return nil, errors.New("google sign-in is not available")
	}
	c, err := m.opts.OAuth.ParseClient(data)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(m.opts.DriveClientPath), 0o700); err != nil {
		return nil, err
	}
	if err := fsx.WriteFileAtomic(m.opts.DriveClientPath, data, 0o600, false); err != nil {
		return nil, err
	}
	return c, nil
}

// DeleteDriveClient forgets the client pasted for Drive; sign-ins use the
// Google sign-in one again.
func (m *Manager) DeleteDriveClient() error {
	if m.opts.DriveClientPath == "" {
		return nil
	}
	if err := os.Remove(m.opts.DriveClientPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// usesSharedClient reports whether a remote's section is a Drive link
// through rclone's shared client: a Drive remote without a client_id.
// compat: such a link, made by an older version, keeps backing up as it
// is until Google retires that client; the Backup page and doctor's
// drive-client check offer the switch (Connect with reconnect).
func usesSharedClient(section map[string]string) bool {
	return section != nil && section["type"] == "drive" && section["client_id"] == ""
}

// SharedClientRemotes names the Drive remotes in an rclone config that
// use rclone's shared client, which Google retires during 2026.
func SharedClientRemotes(conf string) []string {
	var out []string
	for _, name := range rcloneSectionNames(conf) {
		if usesSharedClient(RcloneSection(conf, name)) {
			out = append(out, name)
		}
	}
	return out
}

// ── the rclone config writer ─────────────────────────────────────────────

// rcloneKV is one line of a section, in the order it is written.
type rcloneKV struct{ key, value string }

func rcloneSectionNames(conf string) []string {
	var out []string
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			out = append(out, line[1:len(line)-1])
		}
	}
	return out
}

// setRcloneSection returns conf with section name replaced by kvs, or
// with it added at the end; kvs nil removes it. Every other line stays as
// it is.
func setRcloneSection(conf, name string, kvs []rcloneKV) (string, error) {
	for _, kv := range kvs {
		if strings.ContainsAny(kv.key+kv.value, "\r\n") || strings.ContainsAny(kv.key, "=[]") {
			return "", fmt.Errorf("an rclone setting cannot hold %q", kv.key)
		}
	}
	lines := strings.Split(conf, "\n")
	var out []string
	in := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			in = t == "["+name+"]"
		}
		if !in {
			out = append(out, line)
		}
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if kvs != nil {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, "["+name+"]")
		for _, kv := range kvs {
			out = append(out, kv.key+" = "+kv.value)
		}
	}
	if len(out) == 0 {
		return "", nil
	}
	return strings.Join(out, "\n") + "\n", nil
}

// writeRcloneSection sets (or, with kvs nil, removes) a section of the
// bridge's rclone config, which it writes atomically, mode 0600.
func (m *Manager) writeRcloneSection(name string, kvs []rcloneKV) error {
	data, err := os.ReadFile(m.rcloneConf())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	conf, err := setRcloneSection(string(data), name, kvs)
	if err != nil {
		return err
	}
	if conf == string(data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.rcloneConf()), 0o700); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(m.rcloneConf(), []byte(conf), 0o600, false)
}

// rcloneToken is the token as rclone keeps it in a remote's token line.
func rcloneToken(tok *OAuthToken) (string, error) {
	b, err := json.Marshal(struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		Expiry       string `json:"expiry"`
	}{tok.AccessToken, "Bearer", tok.RefreshToken, tok.Expiry.UTC().Format(time.RFC3339Nano)})
	return string(b), err
}

// ── the sign-in ──────────────────────────────────────────────────────────

// connectTimeout is how long a Drive sign-in waits for you to approve it.
const connectTimeout = 10 * time.Minute

// driveSignIn is what a Drive sign-in's done hands its job.
type driveSignIn struct {
	tok *OAuthToken
	err error
}

// connectDrive waits for the sign-in started with authURL to come back
// (or connectTimeout to run out, or a cancel), then puts the link in place.
func (m *Manager) connectDrive(ctx context.Context, job *connectJob, c OAuthClient, signedIn <-chan driveSignIn) error {
	var r driveSignIn
	select {
	case r = <-signedIn:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("access was not approved within %v — try again", connectTimeout)
		}
		return errors.New("connecting was cancelled")
	}
	if r.err != nil {
		return r.err
	}
	m.mu.Lock()
	job.Step, job.AuthURL = stepFolder, ""
	m.mu.Unlock()
	return m.installDriveLink(ctx, job, c, r.tok)
}

// connectingSuffix names the remote a new link is tried out as before it
// replaces backup.remote, so that a link that does not work never takes
// the place of one that does.
const connectingSuffix = "-connecting"

// installDriveLink writes the new link as a remote of its own, finds or
// makes DriveFolder with it, proves it by making backup.root there, and
// only then puts it in backup.remote's place. Until that last step the
// remote there, a link through rclone's shared client say, is left as it
// is and keeps backing up.
func (m *Manager) installDriveLink(ctx context.Context, job *connectJob, c OAuthClient, tok *OAuthToken) (err error) {
	remote := m.opts.Settings().Remote
	trial := remote + connectingSuffix
	token, err := rcloneToken(tok)
	if err != nil {
		return err
	}
	kvs := []rcloneKV{{"type", "drive"}, {"client_id", c.ClientID}, {"client_secret", c.ClientSecret}, {"scope", "drive.file"}, {"token", token}}
	m.mu.Lock()
	err = m.writeRcloneSection(trial, kvs)
	m.mu.Unlock()
	if err != nil {
		return fmt.Errorf("writing the rclone config: %w", err)
	}
	defer func() {
		m.mu.Lock()
		if rerr := m.writeRcloneSection(trial, nil); rerr != nil && err == nil {
			err = fmt.Errorf("removing the trial remote: %w", rerr)
		}
		m.mu.Unlock()
	}()
	id, err := m.driveFolderID(ctx, trial)
	if err != nil {
		return err
	}
	if id == "" {
		if _, err := m.runner(ctx, []string{"mkdir", trial + ":" + DriveFolder}, nil); err != nil {
			return fmt.Errorf("creating the folder %s in Google Drive failed: %v", DriveFolder, err)
		}
		if id, err = m.driveFolderID(ctx, trial); err != nil {
			return err
		}
		if id == "" {
			return fmt.Errorf("the folder %s was created in Google Drive but cannot be found", DriveFolder)
		}
	}
	kvs = append(kvs, rcloneKV{"root_folder_id", id})
	m.mu.Lock()
	err = m.writeRcloneSection(trial, kvs)
	m.mu.Unlock()
	if err != nil {
		return fmt.Errorf("writing the rclone config: %w", err)
	}
	// Prove the link works and create the backup folder.
	if _, err := m.runner(ctx, []string{"mkdir", trial + ":" + m.opts.Settings().Root}, nil); err != nil {
		return fmt.Errorf("connected, but creating the backup folder failed: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.connect != job || ctx.Err() != nil:
		return errors.New("connecting was cancelled")
	case m.disconnecting:
		return errDisconnecting
	case m.job != nil:
		// rclone rewrites its config when it refreshes a token, and
		// would write the old remote back over the new one.
		return errors.New("a backup or restore started while Google Drive was being connected; the old link is unchanged — connect again once it has finished")
	}
	// What rclone may have written to the trial remote (a token it
	// refreshed) goes along.
	data, _ := os.ReadFile(m.rcloneConf())
	if cur := RcloneSection(string(data), trial); cur != nil {
		for i := range kvs {
			if v := cur[kvs[i].key]; v != "" {
				kvs[i].value = v
			}
		}
	}
	if err := m.writeRcloneSection(remote, kvs); err != nil {
		return fmt.Errorf("writing the rclone config: %w", err)
	}
	return nil
}

// driveFolderID is the id of DriveFolder at the top of remote's Drive, as
// far as the remote's client can see it, or "".
func (m *Manager) driveFolderID(ctx context.Context, remote string) (string, error) {
	out, err := m.runner(ctx, []string{"lsjson", "--dirs-only", "--no-modtime", "--no-mimetype", remote + ":"}, nil)
	if err != nil {
		return "", fmt.Errorf("listing Google Drive failed: %v", err)
	}
	var entries []struct {
		Name  string `json:"Name"`
		ID    string `json:"ID"`
		IsDir bool   `json:"IsDir"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return "", fmt.Errorf("listing Google Drive: %w", backupJSONError(err))
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir && e.Name == DriveFolder && e.ID != "" {
			ids = append(ids, e.ID)
		}
	}
	if len(ids) == 0 {
		return "", nil
	}
	// Two of them (made by hand in Drive's web page, say): the same one each time.
	slices.Sort(ids)
	return ids[0], nil
}
