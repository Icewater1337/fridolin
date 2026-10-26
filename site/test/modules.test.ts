// Tests for the site-module loader: npm test
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, realpathSync, rmSync, symlinkSync, openSync, fstatSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { AddressInfo } from 'node:net'
import express from 'express'
import { validateManifest, canAccess, loadModules, mountModules, type SessionUser } from '../server/modules.ts'
import { readSecrets } from '../server/config.ts'

const SITE_DIR = join(dirname(fileURLToPath(import.meta.url)), '..')

const base = {
  version: 1, id: 'demo', title: 'Demo', route: '/demo',
  access: { roles: ['admin'] },
  app: { dist: 'dist', base: 'inject' },
}
const invalid = (patch: Record<string, unknown>) => () => validateManifest({ ...base, ...patch }, 'demo')

test('the starter site\'s modules are valid', () => {
  const { modules, broken } = loadModules(join(SITE_DIR, 'template/modules'))
  assert.deepEqual(broken, [])
  assert.ok(modules.length > 0)
})

test('validateManifest accepts a minimal app module', () => {
  assert.equal(validateManifest(base, 'demo').route, '/demo')
})

test('validateManifest refuses what it must', () => {
  assert.throws(invalid({ access: undefined }), /"access" is required/)
  assert.throws(invalid({ access: { roles: [] } }), /at least one role/)
  assert.throws(invalid({ access: { roles: ['admin'], emails: [] } }), /non-empty/)
  assert.throws(() => validateManifest({ ...base, id: 'other' }, 'demo'), /folder is "demo"/)
  assert.throws(invalid({ version: 2 }), /"version"/)
  assert.throws(invalid({ route: '/auth' }), /reserved/)
  assert.throws(invalid({ route: '/a/b' }), /one path segment/)
  assert.throws(invalid({ app: undefined }), /needs "app", "server" or both/)
  assert.throws(invalid({ app: { dist: 'dist', base: 'magic' } }), /"app.base"/)
  assert.throws(invalid({ app: { dist: 'dist', base: 'built', public: ['../secret'] } }), /plain file name/)
  assert.throws(invalid({ app: { dist: 'dist', base: 'built', public: ['.env'] } }), /plain file name/)
  assert.throws(invalid({ server: { entry: '../x.ts' } }), /inside the module folder/)
  assert.throws(invalid({ server: { entry: '/etc/x.ts' } }), /inside the module folder/)
  assert.throws(invalid({ server: { entry: 's.ts', mount: '/auth' } }), /"server.mount"/)
  assert.throws(invalid({ server: { entry: 's.ts', mount: '/demo-other' } }), /"server.mount"/)
  assert.throws(invalid({ server: { entry: 's.ts', mount: '/api/other' } }), /"server.mount"/)
  assert.throws(invalid({ server: { entry: 's.ts', public: 'intake' } }), /"server.public" must be a list/)
  for (const p of ['intake', '/', '/../x', '/a/..', '/a/*', '/a b', '/a//b', '/a/']) {
    assert.throws(invalid({ server: { entry: 's.ts', public: [p] } }), /"server.public" entry/, p)
  }
  validateManifest({ ...base, server: { entry: 's.ts', public: ['/intake', '/v1/form.json', '/a_b-c'] } }, 'demo')
  assert.throws(invalid({ capabilities: { files: { db: { path: 'x' } } } }), /"read" or "write"/)
  assert.throws(invalid({ capabilities: { secrets: ['API KEY'] } }), /"capabilities.secrets" entry "API KEY"/)
  assert.throws(invalid({ capabilities: { secrets: ['_key'] } }), /"capabilities.secrets"/)
  assert.throws(invalid({ capabilities: { commands: [' '] } }), /must not be empty/)
  validateManifest({ ...base, capabilities: { secrets: ['OPENAI_API_KEY', 'stripe-key'], commands: ['sqlite3'] } }, 'demo')
  // Mounts under the route or /api/<id> are fine.
  validateManifest({ ...base, server: { entry: 's.ts', mount: '/demo/v2' } }, 'demo')
  validateManifest({ ...base, server: { entry: 's.ts', mount: '/api/demo' } }, 'demo')
})

