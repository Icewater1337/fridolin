// Sign-in for the site: Google accounts and password accounts that Fridolin's
// dashboard set up, and a signed session cookie.
//
// The cookie only names the account (its id). Every request looks the account
// up again in users.json, so removing someone or changing their role takes
// effect at once — there is no session to revoke.

import { createHash, createHmac, pbkdf2Sync, randomBytes, timingSafeEqual } from 'node:crypto'
import { readFileSync } from 'node:fs'
import bcrypt from 'bcryptjs'
import express from 'express'
import type { GoogleClient, SiteUser, UserStore } from './config.ts'

const SESSION_COOKIE = 'fridolin_site'
const OAUTH_COOKIE = 'fridolin_oauth'
const SESSION_DAYS = 7

/** What module code sees as the signed-in user (same shape as before modules moved here). */
export type SessionUser = {
  id: string
  email: string
  name: string
  role: string
  authType: 'google' | 'password'
  picture?: string
}

export function loadSessionKey(path: string): Buffer {
  const hex = readFileSync(path, 'utf8').trim()
  const key = Buffer.from(hex, 'hex')
  if (key.length < 32) throw new Error(`${path} must hold at least 32 random bytes as hex`)
  return key
}

// ── signed values ─────────────────────────────────────────────────────────────

export function sign(key: Buffer, payload: object): string {
  const body = Buffer.from(JSON.stringify(payload)).toString('base64url')
  const mac = createHmac('sha256', key).update(body).digest('base64url')
  return `${body}.${mac}`
}

export function verify<T>(key: Buffer, value: string | undefined): T | null {
  if (!value) return null
  const dot = value.lastIndexOf('.')
  if (dot <= 0) return null
  const body = value.slice(0, dot)
  const mac = Buffer.from(value.slice(dot + 1), 'base64url')
  const want = createHmac('sha256', key).update(body).digest()
  if (mac.length !== want.length || !timingSafeEqual(mac, want)) return null
  try {
    const payload = JSON.parse(Buffer.from(body, 'base64url').toString('utf8')) as T & { exp?: number }
    if (typeof payload.exp === 'number' && payload.exp < Date.now() / 1000) return null
    return payload
  } catch {
    return null
  }
}

// ── passwords ─────────────────────────────────────────────────────────────────

/** A bcrypt hash an account was brought over with from another app (Fridolin checks the format). */
const BCRYPT_RE = /^\$2[aby]\$\d\d\$[./A-Za-z0-9]{53}$/

/**
 * Checks a password against what Fridolin stored: its own
 * pbkdf2-sha256$<iterations>$<salt b64>$<hash b64>, or a bcrypt hash an
 * account was brought over with, so its owner keeps the password they had.
 */
export async function checkPassword(password: string, stored: string | undefined): Promise<boolean> {
  if (stored && BCRYPT_RE.test(stored)) return bcrypt.compare(password, stored)
  const parts = (stored ?? '').split('$')
  if (parts.length !== 4 || parts[0] !== 'pbkdf2-sha256') return false
  const iterations = Number(parts[1])
  if (!Number.isInteger(iterations) || iterations < 100_000) return false
  const salt = Buffer.from(parts[2], 'base64')
  const want = Buffer.from(parts[3], 'base64')
  if (want.length < 32) return false
  const got = pbkdf2Sync(password, salt, iterations, want.length, 'sha256')
  return timingSafeEqual(got, want)
}

/** For tests and tools: the format Fridolin writes. */
export function hashPassword(password: string, iterations = 600_000): string {
  const salt = randomBytes(16)
  const hash = pbkdf2Sync(password, salt, iterations, 32, 'sha256')
  return `pbkdf2-sha256$${iterations}$${salt.toString('base64')}$${hash.toString('base64')}`
}

// ── cookies ───────────────────────────────────────────────────────────────────

export function readCookie(req: express.Request, name: string): string | undefined {
  for (const part of (req.headers.cookie ?? '').split(';')) {
    const i = part.indexOf('=')
    if (i > 0 && part.slice(0, i).trim() === name) return decodeURIComponent(part.slice(i + 1).trim())
  }
  return undefined
}

