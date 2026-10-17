// Previews: a preview process opens only with its token; the live site
// forwards signed-in admins (and only them) to it.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, cpSync, symlinkSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { randomBytes } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import type { AddressInfo } from 'node:net'
import { createSite } from '../server/app.ts'
import { readRuntime } from '../server/config.ts'
import { sign } from '../server/auth.ts'

const SITE_DIR = join(dirname(fileURLToPath(import.meta.url)), '..')

function moduleDir(root: string, id: string, title: string) {
  const dir = join(root, 'modules', id)
  mkdirSync(join(dir, 'app'), { recursive: true })
  writeFileSync(join(dir, 'app/index.html'), `<!doctype html><html><head></head><body>${title}</body></html>`)
  writeFileSync(join(dir, 'module.json'), JSON.stringify({ version: 1, id, title, route: `/${id}`, access: { roles: ['*'] }, app: { dist: 'app', base: 'inject' } }))
}

async function listen(app: import('express').Express) {
  const server = app.listen(0, '127.0.0.1')
  await new Promise(r => server.once('listening', r))
  return { server, addr: `127.0.0.1:${(server.address() as AddressInfo).port}` }
}

test('the live site forwards an admin to a preview, and nobody else', async (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'fridolin-preview-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  cpSync(join(SITE_DIR, 'template'), join(dir, 'live'), { recursive: true })
  cpSync(join(SITE_DIR, 'template'), join(dir, 'ws'), { recursive: true })
  for (const d of ['live', 'ws']) symlinkSync(join(SITE_DIR, 'node_modules'), join(dir, d, 'node_modules'))
  moduleDir(join(dir, 'ws'), 'draft', 'Draft page')

  // The preview: no accounts, a token.
  const token = randomBytes(16).toString('hex')
  writeFileSync(join(dir, 'preview.json'), JSON.stringify({ listen: '127.0.0.1:0', title: 'P', modules_dir: join(dir, 'ws/modules'), preview_label: 'builder/web', preview_token: token }))
  const preview = await listen(await createSite(readRuntime(join(dir, 'preview.json'))))
  t.after(() => { preview.server.close() })

  // The live site, which knows the preview through previews.json.
  const key = randomBytes(32)
  writeFileSync(join(dir, 'session.key'), key.toString('hex'))
  writeFileSync(join(dir, 'users.json'), JSON.stringify({ users: [
    { id: 'u1', kind: 'google', email: 'owner@example.com', role: 'admin' },
    { id: 'u2', kind: 'google', email: 'member@example.com', role: 'member' },
  ] }))
  writeFileSync(join(dir, 'previews.json'), JSON.stringify({ web: { listen: preview.addr, token, label: 'builder/web' } }))
  writeFileSync(join(dir, 'live.json'), JSON.stringify({
    listen: '127.0.0.1:0', title: 'Live', modules_dir: join(dir, 'live/modules'),
    users_file: join(dir, 'users.json'), session_key_file: join(dir, 'session.key'), previews_file: join(dir, 'previews.json'),
  }))
  const live = await listen(await createSite(readRuntime(join(dir, 'live.json'))))
  t.after(() => { live.server.close() })

  const session = (uid: string) => `fridolin_site=${encodeURIComponent(sign(key, { uid, exp: Math.floor(Date.now() / 1000) + 600 }))}`
  const tiles = async (base: string, cookie: string) => {
    const r = await fetch(`${base}/api/modules`, { headers: { cookie } })
    return r.ok ? ((await r.json()) as { modules: { id: string }[] }).modules.map(m => m.id) : r.status
  }

  // The preview alone: refused without its token, open with it.
  assert.equal(await tiles(`http://${preview.addr}`, ''), 401)
  assert.equal(await tiles(`http://${preview.addr}`, session('u1')), 401, 'a live session must not open a preview')
  const login = await fetch(`http://${preview.addr}/_fridolin/preview-login?token=${token}`, { redirect: 'manual' })
  assert.equal(login.status, 302)
  const pv = login.headers.getSetCookie()[0].split(';')[0]
  assert.deepEqual(await tiles(`http://${preview.addr}`, pv), ['welcome', 'draft'])
  assert.equal((await fetch(`http://${preview.addr}/_fridolin/preview-login?token=wrong`)).status, 403)

  // Through the live site: an admin switches to the preview and back.
  const base = `http://${live.addr}`
  const admin = session('u1')
  assert.deepEqual(await tiles(base, admin), ['welcome'])
  const sw = await fetch(`${base}/_fridolin/preview/web`, { headers: { cookie: admin }, redirect: 'manual' })
  assert.equal(sw.status, 302)
  const choice = sw.headers.getSetCookie()[0].split(';')[0]
  const both = `${admin}; ${choice}`
  assert.deepEqual(await tiles(base, both), ['welcome', 'draft'])
  assert.match(await (await fetch(`${base}/draft/`, { headers: { cookie: both } })).text(), /Draft page/)
  assert.deepEqual(await (await fetch(`${base}/_fridolin/site.json`, { headers: { cookie: both } })).json(), { title: 'Live', preview: 'builder/web', exit: true })
  const me = await (await fetch(`${base}/auth/me`, { headers: { cookie: both } })).json() as { email: string }
  assert.equal(me.email, 'owner@example.com', 'the site’s own sign-in is never forwarded')

  // A member with the same cookie sees the live site; so does a stranger.
  const member = `${session('u2')}; ${choice}`
  assert.deepEqual(await tiles(base, member), ['welcome'])
  assert.equal((await fetch(`${base}/_fridolin/preview/web`, { headers: { cookie: session('u2') }, redirect: 'manual' })).status, 404)
  assert.equal((await fetch(`${base}/_fridolin/preview/nope`, { headers: { cookie: admin }, redirect: 'manual' })).status, 404)

  // Leaving clears the choice.
  const exit = await fetch(`${base}/_fridolin/preview-exit`, { headers: { cookie: both }, redirect: 'manual' })
  assert.match(exit.headers.getSetCookie()[0], /^fridolin_preview=;/)
})

test('check.ts reports mounted and broken modules as its last line', (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'fridolin-check-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  cpSync(join(SITE_DIR, 'template'), dir, { recursive: true })
  symlinkSync(join(SITE_DIR, 'node_modules'), join(dir, 'node_modules'))
  mkdirSync(join(dir, 'modules/bad'))
  writeFileSync(join(dir, 'modules/bad/module.json'), '{"version": 1, "id": "bad"}')
  const out = execFileSync(process.execPath, ['--import', 'tsx/esm', join(SITE_DIR, 'server/check.ts'), join(dir, 'modules')], { cwd: SITE_DIR, encoding: 'utf8' })
  const report = JSON.parse(out.trim().split('\n').pop()!) as { mounted: string[]; broken: { id: string }[] }
  assert.deepEqual(report.mounted, ['welcome'])
  assert.deepEqual(report.broken.map(b => b.id), ['bad'])
})
