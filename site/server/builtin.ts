// The site's built-in pages: /files and /wiki (docs/FILES.md).
//
// Every Fridolin's site has them, with or without a repository of
// modules. The pages are the site's own (web/builtin.js); what they show
// comes from the bridge, which this process asks on its site listener with
// the token it got through the secrets pipe, naming the signed-in account.
// The bridge decides what that account's role may read and change, holds
// every path to the brain folders and audits each view and change: this
// process only passes requests on, for signed-in people, and answers back
// what the bridge says. A change must carry X-Fridolin-Site, which a form
// or a link of another site cannot send.

import express from 'express'
import { Readable } from 'node:stream'
import type { SessionUser } from './auth.ts'

/** The bridge's site listener: its address and token. */
export type Bridge = { url: string; token: string }

/** First path segments the built-in pages take. */
export const BUILTIN_ROUTES = ['files', 'wiki']

/** What the pages may ask, as /_fridolin/api/<path>: the bridge's /site-api/<path>. */
const CALLS = [
  /^files\/(roots|list|search|view|raw|history)$/,
  /^wiki$/,
  /^wiki\/search$/,
  /^wiki\/[a-z0-9][a-z0-9_-]{0,39}\/(pages|page)$/,
]

/** The changes the pages may send, by method and path, with their body and its limit. */
const WRITES: Record<string, { type: string; max: number }> = {
  'PUT files/text': { type: 'application/json', max: 1 << 20 },
  'POST files/move': { type: 'application/json', max: 1 << 20 },
  'POST files/delete': { type: 'application/json', max: 1 << 20 },
  'POST files/upload': { type: 'multipart/form-data', max: 1 << 30 },
}

/** Response headers passed back from the bridge. */
const PASS_HEADERS = ['content-type', 'content-length', 'content-disposition', 'content-range', 'accept-ranges', 'last-modified', 'etag', 'content-security-policy']

export function builtinRoutes(opts: {
  bridge?: Bridge
  requireAuth: express.RequestHandler
  shell: express.RequestHandler
  page: (res: express.Response) => void
  fetch?: typeof fetch
}): express.Router {
  const router = express.Router()
  const doFetch = opts.fetch ?? fetch

  router.all('/_fridolin/api/*path', opts.requireAuth, async (req, res) => {
    const p = ([] as string[]).concat((req.params as { path: string | string[] }).path).join('/')
    const write = WRITES[`${req.method} ${p}`]
    if (req.method === 'GET' || req.method === 'HEAD') {
      if (!CALLS.some(re => re.test(p))) {
        res.status(404).json({ error: 'no such call' })
        return
      }
    } else if (!write) {
      res.status(404).json({ error: 'no such call' })
      return
    } else if (req.get('x-fridolin-site') !== '1') {
      res.status(403).json({ error: 'a change needs the X-Fridolin-Site header' })
      return
    } else if (!(req.get('content-type') || '').startsWith(write.type)) {
      res.status(415).json({ error: `send ${write.type}` })
      return
    } else if (Number(req.get('content-length') || 0) > write.max) {
      res.status(413).json({ error: 'too large' })
      return
    }
    if (!opts.bridge) {
      res.status(503).json({ error: 'Files and the wiki are not available here (previews have none)' })
      return
    }
    const user = (req as express.Request & { user?: SessionUser }).user!
    // The bridge's own address, with one of CALLS as the path and the
    // page's query: nothing the browser sends can name another server.
    const url = new URL(opts.bridge.url)
    url.pathname = `/site-api/${p}`
    url.search = new URL(req.originalUrl, 'http://site.invalid').search
    const headers: Record<string, string> = {
      Authorization: `Bearer ${opts.bridge.token}`,
      'X-Fridolin-Site-User': user.id,
    }
    const range = req.get('range')
    if (range) headers.Range = range
    const abort = new AbortController()
    res.on('close', () => abort.abort())
    const init: RequestInit & { duplex?: 'half' } = { method: req.method, headers, signal: abort.signal, redirect: 'error' }
    if (write) {
      // The body as it comes, never parsed here: the bridge reads it and
      // holds it to its own limits.
      headers['Content-Type'] = req.get('content-type')!
      const len = req.get('content-length')
      if (len) headers['Content-Length'] = len
      init.body = Readable.toWeb(req) as unknown as RequestInit['body']
      init.duplex = 'half'
    }
    let upstream: Response
    try {
      upstream = await doFetch(url.href, init)
    } catch {
      if (!res.headersSent) res.status(502).json({ error: 'Fridolin did not answer; is the bridge running?' })
      return
    }
    res.status(upstream.status)
    for (const h of PASS_HEADERS) {
      const v = upstream.headers.get(h)
      if (v) res.setHeader(h, v)
    }
    res.setHeader('Cache-Control', 'private, no-store')
    res.setHeader('X-Content-Type-Options', 'nosniff')
    if (!upstream.body) {
      res.end()
      return
    }
    Readable.fromWeb(upstream.body as import('node:stream/web').ReadableStream).on('error', () => res.destroy()).pipe(res)
  })

  const pages = [...BUILTIN_ROUTES.map(r => `/${r}`), ...BUILTIN_ROUTES.map(r => `/${r}/*path`)]
  router.get(pages, opts.requireAuth, opts.shell, (_req, res) => {
    res.setHeader('Cache-Control', 'no-cache')
    opts.page(res)
  })
  return router
}
