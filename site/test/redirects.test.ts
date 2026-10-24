// modules/redirects.json: old addresses of this site sent to their new place.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import express from 'express'
import type { AddressInfo } from 'node:net'
import { loadRedirects, redirectRoutes } from '../server/redirects.ts'

function dirWith(t: import('node:test').TestContext, json: unknown) {
  const dir = mkdtempSync(join(tmpdir(), 'fridolin-redirects-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  writeFileSync(join(dir, 'redirects.json'), JSON.stringify(json))
  return dir
}

test('old addresses go to the new place, the longest match first, with the rest and the query', async (t) => {
  const list = loadRedirects(dirWith(t, { redirects: [
    { from: '/files-team', to: '/files/team' },
    { from: '/files-team/api/download', to: '/files/team' },
    { from: '/wiki-study/', to: '/wiki/team-study' },
  ] }))
  const app = express()
  app.use(redirectRoutes(list))
  app.get('/files-teamwork', (_req, res) => { res.send('a module') })
  const server = app.listen(0, '127.0.0.1')
  await new Promise(r => server.once('listening', r))
  t.after(() => { server.close() })
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  for (const [from, to] of [
    ['/files-team', '/files/team'],
    ['/files-team/', '/files/team/'],
    ['/files-team/api/download/documents/a%20b.pdf', '/files/team/documents/a%20b.pdf'],
    ['/wiki-study/people/Alex?x=1', '/wiki/team-study/people/Alex?x=1'],
  ]) {
    const r = await fetch(url + from, { redirect: 'manual' })
    assert.equal(r.status, 302, from)
    assert.equal(r.headers.get('location'), to, from)
  }
  const other = await fetch(`${url}/files-teamwork`, { redirect: 'manual' })
  assert.equal(await other.text(), 'a module', 'a path that only starts the same is not taken')
  const post = await fetch(`${url}/files-team`, { method: 'POST', redirect: 'manual' })
  assert.equal(post.status, 404, 'only reads are sent on')
})

test('a redirect stays on this site and off its own paths', (t) => {
  for (const bad of [
    { from: '/x', to: 'https://evil.example.com/' },
    { from: '/x', to: '//evil.example.com' },
    { from: 'x', to: '/y' },
    { from: '/auth/login', to: '/y' },
    { from: '/_fridolin/api', to: '/y' },
    { from: '/a/../b', to: '/y' },
  ]) {
    assert.throws(() => loadRedirects(dirWith(t, { redirects: [bad] })), Error, JSON.stringify(bad))
  }
  assert.throws(() => loadRedirects(dirWith(t, { redirects: [{ from: '/a', to: '/b' }, { from: '/a/', to: '/c' }] })), /twice/)
  assert.deepEqual(loadRedirects(mkdtempSync(join(tmpdir(), 'fridolin-none-'))), [])
})
