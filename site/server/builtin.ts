// The site's built-in pages: /files and /wiki (docs/FILES.md).
//
// Every Fridolin's site has them, with or without a repository of
// modules. The pages are the site's own (web/builtin.js); what they show
// comes from the bridge, which this process asks on its site listener with
// the token it got through the secrets pipe, naming the signed-in account.
// The bridge decides what that account's role may read, holds every path
// to the brain folders and audits each view: this process only passes
// requests on, for signed-in people, and answers back what the bridge says.

import express from 'express'
import { Readable } from 'node:stream'
import type { SessionUser } from './auth.ts'

/** The bridge's site listener: its address and token. */
export type Bridge = { url: string; token: string }

/** First path segments the built-in pages take. */
export const BUILTIN_ROUTES = ['files', 'wiki']

/** What the pages may ask, as /_fridolin/api/<path>: the bridge's /site-api/<path>. */
const CALLS = [
  /^files\/(roots|list|search|view|raw)$/,
  /^wiki$/,
  /^wiki\/search$/,
  /^wiki\/[a-z0-9][a-z0-9_-]{0,39}\/(pages|page)$/,
]

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

  router.get('/_fridolin/api/*path', opts.requireAuth, async (req, res) => {
    const p = ([] as string[]).concat((req.params as { path: string | string[] }).path).join('/')
    if (!CALLS.some(re => re.test(p))) {
      res.status(404).json({ error: 'no such call' })
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
    let upstream: Response
    try {
      upstream = await doFetch(url.href, { headers, signal: abort.signal, redirect: 'error' })
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