function setCookie(res: express.Response, name: string, value: string, maxAgeSec: number, secure: boolean) {
  const attrs = [`${name}=${encodeURIComponent(value)}`, 'Path=/', 'HttpOnly', 'SameSite=Lax', `Max-Age=${maxAgeSec}`]
  if (secure) attrs.push('Secure')
  res.append('Set-Cookie', attrs.join('; '))
}

// ── the auth layer ────────────────────────────────────────────────────────────

export type AuthOptions = {
  key: Buffer
  users: UserStore
  google: () => GoogleClient | null
  /** "https://example.com"; empty means derive it from the request (local use). */
  publicOrigin: string
  /** Swappable for tests: Google's token and userinfo calls. */
  googleFetch?: typeof fetch
}

export type Auth = {
  router: express.Router
  /** Puts the signed-in user on the request, or redirects to the sign-in page. */
  requireAuth: express.RequestHandler
  /** The signed-in user, or undefined. */
  current: (req: express.Request) => SessionUser | undefined
}

type SessionCookie = { uid: string; exp: number; pic?: string }
type OAuthCookie = { state: string; verifier: string; next: string; exp: number }

const GOOGLE_AUTH = 'https://accounts.google.com/o/oauth2/v2/auth'
const GOOGLE_TOKEN = 'https://oauth2.googleapis.com/token'
const GOOGLE_USERINFO = 'https://openidconnect.googleapis.com/v1/userinfo'