test('canAccess: roles, "*", and emails only through Google', () => {
  const admin: SessionUser = { id: 'u1', name: 'A', email: 'a@example.com', role: 'admin', authType: 'google' }
  const team: SessionUser = { id: 'u2', name: 'J', email: 'team.sam', role: 'team', authType: 'password' }
  assert.equal(canAccess({ roles: ['admin'] }, admin), true)
  assert.equal(canAccess({ roles: ['admin'] }, team), false)
  assert.equal(canAccess({ roles: ['*'] }, team), true)
  assert.equal(canAccess({ roles: ['*'] }, undefined), false)
  assert.equal(canAccess({ roles: ['admin'], emails: ['a@example.com'] }, admin), true)
  assert.equal(canAccess({ roles: ['admin'], emails: ['b@example.com'] }, admin), false)
  // A password account named like the email must not pass.
  assert.equal(canAccess({ roles: ['*'], emails: ['a@example.com'] }, { id: 'u9', name: 'x', email: 'a@example.com', role: 'admin', authType: 'password' }), false)
})

// ── Mounting, against a real Express server ───────────────────────────────────

function writeModule(root: string, id: string, manifest: Record<string, unknown>, files: Record<string, string> = {}) {
  const dir = join(root, id)
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'module.json'), JSON.stringify({ version: 1, id, title: id, route: `/${id}`, ...manifest }))
  for (const [name, body] of Object.entries(files)) {
    mkdirSync(dirname(join(dir, name)), { recursive: true })
    writeFileSync(join(dir, name), body)
  }
}

