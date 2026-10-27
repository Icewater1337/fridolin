// What the bridge's GET endpoints answer, as the dashboard reads them.
// Never served and never built: web/assets is what the binary embeds, and
// this file is only for tsc (make typecheck-web). The dashboard's modules
// refer to it from JSDoc:
//
//   /** @import { Status } from '../../types/api.js' */
//
// The Go handlers are the source of truth (internal/httpapi and what
// internal/app hands them), and cmd/fridolin/testdata/golden/api holds an
// example answer of each GET. A change to a response changes its type
// here. The Go encoding rules this follows: a field with omitempty is
// optional, a pointer without it is `T | null`, a slice the handler can
// leave nil is `T[] | null`, a map is a Record, and a time is a string
// (RFC 3339).

// ---------------------------------------------------------------------------
// Brains: GET /api/brains, /api/brains/{id}, and the manifest (brain.json)

/** How a brain's group messages that do not address it are handled. */
export type Observe = 'history' | 'run';

/** Whether a brain's request_approval asks you (ask) or answers approved at once (auto). */
export type ApprovalsMode = 'ask' | 'auto';

/** Which chats a brain answers (manifest.Routing). */
export interface Routing {
  handles_dms: boolean;
  default: boolean;
  groups: string[];
  /** In the brain's groups, whether a file addresses the bridge; unset is true. */
  answer_files?: boolean;
  /** In the brain's groups, whether a forwarded message addresses the bridge; unset is true. */
  answer_forwards?: boolean;
}

/** Where a brain's folder is backed up (manifest.Backup). */
export interface BrainBackup {
  enabled: boolean;
  /** rclone filter globs relative to the brain folder. */
  exclude: string[];
  /** Which targets get the folder, by "cloud" and "node:<target>"; unset is the cloud only. */
  targets?: Record<string, boolean>;
  /** Paths that go to the node targets but never to the cloud. */
  node_only?: string[];
}

/** The Google account a brain signed in with and the calendars it may use. */
export interface BrainCalendar {
  account: string;
  calendars: string[];
  default: string;
}

/** How a brain's memory index serves its runs (manifest.Memory). */
export interface BrainMemory {
  inject?: boolean;
  top_k?: number;
  max_chars?: number;
  /** When the brain consolidates its notes, "weekly sun 18:00" or "daily 07:00"; unset is never. */
  consolidate?: string;
}

/** The GitHub access a scheduled agent's runs are granted (chat.GitHubGrant). */
export interface GitHubGrant {
  repos: string[];
  permissions?: Record<string, string>;
}

/** A program a scheduled agent runs on a node instead of a brain run. */
export interface ScheduleCommand {
  /** The node's id or name. */
  node: string;
  argv: string[];
  dir?: string;
}

/** A scheduled agent as brain.json writes it (manifest.Schedule). */
export interface Schedule {
  name: string;
  /** "every 6h", "daily 07:00", "weekly mon 09:00" or "cron …", in the bridge's time zone. */
  when: string;
  /** Unset is true. */
  enabled?: boolean;
  description?: string;
  prompt?: string;
  prompt_file?: string;
  template?: string;
  command?: ScheduleCommand;
  vars?: Record<string, string>;
  /** always, worth (the default) or silent. */
  report?: string;
  chat?: string;
  /** read, write (the default) or irreversible. */
  risk?: string;
  /** Overrides the brain's list for these runs; [] allows every tool. */
  disallowed_tools?: string[];
  /** low, medium, high, xhigh or max; left out, the brain's. */
  effort?: string;
  /** In place of run_timeout_minutes, shorter or longer. */
  timeout_minutes?: number;
  /** run (the default) or skip. */
  missed?: string;
  jitter_minutes?: number;
  preapproved?: string[];
  github?: GitHubGrant;
}

/** Where a brain's runs happen: Fridolin's own sandbox, or a Docker Sandbox (docs/SANDBOXES.md). */
export type SandboxRuntime = 'bubblewrap' | 'docker-sandbox';

/** What a docker-sandbox brain does while its sandbox is not available. */
export type SandboxFallback = 'bubblewrap' | 'refuse';

/** A brain's own sandbox settings (brain.json "sandbox"). */
export interface BrainSandbox {
  /** Overrides config.json's sandbox.runtime; unset inherits it. */
  runtime?: SandboxRuntime;
  /** bubblewrap (the default) or refuse. */
  fallback?: SandboxFallback;
  /** Extra hosts the brain may reach beyond its provider and the core's API, in sbx's host pattern syntax; at most 50. */
  egress?: string[];
}

/** A brain's brain.json (manifest.Manifest), normalized. */
export interface BrainManifest {
  version: number;
  id: string;
  name: string;
  routing: Routing;
  model?: string;
  effort?: string;
  /** Overrides the global list; unset inherits it, [] allows all. */
  disallowed_tools?: string[];
  prompt_file?: string;
  /** Where replies to files handed in through /ingest go. */
  reply_chat?: string;
  observe?: Observe;
  approvals?: ApprovalsMode;
  backup: BrainBackup;
  calendar?: BrainCalendar;
  /** "core" for the brain that may reach the others through its grants. */
  role?: string;
  /** On the core brain: what it may do with each other brain, by id (delegate, read, files). */
  grants?: Record<string, string[]>;
  memory?: BrainMemory;
  schedules?: Schedule[];
  sandbox?: BrainSandbox;
}

/** Why a configured brain needs attention. */
export type IssueKind = 'drift' | 'missing' | 'invalid' | 'unapproved';

/** A problem with one configured brain (registry.Issue). */
export interface BrainIssue {
  id?: string;
  path: string;
  kind: IssueKind;
  detail: string;
}

/** The run a brain is working on. */
export interface RunView {
  chat: string;
  sender: string;
  preview: string;
  silent: boolean;
  started: string;
  pid: number;
}

/** A message waiting for its brain. */
export interface QueuedView {
  chat: string;
  sender: string;
  preview: string;
  silent: boolean;
  queued: string;
}

/** How a brain's run ended; question means it asked and is waiting. */
export type RunOutcome = 'ok' | 'error' | 'interrupted' | 'question';

/** A brain's last run. */
export interface RunInfo {
  chat: string;
  finished: string;
  seconds: number;
  outcome: RunOutcome;
  error?: string;
}

/** A running brain's live state (brain.Runtime): an entry of Status.brains. */
export interface BrainRuntime {
  id: string;
  name: string;
  dir: string;
  routing: Routing;
  model: string;
  effort: string;
  running: RunView | null;
  queue: QueuedView[];
  last: RunInfo | null;
  backup: BrainBackup;
  manifest: BrainManifest;
}

/** When an intent with a time fires (trigger.Schedule). */
export interface IntentSchedule {
  kind: 'once' | 'daily' | 'weekly';
  /** A one-time intent's time. */
  at?: string;
  /** A weekly intent's day: mon, tue, …, sun. */
  day?: string;
  /** HH:MM, for daily and weekly. */
  time?: string;
  every?: string;
  cron?: string;
}

/** A trigger keyword: a plain string matches anywhere. */
export type Trigger = string | { text: string; match?: 'anywhere' | 'start' | 'word' };

/** A brain's standing intent (intent.Intent). */
export interface Intent {
  id: string;
  text: string;
  /** An intent has schedule or match, not both. */
  schedule?: IntentSchedule;
  match?: Trigger[];
  repeat?: boolean;
  chat: string;
  created_by: string;
  created: string;
  last_fired?: string;
  fired?: number;
}

/**
 * Where a brain's next run happens: a runtime, sandbox-exec (a native
 * Mac's own sandbox, Fridolin's sandbox there) or refused (its fallback is
 * refuse while its Docker Sandbox is not ready).
 */
export type EffectiveRuntime = SandboxRuntime | 'sandbox-exec' | 'refused';

/**
 * Where a brain's runs happen and why (httpapi.BrainSandbox), from a look
 * at the node at most 30 s old.
 */
export interface BrainSandboxRuntime {
  /** What its brain.json, or else config.json, chose. */
  configured: SandboxRuntime;
  /** What the next run would use. */
  effective: EffectiveRuntime;
  fallback: SandboxFallback;
  /** The node whose Docker Sandboxes run it. */
  node: string;
  /** Its Docker Sandbox can run it now. */
  ready: boolean;
  /** Why it is not ready, in words. */
  reason: string;
  egress: string[];
}

/** One entry of GET /api/brains, and GET /api/brains/{id}. */
export interface Brain {
  /** "" while the entry has no approved manifest. */
  id: string;
  name: string;
  path: string;
  /** ok, or the kind of its worst issue. */
  status: 'ok' | IssueKind;
  /** The approved manifest; null for an entry that has none. */
  manifest: BrainManifest | null;
  /** brain.json on disk, where it differs from the approved one. */
  disk_manifest?: BrainManifest;
  issues: BrainIssue[];
  /** Left out for a brain that does not run. */
  runtime?: BrainRuntime;
  /** Where its runs happen. */
  sandbox?: BrainSandboxRuntime;
  /** The brain that takes the chats no other brain claims. */
  fallback: boolean;
  intents?: Intent[];
}

