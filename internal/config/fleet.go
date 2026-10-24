package config

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The sections below are the settings of the containerized core, its
// nodes, the tiered vault and the approval policy (docs/PLAN.md). They are
// read by the code that runs those parts; every one is off by default, so a
// config.json without them runs as before.

// CoreConfig is the core: the one host whose bridge holds the WhatsApp
// session, reachable on the tailnet and guarded by a lease in the cloud.
type CoreConfig struct {
	Tailnet TailnetConfig `json:"tailnet"`
	Lease   LeaseConfig   `json:"lease"`
	// MemoryLimitGB is the memory the core's container may use, in GB
	// (default 8).
	MemoryLimitGB int `json:"memory_limit_gb,omitempty"`
}

// TailnetConfig makes the core its own device on the tailnet (tsnet).
type TailnetConfig struct {
	// Enabled joins the tailnet with the auth key from the host's secrets.
	Enabled bool `json:"enabled"`
	// Hostname is the core's name on the tailnet (default fridolin-core):
	// a DNS label, lower-case letters, digits and -.
	Hostname string `json:"hostname,omitempty"`
}

// LeaseConfig is the core lease: one object in the vault bucket that says
// which host is the core, so two copies never run one WhatsApp session.
type LeaseConfig struct {
	// Enabled takes and renews the lease, and refuses to run as core
	// without it.
	Enabled bool `json:"enabled"`
	// RenewSeconds is how often the holder renews it (default 300).
	RenewSeconds int `json:"renew_seconds,omitempty"`
	// StaleSeconds is how long after its last renewal another host may take
	// it over (default 1200). It must be more than RenewSeconds.
	StaleSeconds int `json:"stale_seconds,omitempty"`
}

// Renew is how often the lease is renewed.
func (l LeaseConfig) Renew() time.Duration { return time.Duration(l.RenewSeconds) * time.Second }

// Stale is how long an unrenewed lease holds.
func (l LeaseConfig) Stale() time.Duration { return time.Duration(l.StaleSeconds) * time.Second }

// VaultConfig is the tiered store: the hot tier in the data dir, the cold
// tier and the snapshots in Backblaze B2, reached through rclone.
type VaultConfig struct {
	// Enabled runs the vault service.
	Enabled bool `json:"enabled"`
	// HotBudgetGB is the most the hot tier may hold, in GB (default 50).
	HotBudgetGB int `json:"hot_budget_gb,omitempty"`
	// ActivityDays keeps a file hot this long after it was last used
	// (default 60).
	ActivityDays int `json:"activity_days,omitempty"`
	// ColdAfterDays lets a binary go cold after this long even without a
	// markdown companion (default 90).
	ColdAfterDays int `json:"cold_after_days,omitempty"`
	// HeadroomPercent of the budget is kept free by eviction (default 10).
	HeadroomPercent int `json:"headroom_percent,omitempty"`
	// Bucket holds the cold tier, the core exports and the lease (default
	// fridolin-vault).
	Bucket string `json:"bucket,omitempty"`
	// SnapshotsBucket holds the restic and Litestream snapshots (default
	// fridolin-snapshots).
	SnapshotsBucket string `json:"snapshots_bucket,omitempty"`
	// Endpoint is the B2 S3 endpoint's host name (default
	// s3.eu-central-003.backblazeb2.com).
	Endpoint string `json:"endpoint,omitempty"`
	// B2DailyBudget is how many Class B and how many Class C transactions
	// a day the bridge may spend on B2 before doctor and the dashboard warn
	// (at 70%; default 2000, under the free tier's 2,500 caps).
	B2DailyBudget int `json:"b2_daily_budget,omitempty"`
	// Targets are further places the snapshots go: restic repositories in
	// folders on nodes (docs/VAULT.md, "Node targets"). They run whether
	// or not the vault itself is on; each brain's brain.json says which of
	// them get its folder.
	Targets []VaultTarget `json:"targets,omitempty"`
}

// VaultTarget is one backup target besides the cloud.
type VaultTarget struct {
	// Name names it: in brain.json ("node:<name>"), on the Vault page, in
	// its password's host secret (fridolin.restic.node.<name>).
	Name string `json:"name"`
	// Kind is TargetNode, the only kind yet.
	Kind string `json:"kind"`
	// Node is the node's id, Folder the folder on it, in its own
	// spelling (D:\Backups\fridolin, /Volumes/Backup/fridolin).
	Node   string `json:"node"`
	Folder string `json:"folder"`
	// NightlyAt is when the nightly snapshot runs, HH:MM local time
	// (default 03:00); Hourly adds one each hour.
	NightlyAt string `json:"nightly_at,omitempty"`
	Hourly    bool   `json:"hourly,omitempty"`
	// IncludeDataDir puts the data dir in the nightly snapshot too, less
	// the brain folders in it that do not choose this target.
	IncludeDataDir bool `json:"include_data_dir,omitempty"`
	// Retention is what pruning keeps (default 24 hourly, 14 daily, 8
	// weekly, 12 monthly, as the cloud's).
	Retention *TargetRetention `json:"retention,omitempty"`
	// CheckDays is how often restic check runs (default 7); ReadDataDays
	// how often a check also reads 5% of the data back (default 30).
	CheckDays    int `json:"check_days,omitempty"`
	ReadDataDays int `json:"read_data_days,omitempty"`
	// WindowHours is how old the last snapshot may be before doctor
	// warns: the node is off, or its disk unplugged (default 26).
	WindowHours int `json:"window_hours,omitempty"`
}

