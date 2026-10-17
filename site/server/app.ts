// The site: sign-in, the home page with a tile per module, and the modules.

import express from 'express'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { readGoogleClient, UserStore, type Runtime } from './config.ts'
import { createAuth, loadSessionKey, page } from './auth.ts'
import { loadModules, mountModules } from './modules.ts'

const WEB_DIR = fileURLToPath(new URL('../web/', import.meta.url))

// The site's own pages run under a strict policy; module apps set their own.
const SHELL_CSP = [
  "default-src 'self'",
  "img-src 'self' https: data:",
  "style-src 'self'",
  "script-src 'self'",
  "object-src 'none'",
  "base-uri 'none'",
  "frame-ancestors 'self'",
  "form-action 'self'",
].join('; ')

export type SiteOptions = {
  /** Swappable for tests. */
  googleFetch?: typeof fetch
}

export async function createSite(rt: Runtime, opts: SiteOptions = {}): Promise<express.Express> {
  const users = new UserStore(rt.users_file)
  const auth = createAuth({
    key: loadSessionKey(rt.session_key_file),
    users,
    // Read per sign-in, so connecting the client in Fridolin needs no restart.
    google: () => readGoogleClient(rt.google_client_file),
    publicOrigin: rt.public_origin,
    googleFetch: opts.googleFetch,
  })

  const app = express()
  // Behind cloudflared (or another proxy on this machine): trust its forwarded
  // client address and scheme, nobody else's.
  app.set('trust proxy', 'loopback')
  app.disable('x-powered-by')
  app.use((_req, res, next) => {
    res.setHeader('X-Content-Type-Options', 'nosniff')
    res.setHeader('Referrer-Policy', 'same-origin')
    next()
  })

  const shell: express.RequestHandler = (_req, res, next) => {
    res.setHeader('Content-Security-Policy', SHELL_CSP)
    next()
  }

  app.get('/_fridolin/health', (_req, res) => { res.type('text').send('ok') })
  app.get('/_fridolin/site.json', (_req, res) => {
    res.json({ title: rt.title, preview: rt.preview_label ?? null })
  })
  // Revalidated on every load (ETag), so a new Fridolin's pages show at once.
  app.use('/_fridolin', shell, express.static(WEB_DIR, { index: false, maxAge: 0 }))

  app.use('/auth', shell)
  app.use(auth.router)

  await mountModules(app, loadModules(rt.modules_dir), auth.requireAuth)

  app.get('/', shell, (_req, res) => {
    res.setHeader('Cache-Control', 'no-cache')
    res.sendFile(join(WEB_DIR, 'index.html'))
  })

  app.use(shell, (_req, res) => {
    res.status(404).send(page('Not found', 'There is no page here.'))
  })

  return app
}