// ---------------------------------------------------------------------------
// GET /api/status

/** The WhatsApp link's state: disabled is dev mode. */
export type WhatsAppMode =
  | 'disabled' | 'connecting' | 'pairing' | 'qr-expired' | 'connected' | 'disconnected'
  | 'logged-out' | 'replaced' | 'outdated' | 'banned' | 'failed';

export interface WhatsAppStatus {
  mode: WhatsAppMode;
  own_number?: string;
  detail?: string;
  since: string;
  /** A pairing code is waiting: GET /api/whatsapp/qr has it. */
  qr_available: boolean;
}

/** Whether the agent CLI was found (claudecli.Status). */
export interface AgentCLIStatus {
  path: string;
  found: boolean;
  version?: string;
}

export interface SandboxStatus {
  enabled: boolean;
  supported: boolean;
}

/** The Drive backup in short, as the Overview shows it (backup.Summary). */
export interface BackupSummary {
  connected: boolean;
  enabled: boolean;
  running: boolean;
  /** What runs is a restore, not a backup. */
  restoring?: boolean;
  last_ok?: string;
  last_error?: string;
  /** Whose backup last_error is: a brain's id, or the bridge's settings. */
  last_error_brain?: string;
  next_run?: string;
  /** The vault's snapshots on Drive took the daily run's place. */
  superseded?: boolean;
}

/** The core's tailnet node. */
export interface TailnetStatus {
  state: 'starting' | 'up' | 'error';
  hostname: string;
  dns_name?: string;
  ips?: string[];
  magic_dns: boolean;
  https: boolean;
  /** Where the dashboard answers on the tailnet. */
  url?: string;
  error?: string;
}

/** The core lease as this bridge sees it. */
export interface LeaseStatus {
  /** held (this device renews it), unverified (the store did not answer) or lost. */
  state: 'held' | 'unverified' | 'lost';
  device_id: string;
  hostname?: string;
  renewed_at?: string;
  /** The device that took it. */
  holder?: string;
  error?: string;
}

export interface CoreStatus {
  tailnet?: TailnetStatus;
  lease?: LeaseStatus;
}

/** GET /api/status. */
export interface Status {
  version: string;
  started_at: string;
  uptime_s: number;
  config_path: string;
  data_dir: string;
  dev_mode: boolean;
  /** runtime.GOOS: darwin, linux, … */
  platform: string;
  whatsapp: WhatsAppStatus;
  claude: AgentCLIStatus;
  agent_provider: string;
  /** Only in Codex mode. */
  codex?: AgentCLIStatus;
  sandbox: SandboxStatus;
  brains: BrainRuntime[];
  issues: BrainIssue[];
  warnings: string[];
  backup: BackupSummary;
  ingest: boolean;
  /** Messages held back to be merged with the next (debounce). */
  debouncing: number;
  /** Settings saved since the start that only apply after a restart. */
  restart_required: string[];
  /** While core.tailnet or core.lease is on. */
  core?: CoreStatus;
}

// ---------------------------------------------------------------------------
// GET /api/config

export interface SandboxConfig {
  enabled: boolean;
  deny_paths: string[] | null;
  allow_paths: string[] | null;
  /** read (the default) or deny. */
  git_config?: string;
  credentials_dir?: string;
  /** The default for every brain; bubblewrap when unset. */
  runtime?: SandboxRuntime;
  /** The node, by id or name, whose sandbox capability runs the brains; "" for the one on the core's machine. */
  node?: string;
}

export interface QueueConfig {
  /** queue (the default) or interrupt. */
  mode?: string;
  max_pending?: number;
  notify?: boolean;
}

export interface DebounceConfig {
  quiet_ms?: number;
  max_wait_ms?: number;
}

export interface IngestConfig {
  enabled: boolean;
}

export interface StartupChecksConfig {
  required_files?: string[];
  notify_whatsapp?: boolean;
  notify_jid?: string;
  hint?: string;
}

export interface BackupConfig {
  enabled: boolean;
  remote?: string;
  root?: string;
  /** HH:MM. */
  daily_at?: string;
  rclone_path?: string;
  keep_versions_days?: number;
  include_bridge_config?: boolean;
  include_secret_files?: boolean;
}

export interface PlaudConfig {
  enabled: boolean;
  brain?: string;
  cli_path?: string;
  interval_seconds?: number;
  polished?: boolean;
  message?: string;
  brain_access?: boolean;
  phone_dir?: string;
  phone_upload?: boolean;
  /** The app may ask by voice (docs/PLAUD.md): a spoken question goes to voice_brain, its answer back to the phone. */
  voice?: boolean;
  /** Answers voice questions; "" for the brain that handles DMs, else the default brain. */
  voice_brain?: string;
  /** The answer goes to the phone only, not also to the brain's private chat. */
  voice_quiet?: boolean;
}

export interface DashboardConfig {
  disabled?: boolean;
  require_token?: boolean;
}

export interface ScriptAPIConfig {
  tokenless_send?: boolean;
}

export interface CoreConfig {
  tailnet: { enabled: boolean; hostname?: string };
  lease: { enabled: boolean; renew_seconds?: number; stale_seconds?: number };
  memory_limit_gb?: number;
}

/** How many snapshots of each age a node target keeps. */
export interface TargetRetention {
  hourly: number;
  daily: number;
  weekly: number;
  monthly: number;
}

/** A node target of the vault (config.VaultTarget). */
export interface VaultTargetConfig {
  name: string;
  kind: string;
  node: string;
  folder: string;
  nightly_at?: string;
  hourly?: boolean;
  include_data_dir?: boolean;
  retention?: TargetRetention;
  check_days?: number;
  read_data_days?: number;
  window_hours?: number;
}

export interface VaultConfig {
  enabled: boolean;
  hot_budget_gb?: number;
  activity_days?: number;
  cold_after_days?: number;
  headroom_percent?: number;
  bucket?: string;
  snapshots_bucket?: string;
  endpoint?: string;
  b2_daily_budget?: number;
  targets?: VaultTargetConfig[];
}

export interface NodesConfig {
  enabled: boolean;
  listen?: string;
  /** By node id or name, the git identities that node's FrIDE gets; a node not named gets all. */
  git_identities?: Record<string, GitIdentityID[]>;
}

export interface PolicyConfig {
  max_runs_per_hour?: number;
  max_pending_irreversible?: number;
  approval_timeout_hours?: number;
}

export interface HousekeepingConfig {
  enabled?: boolean;
  every_hours?: number;
  dry_run?: boolean;
  cpu_percent?: number;
  rss_mb?: number;
  min_age_minutes?: number;
}

export interface SchedulesConfig {
  enabled?: boolean;
  jitter_minutes?: number;
  keep_days?: number;
  keep_runs?: number;
  max_total_mb?: number;
  max_log_kb?: number;
}

export interface ConnectionConfig {
  brains?: string[];
  brain_monthly_usd?: number;
  /** ElevenLabs only: the default voice id. */
  voice?: string;
}
export interface ConnectionsConfig {
  openai?: ConnectionConfig;
  elevenlabs?: ConnectionConfig;
}

/**
 * config.json (config.File), less brain_dirs, which GET /api/config never
 * sends: brains have their own endpoints. In `effective` the defaults are
 * filled in, so most optional fields are there.
 */
export interface ConfigFile {
  _comment?: string;
  version?: number;
  allowed_numbers: string[] | null;
  allow_any_dm?: boolean;
  allowed_groups: string[] | null;
  /** Who in an allowed group reaches its brain, by group JID; ["*"] is every member. */
  group_members?: Record<string, string[]>;
  triggers: Trigger[];
  claude_path: string;
  agent_provider?: string;
  codex_path?: string;
  codex_model?: string;
  codex_effort?: string;
  data_dir: string;
  db_path?: string;
  media_dir?: string;
  max_chunk_size?: number;
  max_media_mb?: number;
  brain_model: string;
  brain_effort?: string;
  disallowed_tools?: string[];
  dangerously_skip_permissions: boolean;
  run_timeout_minutes?: number;
  max_concurrent_runs?: number;
  inherit_claude_user_config?: boolean;
  child_env_passthrough?: string[];
  http_listen: string;
  log_level?: string;
  /** Always true: classic mode was removed. */
  brain_mode: true;
  sandbox: SandboxConfig;
  otp_guard: boolean;
  otp_markers?: string[];
  viewer: boolean;
  queue: QueueConfig;
  debounce: DebounceConfig;
  ingest: IngestConfig;
  startup_checks: StartupChecksConfig;
  backup: BackupConfig;
  plaud: PlaudConfig;
  dashboard: DashboardConfig;
  script_api: ScriptAPIConfig;
  core: CoreConfig;
  vault: VaultConfig;
  nodes: NodesConfig;
  policy: PolicyConfig;
  housekeeping: HousekeepingConfig;
  schedules: SchedulesConfig;
  connections?: ConnectionsConfig;
  /** The IANA zone schedules are read in; left out for the bridge's own. */
  timezone?: string;
}

