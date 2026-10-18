// End-to-end tests for the site process: sign-in, sessions, tiles, access.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, symlinkSync, cpSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { randomBytes } from 'node:crypto'
import type { AddressInfo } from 'node:net'
import { createSite } from '../server/app.ts'
import { readRuntime } from '../server/config.ts'
import { hashPassword, safeNext, sign, verify } from '../server/auth.ts'

const SITE_DIR = join(dirname(fileURLToPath(import.meta.url)), '..')

type Fixture = { url: string; dir: string; writeUsers: (users: object[]) => void; googleCalls: string[] }

const ADMIN = { id: 'u_admin', kind: 'google', email: 'owner@example.com', name: 'Owner Person', role: 'admin' }
const MEMBER = { id: 'u_member', kind: 'password', username: 'sam', name: 'Sam', role: 'member', password_hash: hashPassword('correct horse', 100_000) }

async function startSite(t: import('node:test').TestContext, googleUser = { email: 'owner@example.com', email_verified: true }): Promise<Fixture> {
  const dir = mkdtempSync(join(tmpdir(), 'fridolin-site-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))

  // The starter template plus a members-only module with an API.
  cpSync(join(SITE_DIR, 'template'), join(dir, 'repo'), { recursive: true })
  symlinkSync(join(SITE_DIR, 'node_modules'), join(dir, 'repo/node_modules'))
  const team = join(dir, 'repo/modules/team')
  mkdirSync(join(team, 'dist'), { recursive: true })
  writeFileSync(join(team, 'dist/index.html'), '<!doctype html><html><head></head><body>team</body></html>')
  writeFileSync(join(team, 'module.json'), JSON.stringify({
    version: 1, id: 'team', title: 'Team', route: '/team', order: 2,
    access: { roles: ['admin', 'member'] },
    app: { dist: 'dist', base: 'inject' },
    server: { entry: 'server.ts' },
  }))
  writeFileSync(join(team, 'server.ts'), `import express from 'express'
export default () => express.Router().get('/whoami', (req, res) => res.json(req.user))
`)
  mkdirSync(join(dir, 'repo/modules/secret/dist'), { recursive: true })
  writeFileSync(join(dir, 'repo/modules/secret/dist/index.html'), '<html><head></head><body>secret</body></html>')
  writeFileSync(join(dir, 'repo/modules/secret/module.json'), JSON.stringify({
    version: 1, id: 'secret', title: 'Admins', route: '/secret', access: { roles: ['admin'] }, app: { dist: 'dist', base: 'built' },
  }))

  const users = join(dir, 'users.json')
  const writeUsers = (list: object[]) => writeFileSync(users, JSON.stringify({ version: 1, users: list }))
  writeUsers([ADMIN, MEMBER])
  writeFileSync(join(dir, 'session.key'), randomBytes(32).toString('hex'))
  writeFileSync(join(dir, 'google.json'), JSON.stringify({ web: { client_id: 'cid.apps.googleusercontent.com', client_secret: 'csecret' } }))
  writeFileSync(join(dir, 'runtime.json'), JSON.stringify({
    listen: '127.0.0.1:0', public_origin: '', title: 'Test site',
    modules_dir: join(dir, 'repo/modules'), users_file: users,
    session_key_file: join(dir, 'session.key'), google_client_file: join(dir, 'google.json'),
  }))

  const googleCalls: string[] = []
  const fakeGoogle = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input)
    googleCalls.push(url)
    if (url.includes('/token')) {
      const body = new URLSearchParams(String(init?.body))
      assert.equal(body.get('code'), 'the-code')
      assert.ok(body.get('code_verifier'))
      return new Response(JSON.stringify({ access_token: 'at' }), { status: 200 })
    }
    return new Response(JSON.stringify({ ...googleUser, picture: 'https://example.com/p.png' }), { status: 200 })
  }) as typeof fetch

  const app = await createSite(readRuntime(join(dir, 'runtime.json')), { googleFetch: fakeGoogle })
  const server = app.listen(0, '127.0.0.1')
  await new Promise(r => server.once('listening', r))
  t.after(() => { server.close() })
  return { url: `http://127.0.0.1:${(server.address() as AddressInfo).port}`, dir, writeUsers, googleCalls }
}

function cookieFrom(r: Response, name: string): string {
  for (const c of r.headers.getSetCookie()) {
    if (c.startsWith(`${name}=`)) return c.split(';')[0]
  }
  return ''
}

