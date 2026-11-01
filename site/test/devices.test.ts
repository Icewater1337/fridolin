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
      const a = answer(r) as { file?: string }
      if (a.file) writeFileSync(join(spool, e, a.file), `sealed app for ${r.op}`)
      writeFileSync(join(spool, e, 'result.json'), JSON.stringify(a))
    }
  }, 20)
  t.after(() => { clearInterval(bridge); server.close(); rmSync(dir, { recursive: true, force: true }) })
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  return { url, seen, spool, pairDevice: (id: string, jwk: Jwk) => writeFileSync(devices, JSON.stringify({ devices: [{ id, key: jwk }] })) }
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

test('a temporary failure of the bridge is a 503 the phone retries, and no failed attempt', async (t) => {
  const { url } = await start(t, () => ({ ok: false, temporary: true, error: 'too many voice questions at once; try again in a few minutes' }))
  const attempt = () => { const p = phone(); return pair(url, jwt(p.key, { aud: 'fridolin:pair', iat: now(), exp: now() + 300, code: 'x', key: p.jwk })) }
  for (let i = 0; i < 11; i++) assert.equal((await attempt()).status, 503)
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

function ask(url: string, key: KeyObject, text: string, claims: object = {}) {
  const sha256 = createHash('sha256').update(text).digest('base64url')
  const enc = phone().jwk
  const token = jwt(key, { iss: 'dev_1', aud: 'fridolin:voice', iat: now(), exp: now() + 300, rid: 'q_0123456789abcdef', sha256, enc, ...claims })
  return fetch(`${url}/_fridolin/devices/voice/ask`, { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'text/plain; charset=utf-8' }, body: text })
}

function fetchReply(url: string, key: KeyObject, claims: object = {}) {
  const token = jwt(key, { iss: 'dev_1', aud: 'fridolin:voice-reply', iat: now(), exp: now() + 300, rid: 'q_0123456789abcdef', ...claims })
  return fetch(`${url}/_fridolin/devices/voice/reply`, { method: 'POST', headers: { Authorization: `Bearer ${token}` } })
}

test('a voice question reaches the bridge only when a paired phone signed it, with a key for the answer', async (t) => {
  const { url, seen, pairDevice } = await start(t, r => (r.op === 'voice' ? { ok: true, state: 'waiting' } : { ok: true }))
  const p = phone()
  pairDevice('dev_1', p.jwk)

  const res = await ask(url, p.key, 'Was steht morgen an?')
  assert.equal(res.status, 202)
  assert.deepEqual(await res.json(), { state: 'waiting' })
  assert.equal(seen.length, 1)
  assert.equal(seen[0].op, 'voice')
  assert.equal(seen[0].body, 'Was steht morgen an?')

  // Each of these is refused here and never spooled.
  assert.equal((await ask(url, phone().key, 'hi')).status, 401, 'another key')
  assert.equal((await ask(url, p.key, 'hi', { aud: 'fridolin:plaud-transcript' })).status, 401, 'a transcript JWT')
  assert.equal((await ask(url, p.key, 'hi', { rid: 'short' })).status, 401, 'no valid question id')
  assert.equal((await ask(url, p.key, 'hi', { sha256: 'x' })).status, 400, 'another text')
  assert.equal((await ask(url, p.key, 'hi', { enc: undefined })).status, 400, 'no key for the answer')
  assert.equal(seen.length, 1)
})

test('a recording from the pin may be as long as a transcript, a question may not', async (t) => {
  const { url, seen, pairDevice } = await start(t, r => (r.op === 'voice' ? { ok: true, state: 'waiting' } : { ok: true }))
  const p = phone()
  pairDevice('dev_1', p.jwk)
  const line = '[00:00 - 00:10] Speaker 1: Wir verschieben die Bestellung auf Dezember.\n'
  const long = line.repeat(Math.ceil(100 * 1024 / line.length))
  const pin = { src: 'pin', name: '2026-10-18_10-00-00' }

  const res = await ask(url, p.key, long, pin)
  assert.equal(res.status, 202)
  assert.equal(seen.length, 1)
  assert.equal(seen[0].body, long, 'spooled whole, speaker labels and all')

  assert.equal((await ask(url, p.key, long)).status, 413, 'a question of that length')
  assert.equal((await ask(url, p.key, 'x'.repeat(512 * 1024 + 1), pin)).status, 413, 'a recording longer than a transcript')
  assert.equal((await ask(url, p.key, 'hi', { src: 'watch' })).status, 400, 'another kind')
  assert.equal((await ask(url, p.key, 'hi', { src: 'pin' })).status, 400, 'a recording without its name')
  assert.equal(seen.length, 1, 'none of them was spooled')
})

test('the answer to a voice question goes back as the bridge sealed it, and only to the phone that signs for it', async (t) => {
  const reply = { alg: 'ECDH-ES+HKDF-SHA256+A256GCM', epk: { kty: 'EC', crv: 'P-256', x: 'x', y: 'y' }, iv: 'iv', ct: 'ct' }
  const { url, seen, pairDevice } = await start(t, r => (r.op === 'voice_reply' ? { ok: true, state: 'done', reply } : { ok: true }))
  const p = phone()
  pairDevice('dev_1', p.jwk)

  const res = await fetchReply(url, p.key)
  assert.equal(res.status, 200)
  assert.deepEqual(await res.json(), { state: 'done', reply })
  assert.equal(seen.at(-1)?.op, 'voice_reply')

  const n = seen.length
  assert.equal((await fetchReply(url, phone().key)).status, 401)
  assert.equal((await fetchReply(url, p.key, { aud: 'fridolin:voice' })).status, 401)
  assert.equal(seen.length, n)
})

test('a failed voice question says why', async (t) => {
  const { url, pairDevice } = await start(t, () => ({ ok: true, state: 'failed', error: 'the question was stopped before it was answered' }))
  const p = phone()
  pairDevice('dev_1', p.jwk)
  assert.deepEqual(await (await fetchReply(url, p.key)).json(), { state: 'failed', error: 'the question was stopped before it was answered' })
})

const SHA = 'ab'.repeat(32)

function appRequest(url: string, path: 'release' | 'apk', key: KeyObject, claims: object = {}) {
  const aud = path === 'release' ? 'fridolin:app-release' : 'fridolin:app-apk'
  const base = path === 'release' ? { ver: 7, vname: '0.7' } : { apk: SHA, enc: phone().jwk }
  const token = jwt(key, { iss: 'dev_1', aud, iat: now(), exp: now() + 300, ...base, ...claims })
  return fetch(`${url}/_fridolin/devices/app/${path}`, { method: 'POST', headers: { Authorization: `Bearer ${token}` } })
}

test('a phone asks for the current build of the app, signed', async (t) => {
  const release = { version_code: 8, version_name: '0.7', sha256: SHA, cert_sha256: SHA, size: 3 }
  const { url, seen, pairDevice } = await start(t, r => (r.op === 'app_release' ? { ok: true, release } : { ok: true }))
  const p = phone()
  pairDevice('dev_1', p.jwk)
  const res = await appRequest(url, 'release', p.key)
  assert.equal(res.status, 200)
  assert.deepEqual(await res.json(), { release })
  assert.equal(seen.at(-1)?.op, 'app_release')

  const n = seen.length
  assert.equal((await appRequest(url, 'release', phone().key)).status, 401, 'an unpaired phone')
  assert.equal((await appRequest(url, 'release', p.key, { aud: 'fridolin:app-apk' })).status, 401, 'another audience')
  assert.equal(seen.length, n)
})

test('a phone fetches a build as the bridge sealed it, which then leaves the spool', async (t) => {
  const { url, seen, spool, pairDevice } = await start(t, r => (r.op === 'app_apk' ? { ok: true, file: 'app.sealed', size: 20 } : { ok: true }))
  const p = phone()
  pairDevice('dev_1', p.jwk)
  const res = await appRequest(url, 'apk', p.key)
  assert.equal(res.status, 200)
  assert.equal(res.headers.get('content-type'), 'application/octet-stream')
  assert.equal(await res.text(), 'sealed app for app_apk')
  assert.equal(seen.at(-1)?.op, 'app_apk')
  for (let i = 0; i < 50 && readdirSync(spool).length > 0; i++) await new Promise(r => setTimeout(r, 20))
  assert.deepEqual(readdirSync(spool), [], 'the sealed app is gone once sent')

  const n = seen.length
  assert.equal((await appRequest(url, 'apk', p.key, { apk: 'not a sha' })).status, 400, 'no build named')
  assert.equal((await appRequest(url, 'apk', p.key, { enc: undefined })).status, 400, 'no key to seal it for')
  assert.equal((await appRequest(url, 'apk', phone().key)).status, 401, 'an unpaired phone')
  assert.equal(seen.length, n)
})

test('the bridge’s refusal of a fetch reaches the phone', async (t) => {
  const { url, pairDevice } = await start(t, () => ({ ok: false, error: 'that build is not the current one any more: ask for the release again' }))
  const p = phone()
  pairDevice('dev_1', p.jwk)
  const res = await appRequest(url, 'apk', p.key)
  assert.equal(res.status, 403)
  assert.match((await res.json() as { error: string }).error, /not the current one/)
})