/** GET /api/config: the file and what is in effect, with the etag a PUT sends back. */
export interface ConfigResponse {
  config: ConfigFile;
  effective: ConfigFile;
  etag: string;
  path: string;
}

// ---------------------------------------------------------------------------
// Chats and WhatsApp

/** One entry of GET /api/chats/ignored: a chat the bridge heard from but does not answer. */
export interface IgnoredChat {
  jid: string;
  kind: 'dm' | 'group';
  name: string;
  sender: string;
  push_name: string;
  count: number;
  first_seen: string;
  last_seen: string;
}

/** One entry of GET /api/whatsapp/groups. */
export interface WhatsAppGroup {
  jid: string;
  name: string;
  participants: number;
  allowed: boolean;
  /** The brain an allowed group goes to, "" for none. */
  brain: string;
}

/** GET /api/whatsapp/qr (404 while there is no pairing code). */
export interface WhatsAppQR {
  created_at: string;
  /** A data: URL of the code's PNG. */
  data_url: string;
}

// ---------------------------------------------------------------------------
// GET /api/nodes

export interface ExecRule {
  name: string;
  /** Regular expressions each argument must match one of; null allows any arguments, [] none. */
  args: string[] | null;
  /** args are the arguments themselves, not patterns. */
  exact?: boolean;
  /** The core added it, for an approved agent's program (exact, an absolute path). */
  core?: boolean;
}

export interface ExecCap {
  allow: ExecRule[];
  /** Folders the node runs programs from that the core adds for its approved agents. */
  accept_from_core?: string[];
  timeout_seconds?: number;
  max_output_bytes?: number;
}

export interface FSRoot {
  path: string;
  write?: boolean;
}

export interface FSCap {
  roots: FSRoot[];
}

export interface FrideCap {
  url: string;
  repos?: string[];
}

export interface AndroidBuildCap {
  java?: string;
  platforms?: string[];
}

export interface BackupCap {
  roots?: string[];
  folders?: string[];
}

export interface UpdateCap {
  box?: boolean;
}

export interface WorkstationCap {
  roots: string[];
  allow_home?: boolean;
  providers?: string[];
  /** sandbox-exec, bwrap or none. */
  sandbox?: string;
}

/** What a node offers, or what the owner approved of it (nodeproto.Capabilities). */
export interface NodeCapabilities {
  exec?: ExecCap;
  fs?: FSCap;
  host?: boolean;
  notify?: boolean;
  fride?: FrideCap;
  android_build?: AndroidBuildCap;
  backup?: BackupCap;
  update?: UpdateCap;
  agent?: WorkstationCap;
  terminal?: WorkstationCap;
  core_host?: CoreHostCap;
  sandbox?: SandboxCap;
}

/** Docker Sandboxes (sbx) on the node: brains' runs in microVMs (nodeproto.SandboxCap). */
export interface SandboxCap {
  /** sbx version. */
  version: string;
  signed_in: boolean;
  /** sandboxd answers. */
  daemon: boolean;
  /** Fridolin's global settings hold. */
  settings: boolean;
  /** The template name fridolin made, "" before setup. */
  template?: string;
  /** "claude", "codex": those with a usable login. */
  providers?: string[];
  /** Why it is not ready, plain. */
  problem?: string;
  /** The version of the linux fridolin the sandboxes mount, "" before the core sent one. */
  bin?: string;
}

/** The machine may take over the core (nodeproto.CoreHostCap; docs/NODES.md, "core-host"). */
export interface CoreHostCap {
  /** A node in a box. */
  box?: boolean;
  /** SHA-256 of the node's core-host.json, hex. */
  hash: string;
  /** The device ids it pins. */
  send_to?: string[];
}

/** How the fleet shows a core host (nodes.CoreHostView). */
export interface CoreHostView {
  /** The owner confirmed it: their vote on a move to it. */
  confirmed: boolean;
  /** The device key's, to compare with `fridolin node core-host show`. */
  fingerprint: string;
  send_to: string[];
  /** What moving the core there means, in words. */
  approval: string;
}

/** A program the core lists for a node (nodeproto.ExecProgram). */
export interface ExecProgram {
  name: string;
  args: string[];
  /** The approved agents that run it, as brain/name. */
  agents?: string[];
}

/** How the core's last list of programs for a node went (nodes.ProgramsStatus). */
export interface ProgramsStatus {
  at: string;
  sent: ExecProgram[];
  accepted: ExecProgram[];
  refused: (ExecProgram & { why: string })[];
  /** Why the list did not reach the node or was refused whole. */
  error?: string;
}

export interface HostStats {
  cpus: number;
  /** Bytes. */
  mem_total?: number;
  mem_available?: number;
  disk_free?: number;
  /** Percent. */
  battery?: number;
}

/** The node's auto-update policy and how the last update went. */
export interface NodeUpdateStatus {
  auto?: boolean;
  /** sending, restarting, ok or failed. */
  state?: string;
  from?: string;
  to?: string;
  error?: string;
  failed_version?: string;
  started_at?: string;
  finished_at?: string;
  /** When the core gives up waiting for the node to come back. */
  wait_until?: string;
}

export type NodeDevice = 'pending' | 'approved' | 'revoked';
export type NodeCommands = 'none' | 'pending' | 'approved';

/** One node of GET /api/nodes (nodes.View). */
export interface Node {
  id: string;
  name: string;
  os: string;
  arch: string;
  version: string;
  /** Base64. */
  public_key: string | null;
  tags: string[] | null;
  device: NodeDevice;
  declared: NodeCapabilities;
  approved: NodeCapabilities;
  joined_at: string;
  device_approved_at?: string;
  commands_approved_at?: string;
  revoked_at?: string;
  /** Where its join code came from: chat (!join) or dashboard; left out when neither. */
  joined_via?: 'chat' | 'dashboard';
  last_seen?: string;
  address?: string;
  resources?: HostStats;
  update?: NodeUpdateStatus;
  /** The second approval stage. */
  commands: NodeCommands;
  /** What the node may serve now. */
  effective: NodeCapabilities;
  online: boolean;
  /** The core's version, when the node runs an older one and can be updated to it. */
  update_to?: string;
  /** Why the node cannot be updated. */
  update_note?: string;
  core_host?: CoreHostView;
  /** What approving what it offers lets it do, one plain sentence each. */
  allows: string[];
  /** What approving fride means, while the node offers a fride that waits for approval. */
  fride_approval?: string;
  /** Left out when no list went out since the core started. */
  programs?: ProgramsStatus;
}

/** A drive or volume of a node (nodeproto.FSVolume). */
export interface NodeVolume {
  /** Its root: C:\, /, /Volumes/Backup. */
  path: string;
  label?: string;
  /** Bytes left; left out where unknown. */
  free?: number;
  /** An external or network disk that may be away. */
  removable?: boolean;
}

/** The result of the node call fs.roots (nodeproto.FSRootsResult). */
export interface NodeFSRoots {
  /** The node's path separator, "\\" or "/". */
  sep: string;
  roots: NodeVolume[];
}

/** The result of the node call fs.browse (nodeproto.BrowseResult): a folder's folders, by name. */
export interface NodeFSBrowse {
  path: string;
  /** The folder above; left out at a volume's root. */
  parent?: string;
  folders: string[];
  /** The folder holds a restic repository already. */
  repo?: boolean;
  /** More folders than a listing holds. */
  truncated?: boolean;
}

/** GET /api/nodes. */
export interface NodesResponse {
  /** nodes.enabled as the bridge started. */
  enabled: boolean;
  /** The node port it serves, "" while it is not open. */
  listen: string;
  /** The core's version. */
  version: string;
  nodes: Node[];
}

// ---------------------------------------------------------------------------
// Activity, threads, audit, approvals, policy, skills

/** One entry of GET /api/activity, newest first. */
export interface ActivityEntry {
  ts: string;
  brain: string;
  chat: string;
  from: string;
  type: 'in' | 'out' | 'event';
  text: string;
}

/** A run of a thread. */
export interface ThreadRun {
  run: string;
  brain: string;
  chat: string;
  started: string;
  skill: string;
  risk: string;
  detail: string;
  /** The run_end's outcome, running, or "refused: …". */
  outcome: string;
  duration_ms: number;
}

/** One entry of GET /api/threads: a chat's runs, newest first. */
export interface Thread {
  thread: string;
  last: string;
  count: number;
  brains: string[];
  runs: ThreadRun[];
}

/** A piece of FrIDE's news about a thread, whole (GET /api/fride). */
export interface FrideNewsItem {
  kind: string;
  text: string;
  at: string;
}

