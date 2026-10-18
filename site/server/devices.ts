// Phones paired with Fridolin: the Plaud Sync app pairs here and uploads its
// transcripts (docs/PLAUD.md, "Through your site").
//
//   POST /_fridolin/devices/pair                   {"assertion": JWT}
//   POST /_fridolin/devices/plaud/transcripts      Authorization: Bearer JWT, the transcript as the body
//
// Each JWT is ES256, signed with a key the phone made in its keystore: the
// pairing one with the key it pairs, which it carries, an upload with the
// key Fridolin keeps for that phone (devices.json, which Fridolin writes).
// This process checks them to turn junk away before it reaches the spool;
// the bridge checks them again, since module code runs in this process.
// Neither route needs a session: the signature is the sign-in.

import express from 'express'
import { createHash, createPublicKey, randomBytes, verify } from 'node:crypto'
import { mkdirSync, readFileSync, renameSync, statSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { awaitAnswer, RefusedError } from './ingest.ts'

export type Jwk = { kty: string; crv: string; x: string; y: string }
type Device = { id: string; key: Jwk }
type Claims = { iss?: string; aud?: string; iat?: number; exp?: number; code?: string; key?: Jwk; name?: string; sha256?: string }

const PAIR_AUD = 'fridolin:pair'
const TRANSCRIPT_AUD = 'fridolin:plaud-transcript'
const MAX_AGE_S = 600
const SKEW_S = 60
const TRANSCRIPT_MAX = 512 * 1024

/** The claims of an ES256 JWT, unchecked: they name the key to check it with. */
export function jwtClaims(token: string): Claims | null {
  const parts = token.split('.')
  if (parts.length !== 3) return null
  try {
    const header = JSON.parse(Buffer.from(parts[0], 'base64url').toString('utf8')) as { alg?: string }
    if (header.alg !== 'ES256') return null
    const claims = JSON.parse(Buffer.from(parts[1], 'base64url').toString('utf8')) as unknown
    return typeof claims === 'object' && claims !== null ? claims as Claims : null
  } catch {
    return null
  }
}

/** Whether key signed token, for aud, and it is current. */
export function verifyJwt(token: string, key: Jwk, aud: string, now = Date.now() / 1000): boolean {
  const claims = jwtClaims(token)
  if (!claims || claims.aud !== aud || typeof claims.iat !== 'number' || typeof claims.exp !== 'number') return false
  if (claims.exp < now - SKEW_S || claims.iat > now + SKEW_S || claims.exp - claims.iat > MAX_AGE_S) return false
  if (key?.kty !== 'EC' || key.crv !== 'P-256') return false
  const dot = token.lastIndexOf('.')
  try {
    const pub = createPublicKey({ key: { kty: 'EC', crv: 'P-256', x: key.x, y: key.y }, format: 'jwk' })
    return verify('sha256', Buffer.from(token.slice(0, dot)), { key: pub, dsaEncoding: 'ieee-p1363' }, Buffer.from(token.slice(dot + 1), 'base64url'))
  } catch {
    return false
  }
}

/** devices.json, re-read whenever it changes. */
class DeviceList {
  private mtimeMs = -1
  private devices: Device[] = []

  constructor(private path: string) {}

  byId(id: string | undefined): Device | undefined {
    let mtimeMs = -1
    try { mtimeMs = statSync(this.path).mtimeMs } catch { this.devices = [] }
    if (mtimeMs >= 0 && mtimeMs !== this.mtimeMs) {
      try {
        this.devices = (JSON.parse(readFileSync(this.path, 'utf8')) as { devices?: Device[] }).devices ?? []
        this.mtimeMs = mtimeMs
      } catch { /* half-written: keep the list we had */ }
    }
    return this.devices.find(d => d.id === id)
  }
}

/** Puts a request for the bridge into the spool and waits for its answer. */
async function handOver(spoolDir: string, request: object, body?: Buffer): Promise<Record<string, unknown>> {
  const dir = join(spoolDir, randomBytes(12).toString('hex'))
  mkdirSync(dir, { recursive: true })
  if (body) writeFileSync(join(dir, 'body.txt'), body)
  // The bridge reads device.json only once it is whole, and body.txt is there by then.
  writeFileSync(join(dir, 'device.json.part'), JSON.stringify(request))
  renameSync(join(dir, 'device.json.part'), join(dir, 'device.json'))
  return awaitAnswer(dir, 'Fridolin refused the request')
}

export function deviceRoutes(devicesFile: string, spoolDir: string): express.Router {
  const devices = new DeviceList(devicesFile)
  // Requests that failed their check, per client: ten, and it waits 15 minutes.
  const failures = new Map<string, { n: number; until: number }>()
  const blocked = (ip: string) => (failures.get(ip)?.until ?? 0) > Date.now()
  const fail = (ip: string) => {
    const f = failures.get(ip) ?? { n: 0, until: 0 }
    if (++f.n >= 10) { f.until = Date.now() + 15 * 60_000; f.n = 0 }
    failures.set(ip, f)
  }
  const refuse = (req: express.Request, res: express.Response, status: number, error: string) => {
    fail(req.ip ?? '')
    res.status(status).json({ error })
  }
  const answer = async (req: express.Request, res: express.Response, run: () => Promise<Record<string, unknown>>, ok: (r: Record<string, unknown>) => void) => {
    try {
      ok(await run())
    } catch (err) {
      // A refusal counts as a failure too: a pairing request needs only a key anyone can make.
      if (err instanceof RefusedError) fail(req.ip ?? '')
      res.status(err instanceof RefusedError ? 403 : 503).json({ error: (err as Error).message })
    }
  }

  const router = express.Router()
  router.use('/_fridolin/devices', (req, res, next) => {
    if (blocked(req.ip ?? '')) {
      res.status(429).json({ error: 'Too many failed requests — try again in 15 minutes' })
      return
    }
    next()
  })

  router.post('/_fridolin/devices/pair', express.json({ limit: '8kb' }), async (req, res) => {
    const assertion = (req.body as { assertion?: unknown } | undefined)?.assertion
    const claims = typeof assertion === 'string' ? jwtClaims(assertion) : null
    // Signed with the key it pairs, which shows the phone holds it.
    if (!claims?.key || typeof claims.code !== 'string' || !verifyJwt(assertion as string, claims.key, PAIR_AUD)) {
      refuse(req, res, 400, 'Not a valid pairing request')
      return
    }
    await answer(req, res, () => handOver(spoolDir, { op: 'pair', assertion }), r => res.json({ device: r.device }))
  })

  router.post('/_fridolin/devices/plaud/transcripts', express.raw({ type: () => true, limit: TRANSCRIPT_MAX }), async (req, res) => {
    const token = /^Bearer (\S+)$/.exec(req.get('authorization') ?? '')?.[1] ?? ''
    const claims = jwtClaims(token)
    const device = devices.byId(claims?.iss)
    if (!claims || !device || !verifyJwt(token, device.key, TRANSCRIPT_AUD)) {
      refuse(req, res, 401, 'Not signed by a paired phone')
      return
    }
    const body = Buffer.isBuffer(req.body) ? req.body : Buffer.alloc(0)
    if (claims.sha256 !== createHash('sha256').update(body).digest('base64url')) {
      refuse(req, res, 400, 'The signature is for another transcript')
      return
    }
    await answer(req, res, () => handOver(spoolDir, { op: 'transcript', assertion: token }, body), () => res.status(201).json({ ok: true }))
  })
  return router
}
