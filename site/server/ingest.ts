// ctx.ingest: hand files to a brain through Fridolin (docs/SITE.md, "Handing
// files to a brain").
//
// The live site cannot reach Fridolin's data folder or the bridge's HTTP
// port. A module whose module.json lists a brain in capabilities.ingest puts
// the files and a request.json into a folder of its own under the spool
// folder; the bridge picks it up, checks that the approved module declares
// that brain, moves the files into its media folder and hands them to the
// brain like an upload. It answers with result.json next to the request.

import { randomBytes } from 'node:crypto'
import { copyFileSync, existsSync, lstatSync, mkdirSync, readFileSync, renameSync, unlinkSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'

export type IngestRequest = {
  /** A brain listed in the module's capabilities.ingest. */
  brain: string
  /** Files the module wrote (its dataDir, the site's TMPDIR): they are moved, not copied. */
  files: string[]
  /** What the brain is told about them, as with a WhatsApp attachment. */
  text?: string
  /** Who sent them, for the brain's history. */
  sender?: string
}

export type IngestResult = { status: 'accepted' }

/** How long a module waits for Fridolin to take the files. */
const ANSWER_MS = 20_000

/** The name a file keeps: no folders, no control characters, no leading dots. */
export function safeName(name: string): string {
  const base = basename(name.replaceAll('\\', '/'))
  // Control characters out, leading dots off: the name keeps its letters, ü included.
  const cleaned = [...base].filter(c => c.charCodeAt(0) >= 0x20 && c.charCodeAt(0) !== 0x7f).join('').replace(/^\.+/, '').trim()
  return cleaned.slice(0, 200) || 'upload'
}

/** ctx.ingest for one module. spoolDir is unset in previews and checks. */
export function makeIngest(moduleId: string, allowed: string[], spoolDir: string | undefined) {
  return async (req: IngestRequest): Promise<IngestResult> => {
    if (!spoolDir) throw new Error('previews and checks cannot hand files to brains; only the live site can')
    if (!req || typeof req.brain !== 'string' || !allowed.includes(req.brain)) {
      throw new Error(`module "${moduleId}" may not hand files to brain "${req?.brain}": list it in capabilities.ingest`)
    }
    const files = Array.isArray(req.files) ? req.files : []
    if (files.length === 0 && !req.text?.trim()) throw new Error('ctx.ingest needs files or text')

    const dir = join(spoolDir, randomBytes(12).toString('hex'))
    const filesDir = join(dir, 'files')
    mkdirSync(filesDir, { recursive: true })
    const names: string[] = []
    for (const f of files) {
      if (typeof f !== 'string' || !lstatSync(f).isFile()) throw new Error(`${f} is not a regular file`)
      let name = safeName(f)
      for (let i = 1; names.includes(name); i++) name = `${i}_${safeName(f)}`
      const dest = join(filesDir, name)
      try {
        renameSync(f, dest)
      } catch {
        copyFileSync(f, dest) // another file system
        unlinkSync(f)
      }
      names.push(name)
    }
    const request = { module: moduleId, brain: req.brain, files: names, text: req.text ?? '', sender: req.sender ?? '' }
    // The bridge reads request.json only once it is whole.
    writeFileSync(join(dir, 'request.json.part'), JSON.stringify(request))
    renameSync(join(dir, 'request.json.part'), join(dir, 'request.json'))

    const result = join(dir, 'result.json')
    for (const deadline = Date.now() + ANSWER_MS; Date.now() < deadline;) {
      await new Promise(r => setTimeout(r, 200))
      if (!existsSync(result)) continue
      const r = JSON.parse(readFileSync(result, 'utf8')) as { ok?: boolean; error?: string }
      if (r.ok) return { status: 'accepted' }
      throw new Error(r.error || 'Fridolin did not take the files')
    }
    throw new Error(`Fridolin did not take the files within ${ANSWER_MS / 1000} seconds; is the bridge running?`)
  }
}
