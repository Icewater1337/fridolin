// Previews of a builder brain's workspace.
//
// A preview process runs module code nobody has approved yet, inside the
// brain's sandbox. It therefore gets no session key and no user list: it is
// opened only with its token, either through a link (the dashboard has it),
// or by the live site, which forwards a signed-in admin's requests to it with
// the token and the admin's identity attached.

import express from 'express'
import http from 'node:http'
import { readFileSync, statSync } from 'node:fs'
import { timingSafeEqual } from 'node:crypto'
import { readCookie, type Auth, type SessionUser } from './auth.ts'

const TOKEN_HEADER = 'x-fridolin-preview'
const USER_HEADER = 'x-fridolin-preview-user'

function sameToken(a: string | undefined, b: string): boolean {
  if (!a) return false
  const x = Buffer.from(a), y = Buffer.from(b)
  return x.length === y.length && timingSafeEqual(x, y)
}

/** The sign-in of a preview process: its token, from a link or from the live site. */
export function createPreviewAuth(token: string, listen: string): Auth {
  // Cookies do not tell ports apart, so each preview's cookie has its own name.
  const cookie = `fridolin_pv_${listen.split(':').pop()}`
  const visitor: SessionUser = { id: 'preview', email: 'preview', name: 'Preview', role: 'admin', authType: 'google' }

  function current(req: express.Request): SessionUser | undefined {
    const cached = (req as express.Request & { user?: SessionUser }).user
    if (cached) return cached
    if (sameToken(req.get(TOKEN_HEADER), token)) {
      // Forwarded by the live site: the admin who is looking.
      try {
        const u = JSON.parse(Buffer.from(req.get(USER_HEADER) ?? '', 'base64url').toString('utf8')) as SessionUser
        if (u && typeof u.role === 'string') return u
      } catch { /* fall through */ }
      return visitor
    }
    return sameToken(readCookie(req, cookie), token) ? visitor : undefined
  }

  const requireAuth: express.RequestHandler = (req, res, next) => {
    const user = current(req)
    if (!user) {
      res.status(401).send('This preview opens only with its link.')
      return
    }
    ;(req as express.Request & { user?: SessionUser }).user = user
    next()
  }

  const router = express.Router()
  router.get('/_fridolin/preview-login', (req, res) => {
    if (!sameToken(String(req.query.token ?? ''), token)) {
      res.status(403).send('This preview link is not valid (any more).')
      return
    }
    res.append('Set-Cookie', `${cookie}=${token}; Path=/; HttpOnly; SameSite=Lax; Max-Age=86400`)
    res.redirect('/')
  })
  router.get('/auth/me', (req, res) => {
    const user = current(req)
    if (user) res.json(user)
    else res.status(401).json({ error: 'This preview opens only with its link.', google: false, password: false })
  })
  return { router, requireAuth, current }
}

type PreviewEntry = { listen: string; token: string; label: string }

/** previews.json (written by Fridolin), re-read when it changes. */
export class PreviewList {
  private mtimeMs = -1
  private list: Record<string, PreviewEntry> = {}
  constructor(private path: string) {}
  get(name: string): PreviewEntry | undefined {
    try {
      const m = statSync(this.path).mtimeMs
      if (m !== this.mtimeMs) {
        this.list = JSON.parse(readFileSync(this.path, 'utf8')) as Record<string, PreviewEntry>
        this.mtimeMs = m
      }
    } catch {
      this.list = {}
      this.mtimeMs = -1
    }
    return Object.hasOwn(this.list, name) ? this.list[name] : undefined
  }
}

const PREVIEW_COOKIE = 'fridolin_preview'

/**
 * On the live site: /_fridolin/preview/<brain> switches a signed-in admin to
 * that brain's preview (a cookie), /_fridolin/preview-exit back. While the
 * cookie is set, the admin's requests outside /_fridolin and /auth go to the
 * preview process instead of the live modules.
 */
export function previewForwarding(previews: PreviewList, auth: Auth, secure: (req: express.Request) => boolean) {
  const router = express.Router()
  const isAdmin = (req: express.Request) => auth.current(req)?.role === 'admin'

  router.get('/_fridolin/preview/:name', auth.requireAuth, (req, res) => {
    const name = String(req.params.name)
    if (!isAdmin(req) || !previews.get(name)) {
      res.status(404).send('No such preview.')
      return
    }
    res.append('Set-Cookie', `${PREVIEW_COOKIE}=${encodeURIComponent(name)}; Path=/; HttpOnly; SameSite=Lax; Max-Age=86400${secure(req) ? '; Secure' : ''}`)
    res.redirect('/')
  })
  router.get('/_fridolin/preview-exit', (req, res) => {
    res.append('Set-Cookie', `${PREVIEW_COOKIE}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0`)
    res.redirect('/')
  })

  /** The preview this request should see, or null. */
  const active = (req: express.Request): { name: string; entry: PreviewEntry } | null => {
    const name = readCookie(req, PREVIEW_COOKIE)
    if (!name || !isAdmin(req)) return null
    const entry = previews.get(name)
    return entry ? { name, entry } : null
  }

  const forward: express.RequestHandler = (req, res, next) => {
    if (req.path.startsWith('/_fridolin/') || req.path.startsWith('/auth/')) { next(); return }
    const p = active(req)
    if (!p) { next(); return }
    const [host, port] = [p.entry.listen.slice(0, p.entry.listen.lastIndexOf(':')), Number(p.entry.listen.split(':').pop())]
    const headers = { ...req.headers }
    for (const h of ['connection', 'keep-alive', 'transfer-encoding', 'upgrade', 'proxy-connection', 'cookie', TOKEN_HEADER, USER_HEADER]) delete headers[h]
    headers[TOKEN_HEADER] = p.entry.token
    headers[USER_HEADER] = Buffer.from(JSON.stringify(auth.current(req))).toString('base64url')
    const up = http.request({ host, port, method: req.method, path: req.originalUrl, headers }, (upRes) => {
      res.status(upRes.statusCode ?? 502)
      for (const [k, v] of Object.entries(upRes.headers)) {
        if (v !== undefined && !['connection', 'keep-alive', 'transfer-encoding', 'set-cookie'].includes(k)) res.setHeader(k, v)
      }
      upRes.pipe(res)
    })
    up.on('error', () => { if (!res.headersSent) res.status(502).send('The preview is not running any more. Leave it at /_fridolin/preview-exit.') })
    req.pipe(up)
  }

  return { router, forward, active }
}
