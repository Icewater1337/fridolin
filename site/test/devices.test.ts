// The phones' routes: pairing and signed transcript uploads (devices.ts).
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createHash, generateKeyPairSync, sign, type KeyObject } from 'node:crypto'
import type { AddressInfo } from 'node:net'
import express from 'express'
import { deviceRoutes, type Jwk } from '../server/devices.ts'

const b64 = (v: object) => Buffer.from(JSON.stringify(v)).toString('base64url')

function phone() {
  const { privateKey, publicKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' })
  const { kty, crv, x, y } = publicKey.export({ format: 'jwk' }) as Jwk
  return { key: privateKey, jwk: { kty, crv, x, y } as Jwk }
}

function jwt(key: KeyObject, claims: object): string {
  const input = `${b64({ alg: 'ES256', typ: 'JWT' })}.${b64(claims)}`
  return `${input}.${sign('sha256', Buffer.from(input), { key, dsaEncoding: 'ieee-p1363' }).toString('base64url')}`
}

const now = () => Math.floor(Date.now() / 1000)

/** The routes on a port, with a stand-in for the bridge that answers every request in the spool. */
async function start(t: import('node:test').TestContext, answer: (req: { op: string }) => object = r => (r.op === 'pair' ? { ok: true, device: 'dev_1' } : { ok: true })) {
  const dir = mkdtempSync(join(tmpdir(), 'fridolin-devices-'))
  const spool = join(dir, 'spool')
  const devices = join(dir, 'devices.json')
  const app = express()
  app.use(deviceRoutes(devices, spool))
  const server = app.listen(0, '127.0.0.1')
  await new Promise(r => server.once('listening', r))
  const seen: { op: string; assertion: string; body?: string }[] = []
  const bridge = setInterval(() => {
    for (const e of existsSync(spool) ? readdirSync(spool) : []) {
      const req = join(spool, e, 'device.json')
      if (!existsSync(req) || existsSync(join(spool, e, 'result.json'))) continue
      const r = JSON.parse(readFileSync(req, 'utf8'))
      const body = join(spool, e, 'body.txt')
      seen.push({ ...r, body: existsSync(body) ? readFileSync(body, 'utf8') : undefined })
      writeFileSync(join(spool, e, 'result.json'), JSON.stringify(answer(r)))
    }
  }, 20)
  t.after(() => { clearInterval(bridge); server.close(); rmSync(dir, { recursive: true, force: true }) })
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  return { url, seen, pairDevice: (id: string, jwk: Jwk) => writeFileSync(devices, JSON.stringify({ devices: [{ id, key: jwk }] })) }
}

const pair = (url: string, assertion: string) => fetch(`${url}/_fridolin/devices/pair`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ assertion }),
})

function upload(url: string, key: KeyObject, text: string, claims: object = {}) {
  const sha256 = createHash('sha256').update(text).digest('base64url')
  const token = jwt(key, { iss: 'dev_1', aud: 'fridolin:plaud-transcript', iat: now(), exp: now() + 300, name: '2026-10-18_10-00-00', sha256, ...claims })
  return fetch(`${url}/_fridolin/devices/plaud/transcripts`, { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'text/plain; charset=utf-8' }, body: text })
}

test('a phone pairs with a request signed by the key it pairs', async (t) => {
  const { url, seen } = await start(t)
  const p = phone()
  const res = await pair(url, jwt(p.key, { aud: 'fridolin:pair', iat: now(), exp: now() + 300, code: 'abc', key: p.jwk }))
  assert.equal(res.status, 200)
  assert.deepEqual(await res.json(), { device: 'dev_1' })
  assert.equal(seen.length, 1)
  assert.equal(seen[0].op, 'pair')

  // Signed by another key than the one it carries: refused here, never spooled.
  const other = phone()
  const forged = await pair(url, jwt(other.key, { aud: 'fridolin:pair', iat: now(), exp: now() + 300, code: 'abc', key: p.jwk }))
  assert.equal(forged.status, 400)
  assert.equal(seen.length, 1)
})

test('an upload reaches the bridge only when a paired phone signed this very text', async (t) => {
  const { url, seen, pairDevice } = await start(t)
  const p = phone()
  pairDevice('dev_1', p.jwk)
  const text = '[00:00 - 00:04] Speaker 1: Ünïcode stays.\n'

  const res = await upload(url, p.key, text)
  assert.equal(res.status, 201)
  assert.deepEqual(seen.map(s => [s.op, s.body]), [['transcript', text]])

  const cases: [string, Promise<Response>, number][] = [
    ['another text', upload(url, p.key, 'something else', { sha256: createHash('sha256').update(text).digest('base64url') }), 400],
    ['an unpaired phone', upload(url, phone().key, text), 401],
    ['another device id', upload(url, p.key, text, { iss: 'dev_2' }), 401],
    ['another audience', upload(url, p.key, text, { aud: 'fridolin:pair' }), 401],
    ['an expired JWT', upload(url, p.key, text, { iat: now() - 900, exp: now() - 600 }), 401],
    ['a JWT that lives too long', upload(url, p.key, text, { exp: now() + 3600 }), 401],
  ]
  for (const [what, r, status] of cases) assert.equal((await r).status, status, what)
  assert.equal(seen.length, 1, 'none of them was spooled')
})

test('the bridge’s refusal reaches the phone', async (t) => {
  const { url, pairDevice } = await start(t, () => ({ ok: false, error: 'the pairing code does not work' }))
  const p = phone()
  pairDevice('dev_1', p.jwk)
  const res = await pair(url, jwt(p.key, { aud: 'fridolin:pair', iat: now(), exp: now() + 300, code: 'used', key: p.jwk }))
  assert.equal(res.status, 403)
  assert.match(((await res.json()) as { error: string }).error, /pairing code does not work/)
})

test('pairing attempts Fridolin refuses count as failures', async (t) => {
  const { url } = await start(t, () => ({ ok: false, error: 'the pairing code does not work' }))
  const attempt = () => { const p = phone(); return pair(url, jwt(p.key, { aud: 'fridolin:pair', iat: now(), exp: now() + 300, code: 'guess', key: p.jwk })) }
  for (let i = 0; i < 10; i++) assert.equal((await attempt()).status, 403)
  assert.equal((await attempt()).status, 429)
})

test('a client whose requests keep failing waits', async (t) => {
  const { url } = await start(t)
  const p = phone()
  for (let i = 0; i < 10; i++) assert.equal((await upload(url, p.key, 'x')).status, 401)
  assert.equal((await upload(url, p.key, 'x')).status, 429)
})
