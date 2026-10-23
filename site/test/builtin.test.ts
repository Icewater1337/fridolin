// The built-in /files and /wiki: signed-in only, passed on to the bridge
// with its token and the account, nothing else asked of it.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { randomBytes } from 'node:crypto'
import type { AddressInfo } from 'node:net'
import { createSite } from '../server/app.ts'
import { readRuntime } from '../server/config.ts'
import { hashPassword } from '../server/auth.ts'
import { loadModules } from '../server/modules.ts'

const MEMBER = { id: 'u_member', kind: 'password', username: 'sam', name: 'Sam', role: 'member', password_hash: hashPassword('correct horse', 100_000) }

type Call = { url: string; headers: Headers }

async function startSite(t: import('node:test').TestContext, opts: { bridge?: boolean } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'fridolin-builtin-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  mkdirSync(join(dir, 'modules'))
  writeFileSync(join(dir, 'users.json'), JSON.stringify({ version: 1, users: [MEMBER] }))
  writeFileSync(join(dir, 'session.key'), randomBytes(32).toString('hex'))
  writeFileSync(join(dir, 'runtime.json'), JSON.stringify({
    listen: '127.0.0.1:0', title: 'Test site', modules_dir: join(dir, 'modules'),
    users_file: join(dir, 'users.json'), session_key_file: join(dir, 'session.key'),
    ...(opts.bridge === false ? {} : { bridge_url: 'http://127.0.0.1:1' }),
  }))
  const calls: Call[] = []
  const bridgeFetch = (async (input: string | URL | Request, init?: RequestInit) => {
    calls.push({ url: String(input), headers: new Headers(init?.headers) })
    if (String(input).includes('/files/raw')) {
      return new Response('%PDF-1', { status: 200, headers: { 'content-type': 'application/pdf', 'content-security-policy': "default-src 'none'", 'x-secret': 'no' } })
    }
    return new Response(JSON.stringify({ roots: [{ id: 'brain:main', brain: 'main' }] }), { status: 200, headers: { 'content-type': 'application/json' } })
  }) as typeof fetch
  const app = await createSite(readRuntime(join(dir, 'runtime.json')), {
    secrets: { _fridolin: { bridge_token: 'bridge-token' }, other: { KEY: 'v' } },
    bridgeFetch,
  })
  const server = app.listen(0, '127.0.0.1')
  await new Promise(r => server.once('listening', r))
  t.after(() => { server.close() })
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  const r = await fetch(`${url}/auth/login`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: 'sam', password: 'correct horse' }),
  })
  const cookie = r.headers.getSetCookie().find(c => c.startsWith('fridolin_site='))!.split(';')[0]
  return { url, cookie, calls }
}

test('the pages and their calls want a signed-in account', async (t) => {
  const { url, calls } = await startSite(t)
  for (const p of ['/files', '/files/main/docs/', '/wiki', '/wiki/main/people/alex']) {
    const r = await fetch(url + p, { redirect: 'manual' })
    assert.equal(r.status, 302, p)
    assert.equal(r.headers.get('location'), `/?next=${encodeURIComponent(p)}`)
  }
  assert.equal((await fetch(`${url}/_fridolin/api/files/roots`)).status, 401)
  assert.equal(calls.length, 0)
})

