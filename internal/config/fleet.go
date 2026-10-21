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
	if !ValidListenAddr(c.Nodes.Listen) {
		return fmt.Errorf("nodes.listen %q must be host:port, such as %s for every interface", c.Nodes.Listen, DefaultNodesListen)
	}
	return nil
}