/** One FrIDE thread of GET /api/fride, the newest first. */
export interface FrideThread {
  thread: number;
  state: string;
  chat: string;
  brain: string;
  message: string;
  node?: string;
  task?: string;
  branch?: string;
  fride_status?: string;
  archive?: string;
  updated: string;
  news: FrideNewsItem[];
}

/** GET /api/fride. */
export interface FrideResponse {
  threads: FrideThread[];
}

/** One line of GET /api/audit, newest first (audit.Entry). */
export interface AuditEntry {
  ts: string;
  kind: string;
  thread?: string;
  chat?: string;
  brain?: string;
  run?: string;
  /** The approval's, question's or delegation's id. */
  id?: string;
  skill?: string;
  risk?: string;
  /** Who acted: a voter's number, "dashboard", a brain. */
  by?: string;
  detail?: string;
  outcome?: string;
  duration_ms?: number;
}

/** An approval or a question a run asked (store.Approval). */
export interface Approval {
  id: string;
  kind: 'approval' | 'question';
  brain: string;
  /** The brain that hears the answer. */
  notify: string;
  run: string;
  thread: string;
  chat: string;
  action: string;
  details: string;
  risk: string;
  skill: string;
  step: string;
  options: string[];
  /** pending, approved, denied, answered or expired. */
  status: string;
  answer: string;
  decided_by: string;
  used: boolean;
  created_at: string;
  expires_at: string;
  decided_at: string | null;
}

/** GET /api/approvals. */
export interface Approvals {
  /** Oldest first. */
  pending: Approval[];
  /** The latest 100 decided, newest first. */
  history: Approval[];
}

/** GET /api/policy. */
export interface PolicyStatus {
  lockdown: boolean;
  lockdown_by: string;
  lockdown_since: string | null;
  pending_approvals: number;
  pending_questions: number;
  max_runs_per_hour: number;
  max_pending_irreversible: number;
  approval_timeout_hours: number;
}

export type Risk = 'read' | 'write' | 'irreversible';

/** What a skill declares under metadata.fridolin, or what was approved of it. */
export interface SkillPolicy {
  risk: Risk;
  vault: string[];
  approve: string[];
  triggers: string[];
}

/**
 * approved, changed (the approved policy keeps applying), new, starter,
 * legacy (no frontmatter) or invalid.
 */
export type SkillStatus = 'approved' | 'changed' | 'new' | 'starter' | 'legacy' | 'invalid';

/** A skill file of a brain. */
export interface Skill {
  /** skills/<name>.md or skills/<name>/SKILL.md. */
  path: string;
  name: string;
  description: string;
  requires: { env: string[]; bins: string[] };
  os: string[];
  install: Record<string, string>[];
  /** The policy the file declares. */
  fridolin: SkillPolicy;
  frontmatter: boolean;
  status: SkillStatus;
  error?: string;
  /** Identifies the declared policy, for approving it as reviewed. */
  hash: string;
  /** The policy that applies. */
  effective: SkillPolicy;
  approved: SkillPolicy | null;
}

/** GET /api/brains/{id}/skills. */
export interface BrainSkills {
  skills: Skill[];
  risks: Risk[];
}

// ---------------------------------------------------------------------------
// Backup: GET /api/backup/status, /api/brains/{id}/backup/versions

/** How one backup run went (backup.Result). */
export interface BackupResult {
  started: string;
  finished: string;
  ok: boolean;
  error?: string;
  bytes: number;
  files: number;
  seconds: number;
  version: string;
  trigger: 'manual' | 'scheduled';
  /** Stopped because it would have mirrored a mass deletion. */
  refused?: boolean;
}

export interface BackupBrainStatus {
  id: string;
  name: string;
  dir: string;
  enabled: boolean;
  exclude: string[];
  last: BackupResult | null;
}

/** The Google client a Drive sign-in uses. */
export interface BackupClientStatus {
  /** drive (pasted for Drive alone), google (Settings → Google sign-in) or builtin (the build's own). */
  source: string;
  type: string;
  client_id_hint: string;
}

/** A Drive sign-in under way or just over. */
export interface BackupConnectJob {
  state: 'running' | 'done' | 'error';
  /** What a running sign-in waits for: signin or folder. */
  step?: string;
  auth_url?: string;
  error?: string;
  reconnect?: boolean;
}

/** The backup or restore that runs. */
export interface BackupJob {
  /** A restore's brain. */
  brain?: string;
  current: string;
  done: string[];
  kind: string;
  queue: string[];
  started: string;
  /** A restore's target folder. */
  target?: string;
}

export interface RestoreResult {
  brain: string;
  version: string;
  target: string;
  started: string;
  finished: string;
  ok: boolean;
  error?: string;
  bytes: number;
  files: number;
}

export interface RcloneStatus {
  error?: string;
  found: boolean;
  install_hint: string;
  path?: string;
  version?: string;
}

/** The backup settings in effect (backup.Settings). */
export interface BackupSettings {
  enabled: boolean;
  remote?: string;
  root?: string;
  daily_at?: string;
  rclone_path?: string;
  keep_versions_days: number;
  include_bridge_config: boolean;
  include_secret_files?: boolean;
}

/** GET /api/backup/status. */
export interface BackupStatus {
  brains: BackupBrainStatus[];
  /** The last backup of the bridge's own settings. */
  bridge: BackupResult | null;
  client: BackupClientStatus | null;
  connect: BackupConnectJob | null;
  connected: boolean;
  drive_folder?: string;
  include_bridge_config: boolean;
  job: BackupJob | null;
  last_restore: RestoreResult | null;
  next_run?: string;
  rclone: RcloneStatus;
  redirect_uri: string;
  /** The files always left out of brain folders. */
  secret_excludes: string[];
  settings: BackupSettings;
  /** The link uses rclone's shared Google client, which Google retires. */
  shared_client: boolean;
}

/** GET /api/brains/{id}/backup/versions. */
export interface BackupVersions {
  brain: string;
  versions: string[];
}

// ---------------------------------------------------------------------------
// GET /api/vault/status

export interface VaultFileCounts {
  hot: number;
  cold: number;
  both: number;
}

export interface VaultSyncRecord {
  at?: string;
  uploaded: number;
  uploaded_bytes: number;
  failed: number;
  error?: string;
}

export interface VaultEvictRecord {
  at?: string;
  evicted: number;
  freed_bytes: number;
  over_budget: boolean;
  error?: string;
}

/** The folder the vault last forgot. */
export interface VaultForgetRecord {
  at?: string;
  prefix: string;
  forgotten: number;
  bytes: number;
  orphans?: number;
  kept?: number;
  failed?: number;
  error?: string;
}

export interface VaultReach {
  at?: string;
  ok: boolean;
  error?: string;
}

/** A file the vault moved out of the hot tier. */
export interface VaultEviction {
  path: string;
  size: number;
  reason: string;
  at: string;
}

export interface SnapshotRecord {
  at?: string;
  kind: string;
  id?: string;
  files: number;
  bytes_added: number;
  error?: string;
}

export interface CheckRecord {
  at?: string;
  read_data_subset?: string;
  ok: boolean;
  error?: string;
}

/** When a job last ran and how it ended. */
export interface VaultRecord {
  at?: string;
  error?: string;
}

/** A snapshot that found most files gone: pruning waits for the owner. */
export interface DeletionHold {
  since: string;
  snapshot: string;
  kind: string;
  prev_files: number;
  files: number;
}

export interface DriveSnapshotsStatus {
  enabled: boolean;
  repo?: string;
  completed_once: boolean;
  last_copy?: VaultRecord;
}

/** A brain folder the snapshots and the cold tier leave out whole, and why. */
export interface SkippedBrain {
  id: string;
  dir: string;
  skip?: string;
  unreadable?: boolean;
}

export interface SnapshotsStatus {
  enabled: boolean;
  repo?: string;
  error?: string;
  last?: SnapshotRecord;
  last_hourly?: SnapshotRecord;
  last_nightly?: SnapshotRecord;
  last_check?: CheckRecord;
  last_read_data?: CheckRecord;
  last_prune?: VaultRecord;
  deletion_hold?: DeletionHold;
  drive: DriveSnapshotsStatus;
  skipped_brains?: SkippedBrain[];
}

/** The last upload of Litestream's local replicas through the mirror. */
export interface MirrorStatus {
  at?: string;
  uploaded: number;
  deleted: number;
  files: number;
  error?: string;
}

export interface LitestreamStatus {
  enabled: boolean;
  running: boolean;
  pid?: number;
  since?: string;
  restarts: number;
  last_error?: string;
  last_exit?: string;
  last_problem?: string;
  last_problem_at?: string;
  repairs?: number;
  last_repair?: string;
  dbs?: string[];
  mirror?: MirrorStatus;
  /** When the vault last copied Litestream's state in. */
  at?: string;
}

export interface DrillCheck {
  name: string;
  ok: boolean;
  detail?: string;
}

/** The last restore drill. */
export interface DrillRecord {
  at?: string;
  ok: boolean;
  snapshot?: string;
  detail?: string;
  checks?: DrillCheck[];
}

