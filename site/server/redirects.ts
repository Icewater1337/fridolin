// Redirects of old addresses, from the site repository (docs/SITE.md,
// "Old addresses"): modules/redirects.json lists paths that moved, such
// as a module's route whose pages the built-in /files and /wiki took
// over. A request for such a path, or one below it, is sent to the new
// place with the rest of its path and its query.
//
//   { "redirects": [ { "from": "/wiki-team", "to": "/wiki/team" } ] }
//
// Only paths of this site: "to" starts with one "/", and nothing of the
// site's own (/_fridolin, /auth) is taken. The longest "from" that matches
// wins. A module's own route is served first, so a redirect never hides a
// module.

import express from 'express'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

export type Redirect = { from: string; to: string }

const PATH = /^\/[A-Za-z0-9._~%!$&'()*+,;=:@-]+(\/[A-Za-z0-9._~%!$&'()*+,;=:@-]+)*$/
const OWN = ['/_fridolin', '/auth']
const plain = (p: string) => PATH.test(p) && !p.split('/').some(seg => seg === '.' || seg === '..')

/** Reads modulesDir/redirects.json; problems are thrown with what to fix. */
export function loadRedirects(modulesDir: string): Redirect[] {
  const file = join(modulesDir, 'redirects.json')
  if (!existsSync(file)) return []
  const data = JSON.parse(readFileSync(file, 'utf8')) as { redirects?: unknown }
  if (!data || !Array.isArray(data.redirects)) throw new Error('redirects.json: "redirects" must be a list')
  const out: Redirect[] = []
  for (const r of data.redirects as Record<string, unknown>[]) {
    const from = typeof r?.from === 'string' ? r.from.replace(/\/+$/, '') : ''
    const to = typeof r?.to === 'string' ? r.to : ''
    if (!plain(from)) throw new Error(`redirects.json: "from" ${JSON.stringify(r?.from)} must be a path of this site, like "/wiki-team"`)
    if (OWN.some(p => from === p || from.startsWith(`${p}/`))) throw new Error(`redirects.json: ${from} is the site's own`)
    if (!(to === '/' || plain(to.replace(/\/+$/, '')))) throw new Error(`redirects.json: "to" ${JSON.stringify(r?.to)} must be a path of this site, like "/wiki/team"`)
    if (out.some(x => x.from === from)) throw new Error(`redirects.json lists ${from} twice`)
    out.push({ from, to: to === '/' ? to : to.replace(/\/+$/, '') })
  }
  return out.sort((a, b) => b.from.length - a.from.length)
}

/** The middleware that sends old addresses on. */
export function redirectRoutes(list: Redirect[]): express.RequestHandler {
  return (req, res, next) => {
    if (req.method !== 'GET' && req.method !== 'HEAD') return next()
    const r = list.find(x => req.path === x.from || req.path.startsWith(`${x.from}/`))
    if (!r) return next()
    const rest = req.path.slice(r.from.length)
    const q = req.originalUrl.indexOf('?')
    const target = (r.to === '/' ? '' : r.to) + (rest || (r.to === '/' ? '/' : '')) + (q >= 0 ? req.originalUrl.slice(q) : '')
    res.redirect(302, target || '/')
  }
}