export function createAuth(opts: AuthOptions): Auth {
  const doFetch = opts.googleFetch ?? fetch
  const origin = (req: express.Request) => opts.publicOrigin || `${req.protocol}://${req.get('host')}`
  const secure = (req: express.Request) => origin(req).startsWith('https://')

  // Failed password sign-ins per username and per client, to slow down guessing.
  const failures = new Map<string, { n: number; until: number }>()
  const blocked = (k: string) => (failures.get(k)?.until ?? 0) > Date.now()
  const fail = (k: string) => {
    const f = failures.get(k) ?? { n: 0, until: 0 }
    f.n++
    if (f.n >= 5) { f.until = Date.now() + 15 * 60_000; f.n = 0 }
    failures.set(k, f)
  }

  function toSession(u: SiteUser, picture?: string): SessionUser {
    return {
      id: u.id,
      email: (u.kind === 'google' ? u.email : u.username) ?? '',
      name: u.name || (u.kind === 'google' ? u.email : u.username) || '',
      role: u.role,
      authType: u.kind,
      picture,
    }
  }

  function current(req: express.Request): SessionUser | undefined {
    const cached = (req as express.Request & { user?: SessionUser }).user
    if (cached) return cached
    const s = verify<SessionCookie>(opts.key, readCookie(req, SESSION_COOKIE))
    if (!s) return undefined
    const u = opts.users.byId(s.uid)
    return u ? toSession(u, s.pic) : undefined
  }

  function startSession(req: express.Request, res: express.Response, u: SiteUser, picture?: string) {
    const exp = Math.floor(Date.now() / 1000) + SESSION_DAYS * 86400
    setCookie(res, SESSION_COOKIE, sign(opts.key, { uid: u.id, exp, pic: picture }), SESSION_DAYS * 86400, secure(req))
  }

  const requireAuth: express.RequestHandler = (req, res, next) => {
    const user = current(req)
    if (!user) {
      if (req.path.startsWith('/api/') || req.originalUrl.includes('/api/')) {
        res.status(401).json({ error: 'Sign in first' })
        return
      }
      res.redirect(`/?next=${encodeURIComponent(req.originalUrl)}`)
      return
    }
    ;(req as express.Request & { user?: SessionUser }).user = user
    next()
  }

  const router = express.Router()

  router.get('/auth/me', (req, res) => {
    const user = current(req)
    if (!user) {
      res.status(401).json({
        error: 'Sign in first',
        google: Boolean(opts.google()),
        password: opts.users.hasPasswordUsers(),
      })
      return
    }
    res.json(user)
  })

  router.post('/auth/login', express.json({ limit: '4kb' }), async (req, res) => {
    const { username, password } = (req.body ?? {}) as { username?: unknown; password?: unknown }
    if (typeof username !== 'string' || typeof password !== 'string' || !username || !password) {
      res.status(400).json({ error: 'Username and password, please' })
      return
    }
    const keys = [`u:${username.toLowerCase()}`, `ip:${req.ip}`]
    if (keys.some(blocked)) {
      res.status(429).json({ error: 'Too many failed attempts — try again in 15 minutes' })
      return
    }
    // Counted before the check: it waits for the hash, and guesses sent at
    // once must not all get past the limit while it does.
    keys.forEach(fail)
    const u = opts.users.byUsername(username)
    if (!u || !(await checkPassword(password, u.password_hash))) {
      res.status(401).json({ error: 'Wrong username or password' })
      return
    }
    keys.forEach(k => failures.delete(k))
    startSession(req, res, u)
    res.json({ ok: true })
  })

  router.post('/auth/logout', (req, res) => {
    setCookie(res, SESSION_COOKIE, '', 0, secure(req))
    res.json({ ok: true })
  })

  router.get('/auth/google', (req, res) => {
    const client = opts.google()
    if (!client) {
      res.status(503).send('Google sign-in is not set up for this site.')
      return
    }
    const state = randomBytes(16).toString('base64url')
    const verifier = randomBytes(32).toString('base64url')
    const next = safeNext(req.query.next)
    const exp = Math.floor(Date.now() / 1000) + 600
    setCookie(res, OAUTH_COOKIE, sign(opts.key, { state, verifier, next, exp }), 600, secure(req))
    const params = new URLSearchParams({
      client_id: client.client_id,
      redirect_uri: `${origin(req)}/auth/callback`,
      response_type: 'code',
      scope: 'openid email profile',
      state,
      code_challenge: createHash('sha256').update(verifier).digest('base64url'),
      code_challenge_method: 'S256',
      prompt: 'select_account',
    })
    res.redirect(`${GOOGLE_AUTH}?${params}`)
  })

  router.get('/auth/callback', async (req, res) => {
    const client = opts.google()
    const pending = verify<OAuthCookie>(opts.key, readCookie(req, OAUTH_COOKIE))
    setCookie(res, OAUTH_COOKIE, '', 0, secure(req))
    if (!client || !pending || typeof req.query.code !== 'string' || req.query.state !== pending.state) {
      res.status(400).send(page('Sign-in did not finish', 'The sign-in link expired or was opened twice. Go back and try again.'))
      return
    }
    try {
      const tokenRes = await doFetch(GOOGLE_TOKEN, {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({
          code: req.query.code,
          client_id: client.client_id,
          client_secret: client.client_secret,
          redirect_uri: `${origin(req)}/auth/callback`,
          grant_type: 'authorization_code',
          code_verifier: pending.verifier,
        }),
      })
      const token = await tokenRes.json() as { access_token?: string; error?: string }
      if (!token.access_token) throw new Error(token.error || `token endpoint answered ${tokenRes.status}`)
      const infoRes = await doFetch(GOOGLE_USERINFO, { headers: { Authorization: `Bearer ${token.access_token}` } })
      const info = await infoRes.json() as { email?: string; email_verified?: boolean; picture?: string }
      if (!info.email || info.email_verified !== true) throw new Error('Google did not return a verified email address')
      const u = opts.users.byGoogleEmail(info.email)
      if (!u) {
        res.status(403).send(page('No access', `${escapeHtml(info.email)} has no account on this site. Ask the site's owner to add it.`))
        return
      }
      startSession(req, res, u, typeof info.picture === 'string' ? info.picture : undefined)
      res.redirect(pending.next)
    } catch (err) {
      console.error('[site] Google sign-in failed:', (err as Error).message)
      res.status(502).send(page('Sign-in failed', 'Google sign-in did not work. Try again in a moment.'))
    }
  })

  return { router, requireAuth, current }
}

/** Only same-site paths: never an absolute URL or "//host". */
export function safeNext(v: unknown): string {
  if (typeof v !== 'string' || !v.startsWith('/') || v.startsWith('//') || v.startsWith('/\\')) return '/'
  return v
}

export function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]!))
}

/** A plain message page (sign-in errors). */
export function page(title: string, message: string): string {
  return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">` +
    `<title>${escapeHtml(title)}</title><link rel="stylesheet" href="/_fridolin/app.css"></head>` +
    `<body><main class="message"><h1>${escapeHtml(title)}</h1><p>${message}</p><p><a href="/">Back to the site</a></p></main></body></html>`
}