test('mountModules serves apps and APIs behind sign-in and access, and isolates broken modules', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'site-modules-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  // Module server code is ESM and imports express, like everything under dashboard/.
  writeFileSync(join(root, 'package.json'), '{ "type": "module" }')
  symlinkSync(join(SITE_DIR, 'node_modules'), join(root, 'node_modules'))

  const html = '<!doctype html><html><head><title>x</title></head><body></body></html>'
  writeModule(root, 'alpha', {
    order: 2,
    access: { roles: ['team', 'admin'] },
    app: { dist: 'dist', base: 'inject', public: ['sw.js'] },
    server: { entry: 'server.ts', access: { roles: ['*'] }, public: ['/open'] },
    capabilities: { files: { note: { path: 'data/note.txt', access: 'read' } } },
  }, {
    'dist/index.html': html,
    'dist/assets/app.js': 'console.log(1)',
    'dist/sw.js': '// sw',
    'data/note.txt': 'hello from the manifest',
    'server.ts': `import express from 'express'
import { readFileSync } from 'fs'
export default (ctx) => {
  const r = express.Router()
  r.get('/note', (_req, res) => res.json({ id: ctx.id, note: readFileSync(ctx.file('note'), 'utf8') }))
  r.get('/open/ping', (req, res) => res.json({ signedIn: !!req.user }))
  r.get('/opener', (_req, res) => res.json({ ok: true }))
  return r
}
`,
  })
  writeModule(root, 'beta', {
    order: 1,
    access: { roles: ['admin'] },
    app: { dist: 'dist', base: 'built' },
  }, { 'dist/index.html': html })
  // Server code that asks for a file its manifest does not declare: the whole module is skipped.
  writeModule(root, 'gamma', {
    access: { roles: ['*'] },
    app: { dist: 'dist', base: 'built' },
    server: { entry: 'server.ts' },
  }, {
    'dist/index.html': html,
    'server.ts': `export default (ctx) => { ctx.file('secrets'); throw new Error('unreachable') }\n`,
  })
  writeModule(root, 'delta', { access: { roles: ['admin'] }, app: { dist: 'dist', base: 'built' }, route: '/alpha' })
  writeModule(root, 'epsilon', { app: { dist: 'dist', base: 'built' } }) // no access rule

  const loaded = loadModules(root)
  assert.deepEqual(loaded.modules.map(m => m.manifest.id), ['alpha', 'beta', 'gamma'])
  assert.deepEqual(loaded.broken.map(b => b.id), ['delta', 'epsilon'])
  assert.match(loaded.broken[0].error, /already used by module "alpha"/)

  // Stand-in for index.ts's requireAuth: the user comes from a test header.
  const requireAuth: express.RequestHandler = (req, res, next) => {
    const raw = req.headers['x-test-user']
    if (!raw) { res.redirect('/'); return }
    ;(req as express.Request & { user?: SessionUser }).user = JSON.parse(String(raw))
    next()
  }
  const app = express()
  await mountModules(app, loaded, requireAuth)
  app.get('/*path', (_req, res) => { res.status(299).send('catch-all') })
  const server = app.listen(0)
  t.after(() => { server.close() })
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`

  const admin: SessionUser = { id: 'u1', name: 'A', email: 'a@example.com', role: 'admin', authType: 'google' }
  const team: SessionUser = { id: 'u2', name: 'J', email: 'team.sam', role: 'team', authType: 'password' }
  const study: SessionUser = { id: 'u3', name: 'S', email: 'study.x', role: 'study', authType: 'password' }
  type ModulesList = { modules: { id: string; href: string }[]; broken: { id: string; error: string }[] }
  const get = (path: string, user?: SessionUser) => fetch(url + path, {
    redirect: 'manual',
    headers: user ? { 'x-test-user': JSON.stringify(user) } : {},
  })

  // Signed out: redirected to sign-in, except the public file.
  assert.equal((await get('/alpha/')).status, 302)
  assert.equal((await get('/alpha/assets/app.js')).status, 302)
  assert.equal((await get('/alpha/api/note')).status, 302)
  assert.equal((await get('/alpha/sw.js')).status, 200)

  // Public server paths answer without sign-in, with no user; only that path and below.
  const open = await get('/alpha/api/open/ping')
  assert.equal(open.status, 200)
  assert.deepEqual(await open.json(), { signedIn: false })
  assert.equal((await get('/alpha/api/opener')).status, 302)
  // A public path the router does not answer falls through to sign-in.
  assert.equal((await get('/alpha/api/open/missing')).status, 302)

  // Allowed: index with <base> injected (also without the slash and for client routes), assets, API.
  for (const path of ['/alpha', '/alpha/', '/alpha/some/client/route']) {
    const r = await get(path, team)
    assert.equal(r.status, 200, path)
    assert.match(await r.text(), /<head><base href="\/alpha\/">/)
  }
  assert.equal(await (await get('/alpha/assets/app.js', team)).text(), 'console.log(1)')
  assert.deepEqual(await (await get('/alpha/api/note', study)).json(), { id: 'alpha', note: 'hello from the manifest' })

  // Not allowed: the app is team/admin only, its API is open to any signed-in user.
  assert.equal((await get('/alpha/', study)).status, 403)
  assert.equal((await get('/alpha/assets/app.js', study)).status, 403)
  assert.equal((await get('/beta/', team)).status, 403)
  const beta = await get('/beta/', admin)
  assert.equal(beta.status, 200)
  assert.doesNotMatch(await beta.text(), /<base/)

  // The broken module is not mounted at all.
  assert.equal((await get('/gamma/', admin)).status, 299)

  // Tiles: only what the user may open, in order; broken modules only for admins.
  const forAdmin = await (await get('/api/modules', admin)).json() as ModulesList
  assert.deepEqual(forAdmin.modules.map(m => m.id), ['beta', 'alpha'])
  assert.equal(forAdmin.modules[0].href, '/beta/')
  assert.deepEqual(forAdmin.broken.map(b => b.id).sort(), ['delta', 'epsilon', 'gamma'])
  assert.match(forAdmin.broken.find(b => b.id === 'gamma')?.error ?? '', /does not declare/)
  const forTeam = await (await get('/api/modules', team)).json() as ModulesList
  assert.deepEqual(forTeam.modules.map(m => m.id), ['alpha'])
  assert.deepEqual(forTeam.broken, [])
  assert.equal((await get('/api/modules')).status, 302)

  // The note file was only read through the manifest.
  assert.equal(readFileSync(join(root, 'alpha/data/note.txt'), 'utf8'), 'hello from the manifest')
})

test('public server paths are rate limited per client', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'site-modules-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  writeFileSync(join(root, 'package.json'), '{ "type": "module" }')
  symlinkSync(join(SITE_DIR, 'node_modules'), join(root, 'node_modules'))
  writeModule(root, 'form', {
    access: { roles: ['admin'] },
    server: { entry: 'server.ts', public: ['/submit'] },
  }, {
    'server.ts': `import express from 'express'
export default () => { const r = express.Router(); r.post('/submit', (_req, res) => res.json({ ok: true })); return r }
`,
  })
  const app = express()
  await mountModules(app, loadModules(root), (_req, res) => { res.redirect('/') })
  const server = app.listen(0)
  t.after(() => { server.close() })
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}/form/api/submit`
  const statuses: number[] = []
  for (let i = 0; i < 125; i++) statuses.push((await fetch(url, { method: 'POST' })).status)
  assert.equal(statuses.filter(s => s === 200).length, 120)
  assert.equal(statuses.at(-1), 429)
})