/** An off-machine store's layers of encryption. */
export interface StoreLayers {
  store: string;
  where: string;
  layers: string[];
  keys: string[];
  /** A layer's key exchange is post-quantum. */
  pq?: boolean;
  /** copying while the single-layer copy of before is there, retired once deleted. */
  old?: string;
  left?: string;
}

export interface MigrationStore {
  store: string;
  action: string;
  pending: number;
  copied: number;
  retired?: boolean;
  left?: string;
  error?: string;
}

/** What fridolin vault migrate-encryption last did or would do. */
export interface EncryptionMigration {
  at: string;
  dry_run: boolean;
  delete_old: boolean;
  running?: boolean;
  stores: MigrationStore[] | null;
  ok: boolean;
}

/** The bridge's Backblaze B2 transactions today. */
export interface B2Usage {
  day: string;
  budget: number;
  /** Today's counts by class. */
  classes: Record<string, number>;
  /** The share of the budget, by class. */
  percent: Record<string, number>;
  /** Counts by class and operation ("C lease:authorize"). */
  ops?: Record<string, number>;
  cap_exceeded?: string;
  cap_error?: string;
  yesterday?: Record<string, number>;
  warnings?: string[];
}

/** A node target as the status shows it. */
export interface VaultTarget {
  name: string;
  kind: string;
  node: string;
  node_name?: string;
  folder: string;
  nightly_at: string;
  hourly: boolean;
  include_data_dir: boolean;
  retention: TargetRetention;
  window_hours: number;
  online: boolean;
  /** The node may serve the folder now; problem says why not. */
  ready: boolean;
  problem?: string;
  /** The brains whose folders go to the target. */
  brains: string[];
  job?: string;
  last_hourly?: SnapshotRecord;
  last_nightly?: SnapshotRecord;
  last?: SnapshotRecord;
  last_check?: CheckRecord;
  last_read_data?: CheckRecord;
  last_prune?: VaultRecord;
  last_drill?: DrillRecord;
  deletion_hold?: DeletionHold;
  size?: number;
  files?: number;
  free?: number;
  size_at?: string;
  /** restic (one layer, from before) or restic+crypt. */
  layers?: string;
}

/** A secret folder as the status shows it: its numbers, never its files. */
export interface SecretFolderView {
  path: string;
  /** sandbox.credentials_dir or vault.secret_dirs. */
  source: string;
  /** Its folder in a version and in a restore. */
  label: string;
  files: number;
  bytes: number;
  skipped?: number;
  error?: string;
}

/** A version of the secret folders taken, or tried. */
export interface SecretBackupRecord {
  at?: string;
  version?: string;
  /** first, daily, changed or asked. */
  reason: string;
  files: number;
  bytes: number;
  error?: string;
}

/** A version of the secret folders in the bucket. */
export interface SecretVersion {
  version: string;
  at: string;
  bytes: number;
}

/** The last restore of the secret folders. */
export interface SecretRestoreRecord {
  at?: string;
  version?: string;
  to?: string;
  force?: boolean;
  replaced?: string;
  folders?: SecretFolderView[];
  files: number;
  bytes: number;
  kept?: number;
  error?: string;
}

/** The secret folders' backups (docs/VAULT.md, Secret folders). */
export interface SecretFoldersStatus {
  enabled: boolean;
  error?: string;
  where?: string;
  keep_days: number;
  folders: SecretFolderView[] | null;
  last?: SecretBackupRecord;
  last_ok?: SecretBackupRecord;
  versions?: SecretVersion[];
  fingerprint?: string;
  prune_hold?: DeletionHold;
  last_prune?: VaultRecord;
  last_restore?: SecretRestoreRecord;
}

/** GET /api/vault/status. */
export interface VaultStatus {
  enabled: boolean;
  state: string;
  error?: string;
  root?: string;
  hot_bytes: number;
  budget_bytes: number;
  target_bytes: number;
  cold_bytes: number;
  files: VaultFileCounts;
  /** The job that runs. */
  job?: string;
  last_upload?: string;
  last_sync?: VaultSyncRecord;
  last_evict?: VaultEvictRecord;
  last_forget?: VaultForgetRecord;
  reachable?: VaultReach;
  evictions: VaultEviction[];
  snapshots: SnapshotsStatus;
  litestream: LitestreamStatus;
  last_drill?: DrillRecord;
  encryption?: StoreLayers[];
  encryption_migration?: EncryptionMigration;
  b2?: B2Usage;
  /** Left out while no secret folder is set. */
  secret_folders?: SecretFoldersStatus;
  /** Left out while there are none. */
  targets?: VaultTarget[];
  updated_at?: string;
  started_at?: string;
}

// ---------------------------------------------------------------------------
// Schedules: GET /api/schedules and an agent's runs

/** A run of a scheduled agent (sched.Run). */
export interface ScheduleRun {
  id: string;
  brain: string;
  agent: string;
  /** brain or command. */
  kind: string;
  trigger: string;
  /** Who asked for a manual run: dashboard or cli. */
  by?: string;
  started: string;
  ended?: string;
  duration_ms?: number;
  /** running, ok, nothing, failed, stopped, dropped, refused, skipped or interrupted. */
  status: string;
  result?: string;
  error?: string;
  sent?: boolean;
  sent_to?: string;
  cost_usd?: number;
  input_tokens?: number;
  output_tokens?: number;
  exit_code?: number;
  node?: string;
  log_bytes?: number;
}

/** A scheduled agent as the Schedules page lists it. */
export interface ScheduleAgent {
  brain: string;
  brain_name: string;
  name: string;
  /** brain or command. */
  kind: string;
  /** What it does, in a few words. */
  task: string;
  /** The report setting, its default filled in. */
  report: string;
  /** Where the results go, "" for nowhere. */
  chat: string;
  /** What the Schedules page shows it as. */
  state: 'pending' | 'active' | 'paused' | 'failing' | 'disabled';
  /** The schedule's setting. */
  enabled: boolean;
  /** The dashboard's pause. */
  paused: boolean;
  running: boolean;
  /** The agent memory.consolidate stands for. */
  from_memory?: boolean;
  /** null when it does not run on its schedule (pending, paused, disabled, schedules off, never again). */
  next: string | null;
  last_run: ScheduleRun | null;
  /** Why it cannot run as it is. */
  problem?: string;
  /** Where it is defined, relative to the brain folder: .brain/schedules/<name>.json, brain.json, or "" for memory.consolidate's. */
  file: string;
  /** In plain words, what it may do that deserves a careful look before it is approved. */
  risky: string[];
  /** Its change on disk that waits for the owner's approval; the approved version runs until then. */
  review?: ScheduleReview;
  /** Its definition, as the edit form takes it: the approved one, or for a pending agent the one on disk. */
  schedule: Schedule;
  /** How a command agent's program stands on its node. */
  program?: ScheduleProgram;
}

/** How a command agent's program stands on its node (app.programView). */
export interface ScheduleProgram {
  node?: string;
  node_name?: string;
  state: 'allowed' | 'waiting' | 'refused' | 'not-accepted' | 'no-node';
  /** In words: allowed, what approving it does, or why the node will not run it. */
  detail: string;
  /** Who allows it: "node" (node.json) or "core" (added for the approved agents); "" when it is not allowed. */
  by?: '' | 'node' | 'core';
}

/** A scheduled agent whose definition on disk differs from the approved one (manifest.ScheduleReview). */
export interface ScheduleReview {
  name: string;
  status: 'new' | 'changed' | 'removed' | 'invalid';
  /** Where it is on disk: its file, or brain.json. */
  source: string;
  disk?: Schedule;
  approved?: Schedule;
  detail?: string;
}

/** A standing intent with a time. */
export interface Reminder {
  brain: string;
  id: string;
  text: string;
  when: string;
  chat: string;
  next: string | null;
  /** When it was set; left out when the brain's file does not say. */
  created?: string;
  /** "bridge" for a standing intent, which the dashboard can remove, else the brain's file that holds it. */
  source: string;
}

export interface ScheduleTemplateVar {
  name: string;
  default?: string;
  /** No default: a schedule must set it. */
  required: boolean;
}

/** A built-in task a schedule can run. */
export interface ScheduleTemplate {
  name: string;
  description: string;
  when?: string;
  report?: string;
  risk?: string;
  /** The GitHub permissions a schedule of it needs. */
  github?: Record<string, string>;
  vars?: ScheduleTemplateVar[];
  optional: boolean;
}

/** GET /api/schedules. */
export interface SchedulesResponse {
  /** schedules.enabled. */
  enabled: boolean;
  /** The zone every time of day is in: timezone from the config ("config"), else the bridge's own ("process"). */
  timezone: { name: string; source: 'config' | 'process' };
  /** How many agents' definitions on disk wait for the owner's approval, new or changed. */
  pending: number;
  agents: ScheduleAgent[];
  reminders: Reminder[];
  templates: ScheduleTemplate[];
}