// TargetRetention is how many snapshots of each span a target keeps.
type TargetRetention struct {
	Hourly  int `json:"hourly"`
	Daily   int `json:"daily"`
	Weekly  int `json:"weekly"`
	Monthly int `json:"monthly"`
}

// TargetNode is the kind of a target in a folder on a node.
const TargetNode = "node"

// TargetKinds are the kinds a target may be: config.schema.json lists the same.
func TargetKinds() []string { return []string{TargetNode} }

// Defaults of a target.
const (
	DefaultTargetNightlyAt    = "03:00"
	DefaultTargetCheckDays    = 7
	DefaultTargetReadDataDays = 30
	DefaultTargetWindowHours  = 26
)

// DefaultTargetRetention is the cloud snapshots' retention.
var DefaultTargetRetention = TargetRetention{Hourly: 24, Daily: 14, Weekly: 8, Monthly: 12}

// Night is NightlyAt with its default.
func (t VaultTarget) Night() string {
	if t.NightlyAt == "" {
		return DefaultTargetNightlyAt
	}
	return t.NightlyAt
}

// Keep is Retention with its default.
func (t VaultTarget) Keep() TargetRetention {
	if t.Retention == nil {
		return DefaultTargetRetention
	}
	return *t.Retention
}

func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// CheckEvery, ReadDataEvery and Window are the target's intervals.
func (t VaultTarget) CheckEvery() time.Duration {
	return time.Duration(orDefault(t.CheckDays, DefaultTargetCheckDays)) * 24 * time.Hour
}
func (t VaultTarget) ReadDataEvery() time.Duration {
	return time.Duration(orDefault(t.ReadDataDays, DefaultTargetReadDataDays)) * 24 * time.Hour
}
func (t VaultTarget) Window() time.Duration {
	return time.Duration(orDefault(t.WindowHours, DefaultTargetWindowHours)) * time.Hour
}

// TargetKey is how brain.json's backup.targets names the target.
func (t VaultTarget) TargetKey() string { return "node:" + t.Name }

// Target returns the target named name.
func (v VaultConfig) Target(name string) (VaultTarget, bool) {
	for _, t := range v.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return VaultTarget{}, false
}

