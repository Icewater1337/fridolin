package site

// SiteSettings is <data_dir>/site/settings.json, edited from the dashboard.
type SiteSettings struct {
	Enabled bool   `json:"enabled"`
	Title   string `json:"title"`
	// Listen is where the site process listens; a tunnel forwards the domain to it.
	Listen string `json:"listen"`
	// Domain is the public host name, e.g. "example.com" (https assumed).
	Domain string   `json:"domain"`
	Repo   SiteRepo `json:"repo"`
	// Roles users can have. "admin" is always one of them.
	Roles []string `json:"roles"`
	// RoleBrains are the brains each role sees on the site's built-in
	// /files and /wiki (docs/FILES.md); admin sees every brain.
	RoleBrains map[string][]string `json:"role_brains"`
	// RoleUploads are the brains each role may upload to there, into the
	// folders each brain takes uploads in (brain.json files.uploads), of
	// those it sees; admin changes every brain's files.
	RoleUploads map[string][]string `json:"role_uploads"`
	// Approved is the commit the live site serves.
	Approved string       `json:"approved"`
	History  []SiteDeploy `json:"history"`
	// Tunnel runs cloudflared with the saved tunnel token.
	Tunnel bool `json:"tunnel"`
	// Builders are the brains allowed to build the site (site_builder.go).
	Builders  []string       `json:"builders"`
	Proposals []SiteProposal `json:"proposals"`
}

// SiteRepo is the private repository that holds the site's modules.
type SiteRepo struct {
	URL    string `json:"url"`
	Branch string `json:"branch"`
	// Subdir is the folder inside the repository that holds modules/ ("" = the root).
	Subdir string `json:"subdir"`
}

// SiteDeploy is one approved commit.
type SiteDeploy struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
	At      string `json:"at"`
}

// SiteUser is one account in users.json. The site process reads the same file.
type SiteUser struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"` // google | password
	Email        string `json:"email,omitempty"`
	Username     string `json:"username,omitempty"`
	Name         string `json:"name,omitempty"`
	Role         string `json:"role"`
	PasswordHash string `json:"password_hash,omitempty"`
	Created      string `json:"created"`
}

// SiteSettingsUpdate is what the dashboard may change directly.
type SiteSettingsUpdate struct {
	Enabled *bool   `json:"enabled"`
	Title   *string `json:"title"`
	Listen  *string `json:"listen"`
	Domain  *string `json:"domain"`
	Tunnel  *bool   `json:"tunnel"`
}

// SiteUserInput adds or changes an account.
type SiteUserInput struct {
	Kind     string `json:"kind"`
	Email    string `json:"email"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Password string `json:"password"`
	// PasswordHash moves a password account over from another app with the
	// password its owner had there: a bcrypt hash, which the site checks as
	// it is. Only when adding an account, instead of Password.
	PasswordHash string `json:"password_hash"`
}

// SiteProposal is a change a builder brain pushed for review.
type SiteProposal struct {
	Branch  string `json:"branch"` // builder/<brain>
	Brain   string `json:"brain"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Commit  string `json:"commit"`
	At      string `json:"at"`
}

// SiteCommit is one commit on the branch.
type SiteCommit struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	At      string `json:"at"`
}

// SiteUpdates is what the branch has beyond the approved commit.
type SiteUpdates struct {
	Head     string         `json:"head"`
	Approved string         `json:"approved"`
	Commits  []SiteCommit   `json:"commits"`
	Changes  []ModuleChange `json:"changes"`
}

// ModuleChange is one module that differs between the approved commit and another one.
type ModuleChange struct {
	ID     string `json:"id"`
	Status string `json:"status"` // added, removed, changed
	// Review lists what an approver should look at: access, routes, server
	// code, files, commands, secrets, public files.
	Review []ReviewItem `json:"review"`
	Files  []string     `json:"files"`
}

// ReviewItem is one security-relevant manifest field that changed.
type ReviewItem struct {
	Field string `json:"field"`
	Old   any    `json:"old"`
	New   any    `json:"new"`
	Note  string `json:"note"`
}

// CheckResult is what site_check returns.
type CheckResult struct {
	Mounted []string       `json:"mounted"`
	Broken  []BrokenModule `json:"broken"`
	Review  []ModuleChange `json:"review"`
}

// BrokenModule is a module the site would skip, and why.
type BrokenModule struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// SiteModuleSecrets is one module's secrets as the dashboard shows them:
// the names, never the values.
type SiteModuleSecrets struct {
	Module string `json:"module"`
	Title  string `json:"title"`
	// Live: the module is in the live commit.
	Live bool `json:"live"`
	// Declared are the names its live module.json lists; Set those with a
	// value, declared or not (a value nobody declares reaches no module).
	Declared []string `json:"declared"`
	Set      []string `json:"set"`
}

// SiteProcStatus is what the dashboard shows about one process.
type SiteProcStatus struct {
	State    string   `json:"state"` // stopped, starting, running, crashed
	Since    string   `json:"since,omitempty"`
	Error    string   `json:"error,omitempty"`
	Restarts int      `json:"restarts"`
	Log      []string `json:"log"`
}

// SiteSandboxStatus is what the dashboard shows about the sandbox.
type SiteSandboxStatus struct {
	On   bool   `json:"on"`
	Note string `json:"note"`
}

func (u SiteUser) Display() string {
	if u.Kind == "google" {
		return u.Email
	}
	return u.Username
}

// DeploySnapshot is an owned copy of a deployment job for consumers.
type DeploySnapshot struct {
	Commit   string   `json:"commit"`
	State    string   `json:"state"` // running, done, error
	Step     string   `json:"step"`
	Error    string   `json:"error,omitempty"`
	Log      []string `json:"log"`
	Started  string   `json:"started"`
	Finished string   `json:"finished,omitempty"`
}
