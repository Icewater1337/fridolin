/**
 * Site modules.
 *
 * Every folder in the site repository's modules/ that holds a module.json is
 * one part of the site — an app, a page, an API — and the server mounts it at
 * startup.
 * The manifest says where the module lives, who may open it and which files,
 * programs and secrets it uses. Its server code gets those paths from the
 * manifest (ctx.file) and its secrets through ctx.secret, never from
 * constants of its own, so the manifest is the one place to review what a
 * module can reach. On macOS the live site runs in a sandbox Fridolin builds
 * from the manifests, which holds module code to what they declare. Format
 * and examples: docs/SITE.md.
 *
 * A module whose manifest is invalid, or whose server code fails to load, is
 * skipped and reported (log + /api/modules for admins); the rest of the site
 * keeps running.
 */

import express from 'express'
import { existsSync, mkdirSync, readdirSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { isAbsolute, join, resolve, sep } from 'node:path'
import { pathToFileURL } from 'node:url'
import { page, type SessionUser } from './auth.ts'
import { makeIngest, makeSearch, type IngestRequest, type IngestResult, type SearchRequest } from './ingest.ts'

// ── Manifest ──────────────────────────────────────────────────────────────────

/** Who may open a module. Roles are listed explicitly; '*' means any signed-in user. */
export type Access = {
  roles: string[]
  /** Also require one of these emails, verified by Google sign-in. */
  emails?: string[]
}

export type ModuleManifest = {
  version: 1
  id: string
  title: string
  description?: string
  icon?: string
  color?: string
  /** Tile position on the home page; lower comes first. */
  order?: number
  /** Where the module is served, e.g. "/finance". One path segment. */
  route: string
  access: Access
  /** A prebuilt single-page app served at the route. */
  app?: {
    /** Folder with index.html: relative to the module folder, absolute, or ~/… */
    dist: string
    /**
     * 'built': the app was built for the route (Vite `base`), asset URLs are already right.
     * 'inject': the app uses relative asset URLs; a <base href="<route>/"> is added to index.html.
     */
    base: 'built' | 'inject'
    /** Files in dist served without sign-in (a PWA's manifest and service worker). */
    public?: string[]
    /** How scripts/build-module.mjs rebuilds dist. */
    build?: { cwd: string; run: string[] }
  }
  /** Server code: a file whose default export returns an Express router. */
  server?: {
    entry: string
    /** Where the router is mounted. Default "<route>/api". Must be under the route or /api/<id>. */
    mount?: string
    /** Who may call it. Default: the module's access. */
    access?: Access
  }
  /** What the module touches. Declared here so it can be reviewed in one place. */
  capabilities?: {
    files?: Record<string, { path: string; access: 'read' | 'write' }>
    commands?: string[]
    secrets?: string[]
    /** Brains the module may hand files to with ctx.ingest. */
    ingest?: string[]
    /** Brains whose folder the module may search with ctx.search. */
    search?: string[]
  }
}

/**
 * First path segments the site serves itself (/auth/*, /api/modules). A
 * module cannot take them; its server may still mount under /api/<id>. The
 * site's own files live under /_fridolin/, which no route can name.
 */
export const RESERVED_ROUTES = ['auth', 'api']

const ID_RE = /^[a-z0-9][a-z0-9-]*$/
/** A secret's name; Fridolin's dashboard checks the same (site_secrets.go). */
const SECRET_RE = /^[A-Za-z][A-Za-z0-9_-]{0,63}$/
// A brain's id, as Fridolin's manifest.go checks it.
const BRAIN_ID_RE = /^[a-z0-9][a-z0-9_-]{0,39}$/

/** Checks a parsed module.json; throws an Error saying what is wrong. */
export function validateManifest(raw: unknown, folder: string): ModuleManifest {
  const fail = (why: string): never => { throw new Error(why) }
  if (!isObject(raw)) fail('module.json must be a JSON object')
  const m = raw as Record<string, unknown>

  if (m.version !== 1) fail('"version" must be 1')
  if (typeof m.id !== 'string' || !ID_RE.test(m.id)) fail('"id" must be lowercase letters, digits and "-"')
  if (m.id !== folder) fail(`"id" is "${m.id}" but the folder is "${folder}"; they must match`)
  if (typeof m.title !== 'string' || !m.title.trim()) fail('"title" is required')
  for (const k of ['description', 'icon', 'color'] as const) {
    if (m[k] !== undefined && typeof m[k] !== 'string') fail(`"${k}" must be a string`)
  }
  if (m.order !== undefined && typeof m.order !== 'number') fail('"order" must be a number')

  if (typeof m.route !== 'string' || !/^\/[a-z0-9][a-z0-9-]*$/.test(m.route)) {
    fail('"route" must be one path segment, e.g. "/finance"')
  }
  const route = m.route as string
  if (RESERVED_ROUTES.includes(route.slice(1))) fail(`"route" ${route} is reserved by the site itself`)

  checkAccess(m.access, 'access')

  if (m.app === undefined && m.server === undefined) fail('a module needs "app", "server" or both')

  if (m.app !== undefined) {
    if (!isObject(m.app)) fail('"app" must be an object')
    const a = m.app as Record<string, unknown>
    if (typeof a.dist !== 'string' || !a.dist) fail('"app.dist" is required')
    if (a.base !== 'built' && a.base !== 'inject') fail('"app.base" must be "built" or "inject"')
    if (a.public !== undefined) {
      if (!isStringArray(a.public)) fail('"app.public" must be a list of file names')
      for (const f of a.public as string[]) {
        if (!/^[\w.-]+$/.test(f) || f.startsWith('.')) fail(`"app.public" entry "${f}" must be a plain file name`)
      }
    }
    if (a.build !== undefined) {
      if (!isObject(a.build)) fail('"app.build" must be an object')
      const b = a.build as Record<string, unknown>
      if (typeof b.cwd !== 'string' || !b.cwd) fail('"app.build.cwd" is required')
      if (!isStringArray(b.run) || (b.run as string[]).length === 0) fail('"app.build.run" must be a command as a list, e.g. ["npx", "vite", "build"]')
    }
  }

  if (m.server !== undefined) {
    if (!isObject(m.server)) fail('"server" must be an object')
    const s = m.server as Record<string, unknown>
    if (typeof s.entry !== 'string' || !s.entry) fail('"server.entry" is required')
    if (isAbsolute(s.entry as string) || (s.entry as string).split(/[/\\]/).includes('..')) {
      fail('"server.entry" must be a path inside the module folder')
    }
    if (s.mount !== undefined) {
      const mount = s.mount
      const ownApi = `/api/${m.id}`
      const ok = typeof mount === 'string' &&
        [route, ownApi].some(p => mount === p || mount.startsWith(p + '/')) &&
        !mount.split('/').includes('..')
      if (!ok) fail(`"server.mount" must be under ${route} or ${ownApi}`)
    }
    if (s.access !== undefined) checkAccess(s.access, 'server.access')
  }

  if (m.capabilities !== undefined) {
    if (!isObject(m.capabilities)) fail('"capabilities" must be an object')
    const c = m.capabilities as Record<string, unknown>
    if (c.files !== undefined) {
      if (!isObject(c.files)) fail('"capabilities.files" must map names to { path, access }')
      for (const [name, f] of Object.entries(c.files as Record<string, unknown>)) {
        if (!isObject(f)) fail(`"capabilities.files.${name}" must be { path, access }`)
        const ff = f as Record<string, unknown>
        if (typeof ff.path !== 'string' || !ff.path) fail(`"capabilities.files.${name}.path" is required`)
        if (ff.access !== 'read' && ff.access !== 'write') fail(`"capabilities.files.${name}.access" must be "read" or "write"`)
      }
    }
    for (const k of ['commands', 'secrets'] as const) {
      if (c[k] !== undefined && !isStringArray(c[k])) fail(`"capabilities.${k}" must be a list of strings`)
    }
    for (const cmd of (c.commands ?? []) as string[]) {
      if (!cmd.trim()) fail('"capabilities.commands" entries must not be empty')
    }
    for (const name of (c.secrets ?? []) as string[]) {
      if (!SECRET_RE.test(name)) fail(`"capabilities.secrets" entry "${name}" must be a letter, then up to 63 letters, digits, _ or -`)
    }
    for (const k of ['ingest', 'search'] as const) {
      if (c[k] === undefined) continue
      if (!isStringArray(c[k])) fail(`"capabilities.${k}" must be a list of brain ids`)
      for (const b of c[k] as string[]) {
        if (!BRAIN_ID_RE.test(b)) fail(`"capabilities.${k}" entry "${b}" is not a brain id`)
      }
    }
  }

  return m as unknown as ModuleManifest

  function checkAccess(v: unknown, where: string) {
    if (!isObject(v)) fail(`"${where}" is required: { "roles": [...] } ("*" = any signed-in user)`)
    const a = v as Record<string, unknown>
    if (!isStringArray(a.roles) || (a.roles as string[]).length === 0) fail(`"${where}.roles" must list at least one role`)
    if (a.emails !== undefined && (!isStringArray(a.emails) || (a.emails as string[]).length === 0)) {
      fail(`"${where}.emails" must be a non-empty list when present`)
    }
  }
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function isStringArray(v: unknown): v is string[] {
  return Array.isArray(v) && v.every(x => typeof x === 'string')
}

/** Resolves a manifest path: ~/… is in the home folder, a relative path is in the module folder. */
export function resolveModulePath(p: string, moduleDir: string): string {
  if (p === '~') return process.env.HOME!
  if (p.startsWith('~/')) return join(process.env.HOME!, p.slice(2))
  return resolve(moduleDir, p)
}

// ── Access ────────────────────────────────────────────────────────────────────

export type { SessionUser }

/** The signed-in user, as requireAuth puts it on the request. */
export function sessionUser(req: express.Request): SessionUser | undefined {
  return (req as express.Request & { user?: SessionUser }).user
}

export function canAccess(access: Access, user: SessionUser | undefined): boolean {
  if (!user) return false
  if (!access.roles.includes('*') && !access.roles.includes(user.role)) return false
  // A password account's "email" is just the username it was created with, so
  // an email rule only counts for addresses Google has verified.
  if (access.emails && !(user.authType === 'google' && access.emails.includes(user.email))) return false
  return true
}

function gate(access: Access, api: boolean): express.RequestHandler {
  return (req, res, next) => {
    if (canAccess(access, sessionUser(req))) { next(); return }
    if (api) res.status(403).json({ error: 'Forbidden' })
    else res.status(403).send(page('No access', 'Your account may not open this page. Ask the site’s owner if you need it.'))
  }
}

// ── Loading ───────────────────────────────────────────────────────────────────

export type SiteModule = {
  manifest: ModuleManifest
  dir: string
  /** Resolved app.dist, when the module has an app. */
  distDir?: string
  /** Resolved capabilities.files. */
  files: Record<string, string>
}

export type BrokenModule = { id: string; error: string }

/** What a module's server code receives. */
export type ModuleContext = {
  id: string
  dir: string
  /** A path declared in capabilities.files. Throws for a name the manifest does not declare. */
  file: (name: string) => string
  /**
   * A secret declared in capabilities.secrets, as set in Fridolin's dashboard
   * (Site → Secrets). Throws for a name the manifest does not declare, and for
   * one that has no value yet.
   */
  secret: (name: string) => string
  /**
   * A folder of the module's own that survives deploys, readable and
   * writable (<data_dir>/site/data/<id> on the live site): logs, state,
   * uploads on their way to a brain.
   */
  dataDir: string
  /**
   * Hands files to a brain listed in capabilities.ingest, as a WhatsApp
   * attachment would reach it. The files are moved. Only the live site can.
   */
  ingest: (req: IngestRequest) => Promise<IngestResult>
  /**
   * Runs a read-only Claude agent (Read, Grep, Glob) in the folder of a brain
   * listed in capabilities.search, under that brain's sandbox, and yields its
   * stream-json events. Only the live site can.
   */
  search: (req: SearchRequest) => AsyncGenerator<unknown>
  log: (...args: unknown[]) => void
}

/** Where the site keeps what modules write: from runtime.json. */
export type ModuleHost = {
  /** Holds one folder per module (ctx.dataDir); a temp folder when unset. */
  dataDir?: string
  /** The handoff folder Fridolin watches (ctx.ingest); unset in previews and checks. */
  spoolDir?: string
}

/**
 * Secret values by module id and name: what Fridolin hands the live site at
 * startup, only the names each module declares. Previews and checks get none.
 */
export type ModuleSecrets = Record<string, Record<string, string>>

/** Default export of a module's server entry. */
export type ModuleServer = (ctx: ModuleContext) => express.Router | Promise<express.Router>

/** Reads every modules/<id>/module.json. Invalid ones land in `broken`. */
export function loadModules(modulesDir: string): { modules: SiteModule[]; broken: BrokenModule[] } {
  const modules: SiteModule[] = []
  const broken: BrokenModule[] = []
  if (!existsSync(modulesDir)) return { modules, broken }

  const folders = readdirSync(modulesDir, { withFileTypes: true })
    .filter(e => e.isDirectory() && existsSync(join(modulesDir, e.name, 'module.json')))
    .map(e => e.name)
    .sort()

  const routes = new Map<string, string>()
  for (const folder of folders) {
    const dir = join(modulesDir, folder)
    try {
      const manifest = validateManifest(JSON.parse(readFileSync(join(dir, 'module.json'), 'utf8')), folder)
      const taken = routes.get(manifest.route)
      if (taken) throw new Error(`"route" ${manifest.route} is already used by module "${taken}"`)
      if (manifest.server && !existsSync(resolve(dir, manifest.server.entry))) {
        throw new Error(`"server.entry" ${manifest.server.entry} does not exist`)
      }
      const files: Record<string, string> = {}
      for (const [name, f] of Object.entries(manifest.capabilities?.files ?? {})) {
        files[name] = resolveModulePath(f.path, dir)
      }
      routes.set(manifest.route, manifest.id)
      modules.push({
        manifest,
        dir,
        distDir: manifest.app ? resolveModulePath(manifest.app.dist, dir) : undefined,
        files,
      })
    } catch (err) {
      broken.push({ id: folder, error: err instanceof Error ? err.message : String(err) })
    }
  }
  return { modules, broken }
}

// ── Mounting ──────────────────────────────────────────────────────────────────

/**
 * Mounts every module on the app and adds GET /api/modules (the home page's
 * tiles). Call it after the shell's own routes and before the SPA catch-all.
 */
export async function mountModules(
  app: express.Express,
  loaded: { modules: SiteModule[]; broken: BrokenModule[] },
  requireAuth: express.RequestHandler,
  secrets?: ModuleSecrets,
  host: ModuleHost = {},
): Promise<{ mounted: string[]; broken: BrokenModule[] }> {
  const mounted: SiteModule[] = []
  const broken = [...loaded.broken]

  for (const mod of loaded.modules) {
    try {
      // Load the server code first: if it fails, nothing of the module is mounted.
      const router = mod.manifest.server ? await loadServer(mod, secrets, host) : undefined
      mountModule(app, mod, router, requireAuth)
      mounted.push(mod)
    } catch (err) {
      broken.push({ id: mod.manifest.id, error: explain(err) })
    }
  }

  for (const b of broken) console.error(`[modules] ${b.id} not loaded: ${b.error}`)
  console.log(`[modules] mounted: ${mounted.map(m => `${m.manifest.id} (${m.manifest.route})`).join(', ') || 'none'}`)

  app.get('/api/modules', requireAuth, (req, res) => {
    const user = sessionUser(req)
    const tiles = mounted
      .filter(m => m.manifest.app && canAccess(m.manifest.access, user))
      .sort((a, b) => (a.manifest.order ?? 100) - (b.manifest.order ?? 100) || a.manifest.title.localeCompare(b.manifest.title))
      .map(({ manifest: m }) => ({
        id: m.id,
        title: m.title,
        description: m.description ?? '',
        icon: m.icon ?? '🧩',
        color: m.color ?? '#64748b',
        href: `${m.route}/`,
      }))
    res.json({ modules: tiles, broken: user?.role === 'admin' ? broken : [] })
  })
  return { mounted: mounted.map(m => m.manifest.id), broken }
}

// Load errors, with a hint for the two everyone hits once.
function explain(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err)
  if (/require\(\) ES Module|Cannot use import statement/.test(msg)) {
    return `${msg} — the site repository's package.json needs "type": "module"`
  }
  if (/Cannot find (package|module) '([^.'/][^']*)'/.test(msg)) {
    return `${msg} — add it to the site repository's package.json; Fridolin runs npm install when it deploys`
  }
  return msg
}

async function loadServer(mod: SiteModule, secrets: ModuleSecrets | undefined, host: ModuleHost): Promise<express.Router> {
  const { id } = mod.manifest
  // Only this module's own, kept here: no other module's ctx can name them.
  const declared = new Set(mod.manifest.capabilities?.secrets ?? [])
  const own = new Map<string, string>()
  for (const name of declared) {
    const v = secrets?.[id]?.[name]
    if (typeof v === 'string') own.set(name, v)
  }
  const entry = resolve(mod.dir, mod.manifest.server!.entry)
  if (!entry.startsWith(mod.dir + sep)) throw new Error('"server.entry" points outside the module folder')
  const exported = (await import(pathToFileURL(entry).href)).default as ModuleServer | undefined
  if (typeof exported !== 'function') throw new Error(`${mod.manifest.server!.entry} has no default export function`)
  const ctx: ModuleContext = {
    id,
    dir: mod.dir,
    file: (name) => {
      const p = mod.files[name]
      if (!p) throw new Error(`module "${id}" uses file "${name}", which its module.json does not declare in capabilities.files`)
      return p
    },
    secret: (name) => {
      if (!declared.has(name)) throw new Error(`module "${id}" uses secret "${name}", which its module.json does not declare in capabilities.secrets`)
      const v = own.get(name)
      if (v !== undefined) return v
      if (!secrets) throw new Error(`secret "${name}": previews and checks get no secrets, only the live site does`)
      throw new Error(`secret "${name}" of module "${id}" is not set: set it in Fridolin's dashboard (Site → Secrets)`)
    },
    dataDir: moduleDataDir(id, host),
    ingest: makeIngest(id, mod.manifest.capabilities?.ingest ?? [], host.spoolDir),
    search: makeSearch(id, mod.manifest.capabilities?.search ?? [], host.spoolDir),
    log: (...args) => console.log(`[module:${id}]`, ...args),
  }
  return exported(ctx)
}

// The module's data folder, made if missing. Without a host data folder (a
// check) it is a temp folder of its own.
function moduleDataDir(id: string, host: ModuleHost): string {
  const dir = host.dataDir ? join(host.dataDir, id) : join(tmpdir(), 'fridolin-site-data', id)
  mkdirSync(dir, { recursive: true, mode: 0o700 })
  return dir
}

function mountModule(app: express.Express, mod: SiteModule, router: express.Router | undefined, requireAuth: express.RequestHandler) {
  const m = mod.manifest
  const access = gate(m.access, false)

  // Public files go first so the sign-in check below does not catch them.
  for (const f of m.app?.public ?? []) {
    app.get(`${m.route}/${f}`, (_req, res) => {
      // root: a preview's modules live under a brain's .fridolin-site folder.
      res.sendFile(f, { root: mod.distDir! }, (err: unknown) => { if (err) res.status(404).send('Not found') })
    })
  }

  // The API before the app, so the app's fallback route cannot shadow it.
  if (router) {
    const mount = m.server!.mount ?? `${m.route}/api`
    app.use(mount, requireAuth, gate(m.server!.access ?? m.access, true), router)
  }

  if (m.app) {
    const dist = mod.distDir!
    // index.html is read per request, so a rebuild goes live without a restart.
    const serveIndex = (_req: express.Request, res: express.Response) => {
      let html: string
      try {
        html = readFileSync(join(dist, 'index.html'), 'utf8')
      } catch {
        res.status(404).send(page(`${m.title} is not built`, 'Its app has no index.html yet. Deploy the site again from Fridolin’s dashboard to build it.'))
        return
      }
      if (m.app!.base === 'inject') html = html.replace('<head>', `<head><base href="${m.route}/">`)
      res.type('html').send(html)
    }
    // Non-strict routing: this also answers "<route>" without the slash.
    app.get(`${m.route}/`, requireAuth, access, serveIndex)
    app.use(m.route, requireAuth, access, express.static(dist, { index: false }))
    app.get(`${m.route}/*path`, requireAuth, access, serveIndex)
  }
}
