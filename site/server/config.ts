// What the site process is told by Fridolin, and the user list it reads.
//
// Fridolin (the Go bridge) owns every setting: it writes runtime.json before it
// starts this process and edits users.json from its local dashboard. This
// process only reads them. users.json is re-read when it changes, so adding a
// user or changing a role takes effect without a restart.

import { closeSync, readFileSync, statSync } from 'node:fs'
import type { ModuleSecrets } from './modules.ts'

export type Runtime = {
  /** host:port to listen on, normally 127.0.0.1:<port> behind a tunnel. */
  listen: string
  /** "https://example.com" — used for the Google redirect and secure cookies. Empty: taken from the request. */
  public_origin: string
  /** Shown in the page title and header. */
  title: string
  /** The modules/ folder of the approved checkout of the site repository. */
  modules_dir: string
  users_file: string
  /** 32+ random bytes (hex) that sign the session cookies. */
  session_key_file: string
  /** Google OAuth client JSON ("web" section). Empty or missing: no Google sign-in. */
  google_client_file: string
  /** Set on a preview process: the branch it serves. Shown in a banner. */
  preview_label?: string
  /**
   * Set on a preview process: the token that opens it. A preview knows no
   * accounts (it runs code nobody approved yet, so it gets no session key and
   * no user list); see preview.ts.
   */
  preview_token?: string
  /** The live site: previews.json, the previews it may forward admins to. */
  previews_file?: string
  /**
   * The live site: a file descriptor (a pipe) holding its modules' secrets as
   * JSON, read once at startup and closed (readSecrets).
   */
  secrets_fd?: number
  /** The live site: holds one data folder per module (ctx.dataDir). A preview has its own. */
  data_dir?: string
  /** The live site: the handoff folder Fridolin watches for ctx.ingest. */
  spool_dir?: string
}

export function readRuntime(path: string): Runtime {
  const raw = JSON.parse(readFileSync(path, 'utf8')) as Partial<Runtime>
  const required = raw.preview_token ? ['listen', 'modules_dir'] as const : ['listen', 'modules_dir', 'users_file', 'session_key_file'] as const
  for (const k of required) {
    if (typeof raw[k] !== 'string' || !raw[k]) throw new Error(`${path}: "${k}" is required`)
  }
  return {
    listen: raw.listen!,
    public_origin: (raw.public_origin ?? '').replace(/\/+$/, ''),
    title: raw.title || 'My site',
    modules_dir: raw.modules_dir!,
    users_file: raw.users_file ?? '',
    session_key_file: raw.session_key_file ?? '',
    google_client_file: raw.google_client_file ?? '',
    preview_label: raw.preview_label || undefined,
    preview_token: raw.preview_token || undefined,
    previews_file: raw.previews_file || undefined,
    secrets_fd: typeof raw.secrets_fd === 'number' ? raw.secrets_fd : undefined,
    data_dir: raw.data_dir || undefined,
    // Only the live site hands files to brains.
    spool_dir: raw.preview_token ? undefined : raw.spool_dir || undefined,
  }
}

/**
 * Reads the modules' secrets from the pipe Fridolin passed, then closes it:
 * they are in no file and no environment variable module code could read.
 */
export function readSecrets(fd: number): ModuleSecrets {
  let raw: unknown
  try {
    raw = JSON.parse(readFileSync(fd, 'utf8'))
  } finally {
    closeSync(fd)
  }
  const out: ModuleSecrets = {}
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) throw new Error('the secrets Fridolin passed are not a JSON object')
  for (const [id, values] of Object.entries(raw)) {
    if (typeof values !== 'object' || values === null || Array.isArray(values)) throw new Error(`the secrets of module "${id}" are not an object`)
    out[id] = {}
    for (const [name, v] of Object.entries(values)) {
      if (typeof v !== 'string') throw new Error(`secret "${name}" of module "${id}" is not a string`)
      out[id][name] = v
    }
  }
  return out
}

export type SiteUser = {
  id: string
  kind: 'google' | 'password'
  /** Google accounts: the address Google verifies. */
  email?: string
  /** Password accounts: what they type to sign in. */
  username?: string
  name?: string
  role: string
  /** Password accounts: pbkdf2-sha256$<iterations>$<salt b64>$<hash b64>. */
  password_hash?: string
}

/** users.json, re-read whenever its modification time changes. */
export class UserStore {
  private mtimeMs = -1
  private users: SiteUser[] = []

  constructor(private path: string) {}

  all(): SiteUser[] {
    let mtimeMs: number
    try {
      mtimeMs = statSync(this.path).mtimeMs
    } catch {
      this.users = []
      this.mtimeMs = -1
      return this.users
    }
    if (mtimeMs !== this.mtimeMs) {
      try {
        const raw = JSON.parse(readFileSync(this.path, 'utf8')) as { users?: SiteUser[] }
        this.users = (raw.users ?? []).filter(u => u && typeof u.id === 'string' && typeof u.role === 'string')
        this.mtimeMs = mtimeMs
      } catch (err) {
        // A half-written file: keep the previous list and try again next time.
        console.error(`[site] could not read ${this.path}: ${(err as Error).message}`)
      }
    }
    return this.users
  }

  byId(id: string): SiteUser | undefined {
    return this.all().find(u => u.id === id)
  }

  byGoogleEmail(email: string): SiteUser | undefined {
    const e = email.toLowerCase()
    return this.all().find(u => u.kind === 'google' && u.email?.toLowerCase() === e)
  }

  byUsername(username: string): SiteUser | undefined {
    const n = username.toLowerCase()
    return this.all().find(u => u.kind === 'password' && u.username?.toLowerCase() === n)
  }

  hasPasswordUsers(): boolean {
    return this.all().some(u => u.kind === 'password')
  }
}

export type GoogleClient = { client_id: string; client_secret: string }

/** The "web" client from a Google Cloud OAuth client file, or null. */
export function readGoogleClient(path: string): GoogleClient | null {
  if (!path) return null
  try {
    const raw = JSON.parse(readFileSync(path, 'utf8')) as Record<string, { client_id?: string; client_secret?: string }>
    const c = raw.web ?? raw.installed
    if (c?.client_id && c.client_secret) return { client_id: c.client_id, client_secret: c.client_secret }
  } catch { /* no client */ }
  return null
}