// ── A package.json in a subfolder ─────────────────────────────────────────────

test('a module finds the packages of the package.json above it, and a missing one names that package.json', async (t) => {
  // As a site repository with dashboard/package.json, dashboard/lib and
  // dashboard/modules. Two of them: Node keeps a failed import, by its path.
  const siteRepo = (installed: boolean) => {
    // Its real path, as Node names the importing file (macOS's /var is a link).
    const repo = realpathSync(mkdtempSync(join(tmpdir(), 'site-subfolder-')))
    t.after(() => rmSync(repo, { recursive: true, force: true }))
    const dashboard = join(repo, 'dashboard')
    mkdirSync(join(dashboard, 'lib'), { recursive: true })
    writeFileSync(join(dashboard, 'package.json'), '{ "type": "module" }')
    writeFileSync(join(dashboard, 'lib', 'search.ts'), "import 'no-such-package-here'\nexport const search = 1\n")
    if (installed) symlinkSync(join(SITE_DIR, 'node_modules'), join(dashboard, 'node_modules'))
    const server = { access: { roles: ['*'] }, server: { entry: 'server.ts' } }
    writeModule(join(dashboard, 'modules'), 'finance', server, { 'server.ts': "import express from 'express'\nexport default () => express.Router()\n" })
    writeModule(join(dashboard, 'modules'), 'search', server, { 'server.ts': "import { search } from '../../lib/search.ts'\nimport express from 'express'\nexport default () => { void search; return express.Router() }\n" })
    return dashboard
  }
  const requireAuth: express.RequestHandler = (_req, _res, next) => next()

  // Not installed: the message says so, and which package.json it went by.
  const bare = siteRepo(false)
  const before = await mountModules(express(), loadModules(join(bare, 'modules')), requireAuth)
  const finance = before.broken.find(b => b.id === 'finance')
  assert.ok(finance, JSON.stringify(before))
  assert.match(finance.error, /Cannot find package 'express'/)
  assert.ok(finance.error.includes(`${join(bare, 'package.json')} has no node_modules beside it`), finance.error)

  // Installed beside it, as a deploy does: express is found from modules/finance.
  const dashboard = siteRepo(true)
  const after = await mountModules(express(), loadModules(join(dashboard, 'modules')), requireAuth)
  assert.deepEqual(after.mounted, ['finance'])
  // A package the package.json does not list, imported from dashboard/lib.
  const search = after.broken.find(b => b.id === 'search')
  assert.ok(search, JSON.stringify(after))
  assert.ok(search.error.includes(`add it to ${join(dashboard, 'package.json')}`), search.error)
})

