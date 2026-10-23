// The site: sign-in, the home page with a tile per module, and the modules.

import express from 'express'
import { fileURLToPath } from 'node:url'
import { readGoogleClient, readSecrets, UserStore, type Runtime } from './config.ts'
import { createAuth, loadSessionKey, page, type Auth } from './auth.ts'
import { loadModules, mountModules, type ModuleSecrets } from './modules.ts'
import { createPreviewAuth, previewForwarding, PreviewList } from './preview.ts'
import { deviceRoutes } from './devices.ts'
import { builtinRoutes, type Bridge } from './builtin.ts'
import { BRIDGE_SECRETS_KEY } from './config.ts'

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
  /** For tests: the modules' secrets, instead of reading rt.secrets_fd. */
  secrets?: ModuleSecrets
  /** For tests: how the built-in pages reach the bridge. */
  bridgeFetch?: typeof fetch
}

export async function createSite(rt: Runtime, opts: SiteOptions = {}): Promise<express.Express> {
  // A preview knows no accounts: only its token opens it (preview.ts).
  const auth: Auth = rt.preview_token
    ? createPreviewAuth(rt.preview_token, rt.listen)
    : createAuth({
      key: loadSessionKey(rt.session_key_file),
      users: new UserStore(rt.users_file),
      // Read per sign-in, so connecting the client in Fridolin needs no restart.
      google: () => readGoogleClient(rt.google_client_file),
      publicOrigin: rt.public_origin,
      googleFetch: opts.googleFetch,
    })
  const forwarding = !rt.preview_token && rt.previews_file
    ? previewForwarding(new PreviewList(rt.previews_file), auth,
      (req) => (rt.public_origin || `${req.protocol}://`).startsWith('https://'))
    : null

  const app = express()
  // Behind cloudflared (or another proxy on this machine): trust its forwarded
  // client address and scheme, nobody else's. In the container, cloudflared's
  // requests arrive through Docker's port forwarding, from a private address.
  app.set('trust proxy', rt.container ? ['loopback', 'uniquelocal'] : 'loopback')
  app.disable('x-powered-by')
  app.use((_req, res, next) => {
    res.setHeader('X-Content-Type-Options', 'nosniff')
    res.setHeader('Referrer-Policy', 'same-origin')
    next()
  })

  // A preview shows inside Fridolin's dashboard (another port of this machine).
  const csp = rt.preview_token ? SHELL_CSP.replace("frame-ancestors 'self'", "frame-ancestors 'self' http://127.0.0.1:* http://localhost:*") : SHELL_CSP
  const shell: express.RequestHandler = (_req, res, next) => {
    res.setHeader('Content-Security-Policy', csp)
    next()
  }

  app.get('/_fridolin/health', (_req, res) => { res.type('text').send('ok') })
  // Phones sign each request themselves; the live site only (devices.ts).
  if (rt.devices_file && rt.spool_dir) app.use(deviceRoutes(rt.devices_file, rt.spool_dir))
  app.get('/_fridolin/site.json', (req, res) => {
    const forwarded = forwarding?.active(req)
    res.json({ title: rt.title, preview: rt.preview_label ?? forwarded?.entry.label ?? null, exit: Boolean(forwarded) })
  })
  // Revalidated on every load (ETag), so a new Fridolin's pages show at once.
  app.use('/_fridolin', shell, express.static(WEB_DIR, { index: false, maxAge: 0 }))

  app.use('/auth', shell)
  app.use(auth.router)
  if (forwarding) {
    app.use(forwarding.router)
    app.use(forwarding.forward)
  }

  // Only the live site has secrets; a preview runs unapproved code.
  const secrets = opts.secrets ?? (rt.secrets_fd !== undefined && !rt.preview_token ? readSecrets(rt.secrets_fd) : undefined)
  // The bridge's token for the built-in pages travels with the secrets; no
  // module gets it from there.
  const bridgeToken = secrets?.[BRIDGE_SECRETS_KEY]?.bridge_token
  if (secrets) delete secrets[BRIDGE_SECRETS_KEY]
  const bridge: Bridge | undefined = rt.bridge_url && bridgeToken ? { url: rt.bridge_url, token: bridgeToken } : undefined
  app.use(builtinRoutes({
    bridge,
    requireAuth: auth.requireAuth,
    shell,
    fetch: opts.bridgeFetch,
    page: (res) => res.sendFile('builtin.html', { root: WEB_DIR }),
  }))
  await mountModules(app, loadModules(rt.modules_dir), auth.requireAuth, secrets, { dataDir: rt.data_dir, spoolDir: rt.spool_dir })

  app.get('/', shell, (_req, res) => {
    res.setHeader('Cache-Control', 'no-cache')
    // With root, only the part below it is checked for dot folders: a preview
    // runs from a brain's .fridolin-site folder.
    res.sendFile('index.html', { root: WEB_DIR })
  })

  app.use(shell, (_req, res) => {
    res.status(404).send(page('Not found', 'There is no page here.'))
  })

  return app
}
