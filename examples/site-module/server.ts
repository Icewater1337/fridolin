// The module's API, mounted at /notes/api behind sign-in and the module's
// access. It keeps its notes in ctx.dataDir, the module's own folder,
// which survives deploys and needs no capability.
import express from 'express'
import { readFileSync, renameSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { ModuleContext } from '../../types/fridolin-site'

type Note = { text: string; by: string; at: string }

export default function notes(ctx: ModuleContext): express.Router {
  const file = join(ctx.dataDir, 'notes.json')
  const load = (): Note[] => {
    try {
      return JSON.parse(readFileSync(file, 'utf8')) as Note[]
    } catch {
      return []
    }
  }
  const save = (list: Note[]) => {
    writeFileSync(`${file}.part`, JSON.stringify(list))
    renameSync(`${file}.part`, file)
  }

  const api = express.Router()
  api.get('/notes', (_req, res) => { res.json({ notes: load() }) })
  api.post('/notes', express.json({ limit: '8kb' }), (req, res) => {
    const text = typeof req.body?.text === 'string' ? req.body.text.trim().slice(0, 500) : ''
    if (!text) {
      res.status(400).json({ error: 'write a note first' })
      return
    }
    const user = (req as express.Request & { user: { name?: string; email: string } }).user
    const list = [{ text, by: user.name || user.email, at: new Date().toISOString() }, ...load()].slice(0, 200)
    save(list)
    ctx.log(`note added by ${user.email}`)
    res.json({ notes: list })
  })
  return api
}