var (
	targetNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	nodeIDRe     = regexp.MustCompile(`^[0-9a-f]{12}$`)
	hhmmRe       = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// ValidTargetName reports whether name may name a target.
func ValidTargetName(name string) bool { return targetNameRe.MatchString(name) }

// absPathAnyOS reports whether p is absolute in Unix or Windows spelling.
func absPathAnyOS(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && (p[0]|0x20) >= 'a' && (p[0]|0x20) <= 'z' && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// maxTargets bounds vault.targets.
const maxTargets = 16

// Check reports what is wrong with the target.
func (t VaultTarget) Check() error {
	switch {
	case !ValidTargetName(t.Name):
		return fmt.Errorf("vault.targets: name %q must be 1 to 32 lower-case letters, digits and -", t.Name)
	case t.Kind != TargetNode:
		return fmt.Errorf("vault.targets %s: kind %q must be %q", t.Name, t.Kind, TargetNode)
	case !nodeIDRe.MatchString(t.Node):
		return fmt.Errorf("vault.targets %s: node %q must be a node's id, 12 hex digits (fridolin nodes lists them)", t.Name, t.Node)
	case t.Folder == "" || len(t.Folder) > 4096 || strings.ContainsRune(t.Folder, 0) || !absPathAnyOS(t.Folder):
		return fmt.Errorf("vault.targets %s: folder %q must be an absolute path on the node", t.Name, t.Folder)
	case t.NightlyAt != "" && !hhmmRe.MatchString(t.NightlyAt):
		return fmt.Errorf("vault.targets %s: nightly_at %q must be HH:MM", t.Name, t.NightlyAt)
	case t.CheckDays < 0 || t.CheckDays > maxDays, t.ReadDataDays < 0 || t.ReadDataDays > maxDays:
		return fmt.Errorf("vault.targets %s: check_days and read_data_days must be between 1 and %d, or 0 for the default", t.Name, maxDays)
	case t.WindowHours < 0 || t.WindowHours > 24*maxDays:
		return fmt.Errorf("vault.targets %s: window_hours must be between 1 and %d, or 0 for the default", t.Name, 24*maxDays)
	}
	if r := t.Retention; r != nil {
		for _, n := range []int{r.Hourly, r.Daily, r.Weekly, r.Monthly} {
			if n < 0 || n > 100000 {
				return fmt.Errorf("vault.targets %s: retention counts must be between 0 and 100000", t.Name)
			}
		}
		if *r == (TargetRetention{}) {
			return fmt.Errorf("vault.targets %s: a retention that keeps no snapshot would delete them all; leave retention out for the default", t.Name)
		}
	}
	return nil
}

// checkTargets checks every target and that names do not repeat.
func checkTargets(ts []VaultTarget) error {
	if len(ts) > maxTargets {
		return fmt.Errorf("vault.targets: at most %d", maxTargets)
	}
	seen := map[string]bool{}
	for _, t := range ts {
		if err := t.Check(); err != nil {
			return err
		}
		if seen[t.Name] {
			return fmt.Errorf("vault.targets: two targets are named %q", t.Name)
		}
		seen[t.Name] = true
	}
	return nil
}

// NodesConfig is the port nodes connect to.
type NodesConfig struct {
	// Enabled accepts node connections.
	Enabled bool `json:"enabled"`
	// Listen is the node port's address, host:port (default :8766).
	Listen string `json:"listen,omitempty"`
}

// PolicyConfig limits what brains may do without asking.
type PolicyConfig struct {
	// MaxRunsPerHour is the most runs one brain starts in an hour
	// (default 20).
	MaxRunsPerHour int `json:"max_runs_per_hour,omitempty"`
	// MaxPendingIrreversible is the most irreversible actions waiting for
	// approval at once (default 3).
	MaxPendingIrreversible int `json:"max_pending_irreversible,omitempty"`
	// ApprovalTimeoutHours is how long an approval poll waits before it
	// counts as no (default 24).
	ApprovalTimeoutHours int `json:"approval_timeout_hours,omitempty"`
}

// ApprovalTimeout is how long an approval waits.
func (p PolicyConfig) ApprovalTimeout() time.Duration {
	return time.Duration(p.ApprovalTimeoutHours) * time.Hour
}

// Defaults of the sections above.
const (
	DefaultTailnetHostname        = "fridolin-core"
	DefaultLeaseRenewSeconds      = 300
	DefaultLeaseStaleSeconds      = 1200
	DefaultCoreMemoryLimitGB      = 8
	DefaultVaultHotBudgetGB       = 50
	DefaultVaultActivityDays      = 60
	DefaultVaultColdAfterDays     = 90
	DefaultVaultHeadroomPercent   = 10
	DefaultVaultBucket            = "fridolin-vault"
	DefaultSnapshotsBucket        = "fridolin-snapshots"
	DefaultVaultEndpoint          = "s3.eu-central-003.backblazeb2.com"
	DefaultB2DailyBudget          = 2000
	DefaultNodesListen            = ":8766"
	DefaultMaxRunsPerHour         = 20
	DefaultMaxPendingIrreversible = 3
	DefaultApprovalTimeoutHours   = 24
)

// fillFleetDefaults gives the sections' unset (zero) values their defaults.
func fillFleetDefaults(c *File) {
	def := func(p *int, v int) {
		if *p == 0 {
			*p = v
		}
	}
	defStr := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	defStr(&c.Core.Tailnet.Hostname, DefaultTailnetHostname)
	def(&c.Core.Lease.RenewSeconds, DefaultLeaseRenewSeconds)
	def(&c.Core.Lease.StaleSeconds, DefaultLeaseStaleSeconds)
	def(&c.Core.MemoryLimitGB, DefaultCoreMemoryLimitGB)
	def(&c.Vault.HotBudgetGB, DefaultVaultHotBudgetGB)
	def(&c.Vault.ActivityDays, DefaultVaultActivityDays)
	def(&c.Vault.ColdAfterDays, DefaultVaultColdAfterDays)
	def(&c.Vault.HeadroomPercent, DefaultVaultHeadroomPercent)
	defStr(&c.Vault.Bucket, DefaultVaultBucket)
	defStr(&c.Vault.SnapshotsBucket, DefaultSnapshotsBucket)
	defStr(&c.Vault.Endpoint, DefaultVaultEndpoint)
	def(&c.Vault.B2DailyBudget, DefaultB2DailyBudget)
	defStr(&c.Nodes.Listen, DefaultNodesListen)
	def(&c.Policy.MaxRunsPerHour, DefaultMaxRunsPerHour)
	def(&c.Policy.MaxPendingIrreversible, DefaultMaxPendingIrreversible)
	def(&c.Policy.ApprovalTimeoutHours, DefaultApprovalTimeoutHours)
	fillHousekeepingDefaults(&c.Housekeeping)
	fillSchedulesDefaults(&c.Schedules)
}

var (
	// dnsLabelRe is one label of a host name, lower case.
	dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// bucketRe is a B2 bucket name as its S3 API takes it.
	bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{4,61}[a-z0-9]$`)
)

// ValidHostname reports whether name is a lower-case DNS host name: dotted
// labels of letters, digits and -, at most 253 characters.
func ValidHostname(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if !dnsLabelRe.MatchString(label) {
			return false
		}
	}
	return true
}

// ValidBucket reports whether name is a bucket name B2 takes: 6 to 63
// lower-case letters, digits and -, not starting with b2-.
func ValidBucket(name string) bool {
	return bucketRe.MatchString(name) && !strings.HasPrefix(name, "b2-")
}

// ValidListenAddr reports whether addr is host:port with a port from 1 to
// 65535 and a host that is empty (every interface), an IP address or a
// host name.
func ValidListenAddr(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return false
	}
	return host == "" || net.ParseIP(host) != nil || ValidHostname(host)
}

// Upper bounds, so each value converts to a time.Duration and a typo of a
// few zeros is caught.
const (
	maxLeaseSeconds         = 24 * 60 * 60
	maxMemoryLimitGB        = 4096
	maxHotBudgetGB          = 1 << 20
	maxDays                 = 100 * 365
	maxHeadroomPercent      = 90
	maxRunsPerHour          = 10000
	maxPendingIrreversible  = 1000
	maxApprovalTimeoutHours = 30 * 24
	maxB2DailyBudget        = 10_000_000
)

// validateFleet checks the sections once their defaults are filled in.
func (c *File) validateFleet() error {
	for _, n := range []struct {
		name  string
		value int
		max   int
	}{
		{"core.lease.renew_seconds", c.Core.Lease.RenewSeconds, maxLeaseSeconds},
		{"core.lease.stale_seconds", c.Core.Lease.StaleSeconds, maxLeaseSeconds},
		{"core.memory_limit_gb", c.Core.MemoryLimitGB, maxMemoryLimitGB},
		{"vault.hot_budget_gb", c.Vault.HotBudgetGB, maxHotBudgetGB},
		{"vault.activity_days", c.Vault.ActivityDays, maxDays},
		{"vault.cold_after_days", c.Vault.ColdAfterDays, maxDays},
		{"vault.headroom_percent", c.Vault.HeadroomPercent, maxHeadroomPercent},
		{"vault.b2_daily_budget", c.Vault.B2DailyBudget, maxB2DailyBudget},
		{"policy.max_runs_per_hour", c.Policy.MaxRunsPerHour, maxRunsPerHour},
		{"policy.max_pending_irreversible", c.Policy.MaxPendingIrreversible, maxPendingIrreversible},
		{"policy.approval_timeout_hours", c.Policy.ApprovalTimeoutHours, maxApprovalTimeoutHours},
	} {
		if n.value < 1 || n.value > n.max {
			return fmt.Errorf("%s must be between 1 and %d, or 0 for the default, not %d", n.name, n.max, n.value)
		}
	}
	if l := c.Core.Lease; l.StaleSeconds <= l.RenewSeconds {
		return fmt.Errorf("core.lease.stale_seconds (%d) must be more than core.lease.renew_seconds (%d), or another host takes over a lease that is still renewed", l.StaleSeconds, l.RenewSeconds)
	}
	if h := c.Core.Tailnet.Hostname; !dnsLabelRe.MatchString(h) {
		return fmt.Errorf("core.tailnet.hostname %q must be a host name without dots: lower-case letters, digits and -, at most 63", h)
	}
	if !ValidBucket(c.Vault.Bucket) {
		return fmt.Errorf("vault.bucket %q must be a bucket name: 6 to 63 lower-case letters, digits and -, not starting with b2-", c.Vault.Bucket)
	}
	if !ValidBucket(c.Vault.SnapshotsBucket) {
		return fmt.Errorf("vault.snapshots_bucket %q must be a bucket name: 6 to 63 lower-case letters, digits and -, not starting with b2-", c.Vault.SnapshotsBucket)
	}
	if !ValidHostname(c.Vault.Endpoint) {
		return fmt.Errorf("vault.endpoint %q must be a host name such as %s, without https:// or a path", c.Vault.Endpoint, DefaultVaultEndpoint)
	}
	if err := checkTargets(c.Vault.Targets); err != nil {
		return err
	}
	if !ValidListenAddr(c.Nodes.Listen) {
		return fmt.Errorf("nodes.listen %q must be host:port, such as %s for every interface", c.Nodes.Listen, DefaultNodesListen)
	}
	if err := c.Housekeeping.validate(); err != nil {
		return err
	}
	return c.Schedules.validate()
}