/** GET /api/schedules/{brain}/{name}/runs/{run}/log. */
export interface ScheduleRunLog {
  id: string;
  /** The log's last 4 MiB. */
  log: string;
  /** The log was longer. */
  cut: boolean;
  running: boolean;
}

// ---------------------------------------------------------------------------
// The site: GET /api/site and its parts

export interface SiteRepo {
  url: string;
  branch: string;
  /** The folder in the repository that holds modules/, "" for the root. */
  subdir: string;
}

export interface SiteDeploy {
  commit: string;
  subject: string;
  at: string;
}

/** A change a builder brain pushed for review. */
export interface SiteProposal {
  branch: string;
  brain: string;
  title: string;
  summary: string;
  commit: string;
  at: string;
}

export interface SiteSettings {
  enabled: boolean;
  title: string;
  listen: string;
  domain: string;
  repo: SiteRepo;
  roles: string[];
  /** The brains each role sees on the site's /files and /wiki. */
  role_brains: Record<string, string[]>;
  /** The brains each role may upload to on the site's /files, of those it sees. */
  role_uploads: Record<string, string[]>;
  /** The commit the live site serves. */
  approved: string;
  history: SiteDeploy[];
  tunnel: boolean;
  /** The brains allowed to build the site. */
  builders: string[];
  proposals: SiteProposal[];
}

export interface SiteDeploySnapshot {
  commit: string;
  state: 'running' | 'done' | 'error';
  step: string;
  error?: string;
  log: string[];
  started: string;
  finished?: string;
}

export interface SiteProcStatus {
  state: 'stopped' | 'starting' | 'running' | 'crashed';
  since?: string;
  error?: string;
  restarts: number;
  log: string[] | null;
}

/** GET /api/site. */
export interface SiteStatus {
  accounts: { admins: number; total: number };
  deploy: SiteDeploySnapshot | null;
  deploy_key?: string;
  google: { redirect_uri: string; source: string; type?: string };
  public_url?: string;
  repo: { connected: boolean; github: string };
  runtime: { cloudflared: boolean; gh: boolean; node: string; node_error?: string };
  settings: SiteSettings;
  site: {
    healthy: boolean;
    local_url: string;
    process: SiteProcStatus;
    sandbox: { on: boolean; note: string };
  };
  tunnel: { process: SiteProcStatus; token_saved: boolean };
}

/** A site account; the password hash is never sent. */
export interface SiteUser {
  id: string;
  kind: 'google' | 'password';
  email?: string;
  username?: string;
  name?: string;
  role: string;
  created: string;
}

/** GET /api/site/users. */
export interface SiteUsers {
  roles: string[];
  users: SiteUser[];
}

export interface SiteModuleSecrets {
  module: string;
  title: string;
  /** The module is in the live commit. */
  live: boolean;
  /** The names its live module.json lists. */
  declared: string[];
  /** The names with a value. */
  set: string[];
}

/** GET /api/site/secrets. */
export interface SiteSecrets {
  modules: SiteModuleSecrets[];
}

/** A security-relevant manifest field that changed. */
export interface ReviewItem {
  field: string;
  old: unknown;
  new: unknown;
  note: string;
}

/** A module that differs from the approved commit. */
export interface ModuleChange {
  id: string;
  status: 'added' | 'removed' | 'changed';
  review: ReviewItem[];
  files: string[];
}

/** A builder's preview: local opens it here, public through the live site. */
export interface PreviewURLs {
  local: string;
  public?: string;
}

export interface ProposalSnapshot {
  /** null when the changes could not be read; error says why. */
  changes: ModuleChange[] | null;
  error?: string;
  preview?: PreviewURLs;
  proposal: SiteProposal;
}

/** GET /api/site/proposals. */
export interface SiteProposals {
  proposals: ProposalSnapshot[];
}

export interface BuilderRow {
  builder: boolean;
  id: string;
  name: string;
  preview?: PreviewURLs;
}

/** GET /api/site/builders. */
export interface SiteBuilders {
  brains: BuilderRow[];
}

export interface BuildMessage {
  role: 'user' | 'assistant';
  text: string;
  at: string;
}

/** GET /api/site/build/{brain}. */
export interface BuildChat {
  /** What the run in progress is doing, from its log. */
  activity: string[];
  messages: BuildMessage[];
  preview?: PreviewURLs;
  proposal?: SiteProposal;
  queued: number;
  running: boolean;
  thread: number;
}

// ---------------------------------------------------------------------------
// GET /api/plaud/status

export interface PlaudCLI {
  found: boolean;
  path?: string;
  version?: string;
  warning?: string;
  install_hint: string;
}

/** The plaud settings in effect. */
export interface PlaudSettings {
  enabled: boolean;
  brain?: string;
  cli_path?: string;
  interval_seconds?: number;
  polished?: boolean;
  message?: string;
  brain_access?: boolean;
  phone_dir?: string;
  phone_upload?: boolean;
  /** The app may ask by voice (docs/PLAUD.md): a spoken question goes to voice_brain, its answer back to the phone. */
  voice?: boolean;
  /** Answers voice questions; "" for the brain that handles DMs, else the default brain. */
  voice_brain?: string;
  /** The answer goes to the phone only, not also to the brain's private chat. */
  voice_quiet?: boolean;
}

export interface PlaudConnectJob {
  state: 'running' | 'done' | 'error';
  auth_url?: string;
  error?: string;
}

/** A recording handed to a brain. */
export interface PlaudForward {
  id: string;
  name: string;
  brain: string;
  at: string;
  /** handed, done, stopped or failed. */
  status: string;
  error?: string;
}

/** A recording that was not handed over, and why. */
export interface PlaudGivenUp {
  id: string;
  name?: string;
  at: string;
  reason: string;
}

export interface PlaudAppBuild {
  state: 'running' | 'done' | 'error';
  started: string;
  finished?: string;
  error?: string;
  log?: string;
  pairing_expires?: string;
  /** The node that builds it, "" for this machine. */
  node?: string;
  stage?: string;
}

export interface PlaudPhone {
  id: string;
  paired: string;
  last_upload?: string;
}

/** The phone app that uploads through the site. */
export interface PlaudAppStatus {
  credentials: boolean;
  site_url?: string;
  built?: string;
  build?: PlaudAppBuild;
  devices: PlaudPhone[];
  /** "this computer", or the node that builds it; "" when nothing can. */
  builds_on?: string;
  no_builder?: string;
}

/** GET /api/plaud/status. */
export interface PlaudStatus {
  cli: PlaudCLI;
  connected: boolean;
  settings: PlaudSettings;
  login_dir: string;
  default_message: string;
  interval_seconds: number;
  target_brain?: string;
  /** The brain picked to receive recordings is gone. */
  brain_missing?: boolean;
  auto_brain?: string;
  summary_chat?: string;
  summary_to_you?: boolean;
  summary_chat_brain?: string;
  summary_error?: string;
  seen?: number;
  pending?: number;
  handed?: number;
  connect?: PlaudConnectJob;
  last_poll?: string;
  last_error?: string;
  last_forwarded?: PlaudForward;
  /** The latest last. */
  forwarded?: PlaudForward[];
  given_up?: PlaudGivenUp[];
  account?: string;
  expired?: boolean;
  login_moved?: boolean;
  app?: PlaudAppStatus;
}

// ---------------------------------------------------------------------------
// GET /api/housekeeping

export interface HousekeepingMember {
  pid: number;
  start?: string;
}

/** Something the job found (housekeeping.Finding). */
export interface HousekeepingFinding {
  kind: string;
  action: string;
  pid?: number;
  ppid?: number;
  start?: string;
  members?: HousekeepingMember[];
  name?: string;
  command?: string;
  cpu_percent?: number;
  rss?: number;
  age_seconds?: number;
  /** Whose it is: "run <id> (brain <x>)", vault, bridge, container. */
  owner?: string;
  reason: string;
  /** Something the job cannot fix itself. */
  problem?: boolean;
  /** stopped, would stop, asked, failed: …, left, reported. */
  outcome?: string;
  approval?: string;
  target?: string;
}

/** A run of the job. */
export interface HousekeepingReport {
  started: string;
  finished: string;
  /** schedule, dashboard or cli. */
  trigger: string;
  dry_run: boolean;
  note?: string;
  scanned: number;
  findings: HousekeepingFinding[] | null;
  errors?: string[];
}

export interface HousekeepingCounts {
  stopped: number;
  would_stop: number;
  asked: number;
  removed: number;
  failed: number;
  problems: number;
  reported: number;
}

/** GET /api/housekeeping. */
export interface HousekeepingStatus {
  enabled: boolean;
  dry_run: boolean;
  every_hours: number;
  cpu_percent: number;
  rss_mb: number;
  min_age_minutes: number;
  container: boolean;
  running: boolean;
  /** The processes asked about that wait for the owner. */
  asked: HousekeepingFinding[];
  last: HousekeepingReport | null;
  /** While the job is on. */
  next_run?: string;
  /** With last. */
  counts?: HousekeepingCounts;
  summary?: string;
}