async function login(url: string, username: string, password: string): Promise<{ status: number; cookie: string }> {
  const r = await fetch(`${url}/auth/login`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }),
  })
  return { status: r.status, cookie: cookieFrom(r, 'fridolin_site') }
}

test('home page, health and the strict policy on the site’s own pages', async (t) => {
  const { url } = await startSite(t)
  assert.equal(await (await fetch(`${url}/_fridolin/health`)).text(), 'ok')
  const home = await fetch(`${url}/`)
  assert.equal(home.status, 200)
  assert.match(home.headers.get('content-security-policy') ?? '', /script-src 'self'/)
  assert.match(await home.text(), /\/_fridolin\/app\.js/)
  assert.deepEqual(await (await fetch(`${url}/_fridolin/site.json`)).json(), { title: 'Test site', preview: null, exit: false })
  const me = await fetch(`${url}/auth/me`)
  assert.equal(me.status, 401)
  assert.deepEqual(await me.json(), { error: 'Sign in first', google: true, password: true })
  assert.equal((await fetch(`${url}/nope`)).status, 404)
})

test('password sign-in, roles, tiles and sign-out', async (t) => {
  const { url } = await startSite(t)
  assert.equal((await login(url, 'sam', 'wrong')).status, 401)
  assert.equal((await login(url, 'nobody', 'x')).status, 401)
  const ok = await login(url, 'SAM', 'correct horse')
  assert.equal(ok.status, 200)
  assert.ok(ok.cookie)
  const as = (path: string, init: RequestInit = {}) => fetch(url + path, { redirect: 'manual', ...init, headers: { cookie: ok.cookie, ...(init.headers ?? {}) } })

  const me = await (await as('/auth/me')).json() as { name: string; role: string; authType: string }
  assert.deepEqual([me.name, me.role, me.authType], ['Sam', 'member', 'password'])

  const tiles = await (await as('/api/modules')).json() as { modules: { id: string }[]; broken: unknown[] }
  assert.deepEqual(tiles.modules.map(m => m.id), ['welcome', 'team'])
  assert.deepEqual(tiles.broken, [])

  const team = await as('/team/')
  assert.equal(team.status, 200)
  assert.match(await team.text(), /<base href="\/team\/">/)
  const whoami = await (await as('/team/api/whoami')).json() as { email: string }
  assert.equal(whoami.email, 'sam')
  assert.equal((await as('/secret/')).status, 403)

  // Signed out: pages redirect to sign-in (keeping where you were going), APIs answer 401.
  const page = await fetch(`${url}/team/x`, { redirect: 'manual' })
  assert.equal(page.status, 302)
  assert.equal(page.headers.get('location'), '/?next=%2Fteam%2Fx')
  assert.equal((await fetch(`${url}/team/api/whoami`)).status, 401)

  const out = await as('/auth/logout', { method: 'POST' })
  assert.match(cookieFrom(out, 'fridolin_site'), /^fridolin_site=$/)
})

test('a removed account or changed role takes effect at once', async (t) => {
  const { url, writeUsers } = await startSite(t)
  const { cookie } = await login(url, 'sam', 'correct horse')
  const get = (path: string) => fetch(url + path, { redirect: 'manual', headers: { cookie } })
  assert.equal((await get('/team/')).status, 200)
  await new Promise(r => setTimeout(r, 20)) // a new mtime
  writeUsers([ADMIN, { ...MEMBER, role: 'guest' }])
  assert.equal((await get('/team/')).status, 403)
  await new Promise(r => setTimeout(r, 20))
  writeUsers([ADMIN])
  assert.equal((await get('/team/')).status, 302)
  assert.equal((await get('/auth/me')).status, 401)
})

test('password guessing is slowed down', async (t) => {
  const { url } = await startSite(t)
  for (let i = 0; i < 5; i++) assert.equal((await login(url, 'sam', `nope${i}`)).status, 401)
  assert.equal((await login(url, 'sam', 'correct horse')).status, 429)
})

// 'correct horse' as Node's bcrypt package ($2b$) and htpasswd -B ($2y$) write it.
const BCRYPT_HASHES = [
  '$2b$10$F48X9LFXjtA2Te5nMw.Y/emwxyBEOJIqP85ZEYE.5adtF22W1jdn2',
  '$2y$10$EnO0mXYizI2nJfRSqXElNO33lcLFrcqB5dz8Nd9xmKfPn6jYf3mZq',
]
const brought = (i: number) => ({ id: `u_old${i}`, kind: 'password', username: `old${i}`, name: 'Old', role: 'member', password_hash: BCRYPT_HASHES[i] })