test('signed in, the pages load and the calls go to the bridge as the account', async (t) => {
  const { url, cookie, calls } = await startSite(t)
  const as = (p: string, init: RequestInit = {}) => fetch(url + p, { redirect: 'manual', ...init, headers: { cookie, ...(init.headers ?? {}) } })

  const page = await as('/wiki/main/people/alex')
  assert.equal(page.status, 200)
  assert.match(page.headers.get('content-security-policy') ?? '', /script-src 'self'/)
  assert.match(await page.text(), /\/_fridolin\/builtin\.js/)

  const roots = await as('/_fridolin/api/files/roots')
  assert.equal(roots.status, 200)
  assert.deepEqual(await roots.json(), { roots: [{ id: 'brain:main', brain: 'main' }] })
  assert.equal(calls[0].url, 'http://127.0.0.1:1/site-api/files/roots')
  assert.equal(calls[0].headers.get('authorization'), 'Bearer bridge-token')
  assert.equal(calls[0].headers.get('x-fridolin-site-user'), 'u_member')

  const raw = await as('/_fridolin/api/files/raw?root=brain%3Amain&path=a.pdf', { headers: { range: 'bytes=0-1' } })
  assert.equal(raw.status, 200)
  assert.equal(await raw.text(), '%PDF-1')
  assert.equal(raw.headers.get('content-security-policy'), "default-src 'none'")
  assert.equal(raw.headers.get('x-secret'), null)
  assert.equal(calls[1].url, 'http://127.0.0.1:1/site-api/files/raw?root=brain%3Amain&path=a.pdf')
  assert.equal(calls[1].headers.get('range'), 'bytes=0-1')

  await as('/_fridolin/api/wiki/main/page?path=wiki%2Fx.md')
  assert.equal(calls[2].url, 'http://127.0.0.1:1/site-api/wiki/main/page?path=wiki%2Fx.md')

  // Only the calls the pages make, nothing else of the bridge.
  for (const p of ['/_fridolin/api/files/write', '/_fridolin/api/../send', '/_fridolin/api/wiki/BAD/page', '/_fridolin/api/nodes']) {
    const r = await as(p)
    assert.equal(r.status, 404, p)
  }
  assert.equal(calls.length, 3)
})

test('a preview, without the bridge, says the pages are not there', async (t) => {
  const { url, cookie } = await startSite(t, { bridge: false })
  const r = await fetch(`${url}/_fridolin/api/files/roots`, { headers: { cookie } })
  assert.equal(r.status, 503)
})

test('no module may take /files or /wiki', () => {
  const dir = mkdtempSync(join(tmpdir(), 'fridolin-reserved-'))
  try {
    for (const id of ['files', 'wiki']) {
      mkdirSync(join(dir, id))
      writeFileSync(join(dir, id, 'module.json'), JSON.stringify({ version: 1, id, title: id, route: `/${id}`, access: { roles: ['*'] }, server: { entry: 'server.ts' } }))
    }
    const { modules, broken } = loadModules(dir)
    assert.equal(modules.length, 0)
    assert.deepEqual(broken.map(b => b.id).sort(), ['files', 'wiki'])
    assert.match(broken[0].error, /reserved by the site itself \(its built-in Files and Wiki/)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('wiki.<domain> and files.<domain> lead to the pages on <domain>', async (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'fridolin-hosts-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  mkdirSync(join(dir, 'modules'))
  writeFileSync(join(dir, 'users.json'), JSON.stringify({ version: 1, users: [] }))
  writeFileSync(join(dir, 'session.key'), randomBytes(32).toString('hex'))
  writeFileSync(join(dir, 'runtime.json'), JSON.stringify({
    listen: '127.0.0.1:0', public_origin: 'https://example.com', modules_dir: join(dir, 'modules'),
    users_file: join(dir, 'users.json'), session_key_file: join(dir, 'session.key'),
  }))
  const app = await createSite(readRuntime(join(dir, 'runtime.json')))
  const server = app.listen(0, '127.0.0.1')
  await new Promise(r => server.once('listening', r))
  t.after(() => { server.close() })
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  const { request } = await import('node:http')
  const get = (host: string, path: string) => new Promise<{ status: number; location?: string }>((resolve, reject) => {
    request(`${url}${path}`, { headers: { host } }, (res) => { res.resume(); resolve({ status: res.statusCode!, location: res.headers.location }) }).on('error', reject).end()
  })
  assert.deepEqual(await get('wiki.example.com', '/'), { status: 301, location: 'https://example.com/wiki' })
  assert.deepEqual(await get('wiki.example.com', '/acme/people/alex?x=1'), { status: 301, location: 'https://example.com/wiki/acme/people/alex?x=1' })
  assert.deepEqual(await get('wiki.example.com', '/wiki/acme/'), { status: 301, location: 'https://example.com/wiki/acme/' })
  assert.deepEqual(await get('files.example.com', '/main/'), { status: 301, location: 'https://example.com/files/main/' })
  assert.equal((await get('example.com', '/')).status, 200)
  assert.equal((await get('other.example.com', '/')).status, 200)
})