// ---------------------------------------------------------------------------
// GitHub and Google

export interface GitHubAppInfo {
  id: number;
  slug: string;
  name: string;
  owner: string;
  html_url: string;
}

export interface GitHubInstallation {
  id: number;
  account: string;
  html_url: string;
  repository_selection: string;
  repos: string[];
  permissions: Record<string, string> | null;
  suspended: boolean;
}

/** GET /api/github. */
export interface GitHubStatus {
  connected: boolean;
  app?: GitHubAppInfo;
  /** The store that holds the app's key. */
  where?: string;
  installations: GitHubInstallation[];
  install_url?: string;
  settings_url?: string;
  delete_url?: string;
  /** A typed token is there, which runs get when no app is. */
  typed_token: boolean;
  error?: string;
}

/** GET /api/google/client: the client sign-ins use, and what its setup needs. */
export interface GoogleClient {
  /** This build carries a built-in client, used or not. */
  builtin: boolean;
  /** The client id's project number. */
  client_id_hint?: string;
  configured: boolean;
  redirect_uri: string;
  /** The scopes to add on the consent screen: the calendar's and Drive's. */
  scopes: string[];
  /** own (pasted under Settings) or builtin (the build's own). */
  source?: string;
  /** installed or web. */
  type?: string;
}

/** One thing POST /api/google/client/check looked at. */
export interface GoogleClientCheckItem {
  name: string;
  /** ok, warn or bad. */
  status: string;
  detail: string;
}

/** POST /api/google/client/check. */
export interface GoogleClientCheck {
  source: string;
  type: string;
  /** The worst of the items' statuses. */
  status: string;
  items: GoogleClientCheckItem[];
}

export interface CalendarEntry {
  id: string;
  summary: string;
  primary: boolean;
  access_role: string;
  color?: string;
}

/** A brain's calendar grant: how it stands with Google. */
export interface GrantHealth {
  /** valid, expiring, revoked, needs_reconsent or unchecked. */
  state: string;
  detail?: string;
  /** When Google last answered a calendar call with it. */
  last_ok?: string;
  /** When it broke. */
  since?: string;
  /** When Google ends the sign-in, for one it limited. */
  expires?: string;
}

/** One connected brain of GET /api/calendar/health. */
export interface CalendarHealthRow extends GrantHealth {
  id: string;
  name: string;
}

/** GET /api/calendar/health. */
export interface CalendarHealth {
  brains: CalendarHealthRow[];
}

/** GET /api/brains/{id}/calendar (?list=0 leaves the calendars unlisted). */
export interface CalendarStatus {
  account?: string;
  /** Signed in with an older version, which asked for every calendar. */
  broad_access?: boolean;
  /** The account's calendars; null when it has none. */
  calendars: CalendarEntry[] | null;
  client_configured: boolean;
  connected: boolean;
  default?: string;
  last_error?: string;
  /** Copies of the OAuth client that brains outside the sandbox could read. */
  leftover_client_copies: string[];
  list_error?: string;
  reconnect_needed?: boolean;
  /** The grant's health, when connected. */
  health?: GrantHealth;
  /** The calendars the brain may use. */
  selected: string[];
  token_path: string;
}

// ---------------------------------------------------------------------------
// Dev mode, logs, the folder browser

/** One entry of GET /api/dev/outbox (dev mode only), newest first. */
export interface DevMessage {
  ts: string;
  jid: string;
  text: string;
  /** A poll's options. */
  poll?: string[];
  /** A poll's message id, which a vote names. */
  id?: string;
}

export interface LogLine {
  seq: number;
  ts: string;
  text: string;
}

/** GET /api/logs?after=&limit=. */
export interface Logs {
  /** When the bridge started, Unix seconds: a new value means a restart. */
  boot: number;
  lines: LogLine[];
  /** The after of the next call. */
  next: number;
}

export interface DirectoryEntry {
  name: string;
  path: string;
  is_brain: boolean;
  is_git: boolean;
}

/** GET /api/fs/list?path=&hidden=. */
export interface DirectoryListing {
  entries: DirectoryEntry[];
  home: string;
  /** "" at the top. */
  parent: string;
  path: string;
}

/** GET /api/fs/capabilities. */
export interface FSCapabilities {
  home: string;
  /** The native folder picker's program, "" for none. */
  picker: string;
}

// ---------------------------------------------------------------------------
// Files and the wiki

export type FileRootKind = 'brain' | 'media' | 'node';
export type FileKind = 'markdown' | 'text' | 'image' | 'pdf' | 'audio' | 'video' | 'other';

/** A folder the Files page reads. */
export interface FileRoot {
  /** "brain:<id>", "media:<id>" or "node:<node>:<n>". */
  id: string;
  kind: FileRootKind;
  brain?: string;
  node?: string;
  label: string;
  path: string;
  /** "core" or the node's name. */
  location: string;
  writable: boolean;
  /** on, off, or left out for a node's folder. */
  backup?: string;
  /** The brain's backup.exclude lines, which the page hides. */
  excluded?: string[];
  online: boolean;
}

/** GET /api/files/roots. */
export interface FilesRoots {
  roots: FileRoot[];
}

export interface FileEntry {
  name: string;
  dir?: boolean;
  size: number;
  mod_time: string;
  /** Left out for a folder. */
  kind?: FileKind;
}

/** GET /api/files/list?root=&path=. */
export interface FilesList {
  root: string;
  path: string;
  entries: FileEntry[];
  truncated: boolean;
  /** The reader may change what is in the folder. */
  writable: boolean;
  /** Where an upload into the folder goes: into it, to the brain, or "" for none taken. */
  upload: FileUpload;
  /** The upload folder of the brain this folder lies in, for a reader who may change it. */
  rule?: UploadRule;
}

export type FileUpload = '' | 'folder' | 'brain';

/** Where a brain takes uploads from the site's roles (brain.json files.uploads). */
export interface UploadRule {
  folder: string;
  mode: FileUpload;
}

/** A file or folder a search by name found. */
export interface FileHit extends FileEntry {
  path: string;
}

/** GET /api/files/search?root=&path=&q=. */
export interface FilesSearch {
  hits: FileHit[];
  truncated: boolean;
}

/**
 * A rendered Markdown node: an element, or with t left out a text. Never
 * HTML: the page builds it with DOM calls.
 */
export interface WikiNode {
  /** The element: p, h1–h6, ul, ol, li, blockquote, pre, code, em, strong, del, a, img, table, thead, tbody, tr, th, td, hr, br. */
  t?: string;
  /** A text node's text. */
  x?: string;
  c?: WikiNode[];
  /** A heading's anchor. */
  id?: string;
  /** A link to the web: http, https or mailto. */
  href?: string;
  /** A link to a page: its path in the root. */
  page?: string;
  anchor?: string;
  /** A link to, or an image from, a file of the root. */
  file?: string;
  /** A [[link]] to a page that does not exist. */
  missing?: boolean;
  alt?: string;
  title?: string;
  lang?: string;
  align?: 'left' | 'center' | 'right';
  checked?: boolean;
  start?: number;
}

export interface WikiHeading {
  level: number;
  text: string;
  id: string;
}

/** A rendered page. */
export interface WikiDoc {
  title: string;
  tags?: string[];
  body: WikiNode[];
  toc: WikiHeading[];
}

/** GET /api/files/view?root=&path=. */
export interface FileView {
  root: string;
  path: string;
  name: string;
  size: number;
  mod_time: string;
  kind: FileKind;
  /** A text file's text. */
  text?: string;
  /** A Markdown file, rendered. */
  doc?: WikiDoc;
  /** text or doc has only the start of the file. */
  truncated?: boolean;
  /** The reader may edit it here: text or Markdown, whole, in a folder it may change. */
  editable?: boolean;
  /** The version an edit sends back. */
  sha?: string;
  /** The Markdown itself, with ?source=1. */
  source?: string;
}

/** A change made on the Files, File or Wiki page (httpapi.FileChange). */
export interface FileChange {
  at: string;
  /** dashboard, or the site's user. */
  by: string;
  /** edit, create, upload, move or delete. */
  action: string;
  path: string;
  /** Where it moved, or for a delete where in the trash it went. */
  to?: string;
  commit?: string;
  outcome: string;
}

/** GET /api/files/history?root=&path=: newest first. */
export interface FilesHistory {
  changes: FileChange[];
}

/** What became of a change in a folder that is a git repository. */
export interface FileCommit {
  /** The commit of the change. */
  commit?: string;
  /** Why there is none, although the folder is a repository. */
  git_error?: string;
}

/** PUT /api/files/text: the file as written. */
export interface FileSaved extends FileCommit {
  root: string;
  path: string;
  size: number;
  mod_time: string;
  /** Its new version, which the next edit sends back. */
  sha: string;
}

/** POST /api/files/move and /api/files/delete. */
export interface FileMoved extends FileCommit {
  root: string;
  /** Where it is now; for a delete, the path it had. */
  path: string;
}