test('an account brought over with a bcrypt hash signs in with its old password', async (t) => {
  const { url, writeUsers } = await startSite(t)
  writeUsers([ADMIN, brought(0), brought(1)])
  for (const name of ['old0', 'old1']) {
    assert.equal((await login(url, name, 'wrong horse')).status, 401)
    const ok = await login(url, name, 'correct horse')
    assert.equal(ok.status, 200)
    assert.ok(ok.cookie)
  }
})

test('guesses sent at once are all counted, though checking a hash waits', async (t) => {
  const { url, writeUsers } = await startSite(t)
  writeUsers([ADMIN, brought(0)])
  const all = await Promise.all(Array.from({ length: 8 }, (_, i) => login(url, 'old0', `nope${i}`)))
  assert.deepEqual(all.map(r => r.status).sort(), [401, 401, 401, 401, 401, 429, 429, 429])
  assert.equal((await login(url, 'old0', 'correct horse')).status, 429)
})

test('Google sign-in: state, PKCE, verified email, known account only', async (t) => {
  const { url, googleCalls } = await startSite(t)
  const start = await fetch(`${url}/auth/google?next=%2Fteam%2F`, { redirect: 'manual' })
  assert.equal(start.status, 302)
  const to = new URL(start.headers.get('location')!)
  assert.equal(to.origin, 'https://accounts.google.com')
  assert.equal(to.searchParams.get('redirect_uri'), `${url}/auth/callback`)
  assert.equal(to.searchParams.get('code_challenge_method'), 'S256')
  const state = to.searchParams.get('state')!
  const oauth = cookieFrom(start, 'fridolin_oauth')
  assert.ok(state && oauth)

  // A wrong state is refused before Google is asked anything.
  const wrong = await fetch(`${url}/auth/callback?code=the-code&state=other`, { headers: { cookie: oauth }, redirect: 'manual' })
  assert.equal(wrong.status, 400)
  assert.equal(googleCalls.length, 0)

  const cb = await fetch(`${url}/auth/callback?code=the-code&state=${state}`, { headers: { cookie: oauth }, redirect: 'manual' })
  assert.equal(cb.status, 302)
  assert.equal(cb.headers.get('location'), '/team/')
  const session = cookieFrom(cb, 'fridolin_site')
  const me = await (await fetch(`${url}/auth/me`, { headers: { cookie: session } })).json() as { email: string; role: string; picture: string }
  assert.deepEqual([me.email, me.role, me.picture], ['owner@example.com', 'admin', 'https://example.com/p.png'])
  assert.equal((await fetch(`${url}/secret/`, { headers: { cookie: session } })).status, 200)
})

test('Google sign-in refuses unknown and unverified addresses', async (t) => {
  for (const [user, status] of [[{ email: 'stranger@example.com', email_verified: true }, 403], [{ email: 'owner@example.com', email_verified: false }, 502]] as const) {
    const { url } = await startSite(t, user)
    const start = await fetch(`${url}/auth/google`, { redirect: 'manual' })
    const state = new URL(start.headers.get('location')!).searchParams.get('state')
    const cb = await fetch(`${url}/auth/callback?code=the-code&state=${state}`, { headers: { cookie: cookieFrom(start, 'fridolin_oauth') }, redirect: 'manual' })
    assert.equal(cb.status, status)
    assert.equal(cookieFrom(cb, 'fridolin_site'), '')
  }
})

test('signed values and redirects', () => {
  const key = randomBytes(32)
  const v = sign(key, { uid: 'x', exp: Math.floor(Date.now() / 1000) + 60 })
  assert.deepEqual(verify<{ uid: string }>(key, v)?.uid, 'x')
  assert.equal(verify(key, v.slice(0, -2) + 'AA'), null)
  assert.equal(verify(randomBytes(32), v), null)
  assert.equal(verify(key, sign(key, { uid: 'x', exp: 1 })), null)
  assert.equal(safeNext('/team/'), '/team/')
  for (const bad of ['//evil.example', 'https://evil.example', '/\\evil.example', '', undefined]) assert.equal(safeNext(bad), '/')
})