test('a module with no package.json above it is told to add one', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'site-nopkg-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  writeModule(root, 'lonely', { access: { roles: ['*'] }, server: { entry: 'server.mjs' } }, { 'server.mjs': "import 'no-such-package-here'\nexport default () => {}\n" })
  const { broken } = await mountModules(express(), loadModules(root), (_req, _res, next) => next())
  assert.equal(broken.length, 1)
  assert.match(broken[0].error, /no package\.json in .*lonely or a folder above it: add one that lists it/)
})

// ── Secrets ───────────────────────────────────────────────────────────────────

test('ctx.secret hands a module only the secrets it declares, and only its own', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'site-secrets-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  writeFileSync(join(root, 'package.json'), '{ "type": "module" }')
  symlinkSync(join(SITE_DIR, 'node_modules'), join(root, 'node_modules'))
  // Each server reports what ctx.secret gives it for every name it tries.
  const probe = (names: string[]) => `import express from 'express'
export default (ctx) => {
  const r = express.Router()
  const out = {}
  for (const name of ${JSON.stringify(names)}) {
    try { out[name] = ctx.secret(name) } catch (e) { out[name] = 'error: ' + e.message }
  }
  r.get('/probe', (_req, res) => res.json(out))
  return r
}
`
  const server = (secrets?: string[]) => ({ access: { roles: ['*'] }, server: { entry: 'server.ts' }, capabilities: { secrets } })
  writeModule(root, 'keeper', server(['API_KEY', 'UNSET']), { 'server.ts': probe(['API_KEY', 'UNSET', 'EXTRA']) })
  writeModule(root, 'snoop', server(), { 'server.ts': probe(['API_KEY']) })
  writeModule(root, 'twin', server(['API_KEY']), { 'server.ts': probe(['API_KEY']) })

  const requireAuth: express.RequestHandler = (req, _res, next) => {
    ;(req as express.Request & { user?: SessionUser }).user = { id: 'u1', name: 'A', email: 'a@example.com', role: 'admin', authType: 'google' }
    next()
  }
  const serve = async (secrets?: Record<string, Record<string, string>>) => {
    const app = express()
    const report = await mountModules(app, loadModules(root), requireAuth, secrets)
    assert.deepEqual(report.broken, [])
    const srv = app.listen(0)
    t.after(() => { srv.close() })
    const url = `http://127.0.0.1:${(srv.address() as AddressInfo).port}`
    return async (id: string) => await (await fetch(`${url}/${id}/api/probe`)).json() as Record<string, string>
  }

  // As Fridolin hands them over: keeper's value, and one keeper does not declare.
  const live = await serve({ keeper: { API_KEY: 'k-123', EXTRA: 'not declared' } })
  const keeper = await live('keeper')
  assert.equal(keeper.API_KEY, 'k-123')
  assert.match(keeper.UNSET, /is not set: set it in Fridolin's dashboard/)
  assert.match(keeper.EXTRA, /does not declare in capabilities.secrets/)
  // Another module gets nothing of keeper's: not by asking for the same
  // name undeclared, and not by declaring it.
  assert.match((await live('snoop')).API_KEY, /does not declare in capabilities.secrets/)
  assert.match((await live('twin')).API_KEY, /"API_KEY" of module "twin" is not set/)

  // A preview or a check: no secrets at all.
  assert.match((await (await serve())('keeper')).API_KEY, /previews and checks get no secrets/)
})

test('readSecrets reads the pipe once and closes it', (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'site-secrets-fd-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const file = join(dir, 'secrets.json')
  writeFileSync(file, JSON.stringify({ keeper: { API_KEY: 'k-123' } }))
  const fd = openSync(file, 'r')
  assert.deepEqual(readSecrets(fd), { keeper: { API_KEY: 'k-123' } })
  assert.throws(() => fstatSync(fd), /EBADF/)

  writeFileSync(file, JSON.stringify({ keeper: { API_KEY: 42 } }))
  const bad = openSync(file, 'r')
  assert.throws(() => readSecrets(bad), /is not a string/)
})