/** POST /api/files/upload?root=&path=. */
export interface FilesUploaded extends FileCommit {
  /** Into the folder, or to the brain, which files them. */
  to: 'folder' | 'brain';
  files: { name: string; path?: string; size: number }[];
}

/** GET /api/files/ticket?root=: opens /files/raw for a while. */
export interface FilesTicket {
  expires: string;
  root: string;
  ticket: string;
}

/** A brain on the wiki's front page. */
export interface WikiBrain {
  brain: string;
  label: string;
  pages: number;
  home: string;
}

/** GET /api/wiki. */
export interface WikiBrains {
  brains: WikiBrain[];
}

export interface WikiPageInfo {
  /** The file's path in the brain folder, "wiki/people/alex.md". */
  path: string;
  /** The path in its section without ".md": "people/alex". */
  name: string;
  title: string;
  section: string;
  mod_time: string;
  size: number;
}

/** GET /api/wiki/{brain}/pages. */
export interface WikiPages {
  brain: string;
  label: string;
  /** The front page, "" for none. */
  home: string;
  pages: WikiPageInfo[];
  truncated: boolean;
}

/** GET /api/wiki/{brain}/page?path=. */
export interface WikiPage {
  brain: string;
  page: WikiPageInfo;
  doc: WikiDoc;
  backlinks: WikiPageInfo[];
  /** The reader may edit the page. */
  editable?: boolean;
}

export interface WikiHit {
  brain: string;
  path: string;
  title: string;
  section: string;
  snippet: string;
}

/** GET /api/wiki/search?q=&brain=. */
export interface WikiSearch {
  hits: WikiHit[];
}

// ---------------------------------------------------------------------------
// Connections (docs/CONNECTIONS.md)

// GET /api/connections
export interface Connection {
  id: 'openai' | 'elevenlabs';
  title: string;
  configured: boolean;
  /** The key's last characters, or "". */
  key_hint: string;
  organization?: string;
  project?: string;
  /** ElevenLabs: the default voice id, or "". */
  voice?: string;
  ops: string[];
  brains: string[];
  brain_monthly_usd: number;
}
export interface ConnectionModule {
  module: string;
  connection: 'openai' | 'elevenlabs';
  use: string[];
  models: string[];
  monthly_usd: number;
}
export interface ConnectionUsage {
  connection: 'openai' | 'elevenlabs';
  /** "module:<id>" or "brain:<id>" */
  caller: string;
  requests: number;
  errors: number;
  audio_seconds: number;
  input_tokens: number;
  output_tokens: number;
  characters: number;
  /** Estimated cost in USD. */
  usd: number;
  /** The caller's monthly limit in USD; 0 for none. */
  limit_usd: number;
}
export interface Connections {
  /** "YYYY-MM", UTC */
  month: string;
  connections: Connection[];
  modules: ConnectionModule[];
  usage: ConnectionUsage[];
}

// Git identities for FrIDE (docs/CONNECTIONS.md#gitlab, docs/NODES.md)

export type GitIdentityID = 'github' | 'gitlab';

/** GET /api/gitlab: the GitLab connection, never its token. */
export interface GitLabConnection {
  configured: boolean;
  /** https://gitlab.example.com, maybe with a path. */
  base_url: string;
  /** The host of base_url, with its port: what FrIDE keys the identity by. */
  host: string;
  /** "…" and the token's last four characters; "" for a short one. */
  token_hint: string;
  name: string;
  email: string;
  /** Where the token is kept, in words. */
  where: string;
}

/** One identity of GET /api/git-identities. */
export interface GitIdentityRow {
  id: GitIdentityID;
  kind: 'github' | 'gitlab';
  host: string;
  name: string;
  email: string;
  /** GitHub connected (or a typed token), GitLab set. */
  set: boolean;
  /** Why the FrIDEs got none the last time the core tried. */
  note?: string;
}

/** A node whose fride is approved, and what its FrIDE took. */
export interface GitIdentityNode {
  id: string;
  name: string;
  online: boolean;
  /** What nodes.git_identities gives it. */
  allowed: GitIdentityID[];
  /** What its FrIDE took last, at `at`. */
  got: GitIdentityID[];
  at?: string;
  /** Its FrIDE takes git identities. */
  supported: boolean;
  /** Its FrIDE did not answer: it gets them once it does. */
  pending?: boolean;
  error?: string;
  /** Why the node keeps them from its FrIDE on purpose (the FrIDE in a Docker Sandbox). */
  held?: string;
}

/** GET /api/git-identities */
export interface GitIdentities {
  identities: GitIdentityRow[];
  nodes: GitIdentityNode[];
}

// ---------------------------------------------------------------------------
// Paths

/** GET /api/schedules/preview?when=: when a schedule would run next, without jitter. */
export interface SchedulePreview {
  when: string;
  /** Up to three times, in the bridge's time zone. */
  next: string[];
  /** Why when is not a schedule. */
  error?: string;
}

/** What a GET of each path answers with. */
export interface ApiGet {
  '/api/status': Status;
  '/api/brains': Brain[];
  '/api/config': ConfigResponse;
  '/api/chats/ignored': IgnoredChat[];
  '/api/whatsapp/qr': WhatsAppQR;
  '/api/whatsapp/groups': WhatsAppGroup[];
  '/api/nodes': NodesResponse;
  '/api/activity': ActivityEntry[];
  '/api/threads': Thread[];
  '/api/fride': FrideResponse;
  '/api/connections': Connections;
  '/api/gitlab': GitLabConnection;
  '/api/git-identities': GitIdentities;
  '/api/audit': AuditEntry[];
  '/api/approvals': Approvals;
  '/api/policy': PolicyStatus;
  '/api/backup/status': BackupStatus;
  '/api/vault/status': VaultStatus;
  '/api/schedules': SchedulesResponse;
  '/api/schedules/preview': SchedulePreview;
  '/api/site': SiteStatus;
  '/api/site/users': SiteUsers;
  '/api/site/secrets': SiteSecrets;
  '/api/site/proposals': SiteProposals;
  '/api/site/builders': SiteBuilders;
  '/api/plaud/status': PlaudStatus;
  '/api/housekeeping': HousekeepingStatus;
  '/api/github': GitHubStatus;
  '/api/google/client': GoogleClient;
  '/api/calendar/health': CalendarHealth;
  '/api/dev/outbox': DevMessage[];
  '/api/logs': Logs;
  '/api/fs/list': DirectoryListing;
  '/api/fs/capabilities': FSCapabilities;
  '/api/files/roots': FilesRoots;
  '/api/files/list': FilesList;
  '/api/files/search': FilesSearch;
  '/api/files/view': FileView;
  '/api/files/ticket': FilesTicket;
  '/api/wiki': WikiBrains;
  '/api/wiki/search': WikiSearch;
}

/**
 * What a GET of path P answers with: P's entry in ApiGet, with a query
 * string or an id in the path matched by the patterns below; any for a
 * path not listed yet. A path the dashboard builds as
 * `/api/activity${qs(…)}` has the type `/api/activity${string}`, which
 * the patterns with a trailing ${string} match. The patterns go from the
 * most specific to the least, so that /api/brains/{id}/skills is not
 * taken for /api/brains/{id}.
 */
export type GetResponse<P extends string> =
  P extends `${infer Base}?${string}` ? GetResponse<Base> :
  P extends keyof ApiGet ? ApiGet[P] :
  P extends `/api/brains/${string}/skills` ? BrainSkills :
  P extends `/api/brains/${string}/calendar` ? CalendarStatus :
  P extends `/api/brains/${string}/backup/versions` ? BackupVersions :
  P extends `/api/brains/${string}` ? Brain :
  P extends `/api/schedules/preview${string}` ? SchedulePreview :
  P extends `/api/schedules/${string}/runs/${string}/log` ? ScheduleRunLog :
  P extends `/api/schedules/${string}/runs` ? ScheduleRun[] :
  P extends `/api/site/build/${string}` ? BuildChat :
  P extends `/api/wiki/search${string}` ? WikiSearch :
  P extends `/api/wiki/${string}/pages` ? WikiPages :
  P extends `/api/wiki/${string}/page${string}` ? WikiPage :
  P extends `/api/activity${string}` ? ActivityEntry[] :
  P extends `/api/audit${string}` ? AuditEntry[] :
  P extends `/api/threads${string}` ? Thread[] :
  P extends `/api/logs${string}` ? Logs :
  P extends `/api/whatsapp/groups${string}` ? WhatsAppGroup[] :
  P extends `/api/fs/list${string}` ? DirectoryListing :
  P extends `/api/files/list${string}` ? FilesList :
  P extends `/api/files/search${string}` ? FilesSearch :
  P extends `/api/files/view${string}` ? FileView :
  P extends `/api/files/ticket${string}` ? FilesTicket :
  P extends `/api/files/history${string}` ? FilesHistory :
  any;
