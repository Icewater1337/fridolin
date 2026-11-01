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
import { closeSync, copyFileSync, existsSync, lstatSync, mkdirSync, openSync, readFileSync, readSync, renameSync, unlinkSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { StringDecoder } from 'node:string_decoder'

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

    await awaitAnswer(dir, 'Fridolin did not take the files')
    return { status: 'accepted' }
  }
}

/** Thrown when the bridge answered no: its message says why. */
export class RefusedError extends Error {
  /** The bridge says a retry can work: a full queue, not a no. */
  temporary = false
}

/**
 * Waits for the bridge's result.json in spool folder dir and returns it;
 * throws a RefusedError with the bridge's reason when it says no, and an
 * Error when it does not answer in time.
 */
export async function awaitAnswer(dir: string, refused: string): Promise<Record<string, unknown>> {
  const result = join(dir, 'result.json')
  for (const deadline = Date.now() + ANSWER_MS; Date.now() < deadline;) {
    await new Promise(r => setTimeout(r, 200))
    if (!existsSync(result)) continue
    const r = JSON.parse(readFileSync(result, 'utf8')) as { ok?: boolean; error?: string }
    if (r.ok) return r
    const err = new RefusedError(r.error || refused)
    err.temporary = (r as { temporary?: boolean }).temporary === true
    throw err
  }
  throw new Error(`Fridolin did not answer within ${ANSWER_MS / 1000} seconds; is the bridge running?`)
}

export type SearchRequest = {
  /** A brain listed in the module's capabilities.search. */
  brain: string
  /** The whole prompt the agent gets: instructions and the question. */
  prompt: string
  /** Aborting it stops the search. */
  signal?: AbortSignal
}

/** How long a search may take, a little over the bridge's own limit. */
const SEARCH_MS = 200_000

/**
 * ctx.search for one module: the bridge runs a read-only Claude agent (Read,
 * Grep, Glob) in the brain's folder, under the brain's sandbox, and this
 * yields its stream-json events as they come. It throws when the bridge
 * refuses or the agent fails.
 */
export function makeSearch(moduleId: string, allowed: string[], spoolDir: string | undefined) {
  return async function* search(req: SearchRequest): AsyncGenerator<unknown> {
    if (!spoolDir) throw new Error('previews and checks cannot search a brain; only the live site can')
    if (!req || typeof req.brain !== 'string' || !allowed.includes(req.brain)) {
      throw new Error(`module "${moduleId}" may not search brain "${req?.brain}": list it in capabilities.search`)
    }
    if (typeof req.prompt !== 'string' || !req.prompt.trim()) throw new Error('ctx.search needs a prompt')
    const dir = join(spoolDir, randomBytes(12).toString('hex'))
    mkdirSync(dir, { recursive: true })
    writeFileSync(join(dir, 'search.json.part'), JSON.stringify({ module: moduleId, brain: req.brain, prompt: req.prompt }))
    renameSync(join(dir, 'search.json.part'), join(dir, 'search.json'))
    const cancel = () => { try { writeFileSync(join(dir, 'cancel'), '') } catch { /* gone already */ } }
    req.signal?.addEventListener('abort', cancel, { once: true })

    const stream = join(dir, 'stream.jsonl')
    const result = join(dir, 'result.json')
    let offset = 0
    let partial = ''
    // A character may be split between two reads.
    const decoder = new StringDecoder('utf8')
    const drain = function* () {
      if (!existsSync(stream)) return
      const fd = openSync(stream, 'r')
      try {
        const buf = Buffer.alloc(64 * 1024)
        for (let n; (n = readSync(fd, buf, 0, buf.length, offset)) > 0;) {
          offset += n
          partial += decoder.write(buf.subarray(0, n))
        }
      } finally {
        closeSync(fd)
      }
      for (let nl; (nl = partial.indexOf('\n')) >= 0;) {
        const line = partial.slice(0, nl).trim()
        partial = partial.slice(nl + 1)
        if (!line) continue
        try { yield JSON.parse(line) } catch { /* not an event */ }
      }
    }
    try {
      for (const deadline = Date.now() + SEARCH_MS; Date.now() < deadline;) {
        // Read what is there before looking for the answer, so nothing written
        // before result.json is missed.
        const done = existsSync(result)
        yield* drain()
        if (done) {
          const r = JSON.parse(readFileSync(result, 'utf8')) as { ok?: boolean; error?: string }
          if (!r.ok) throw new Error(r.error || 'the search failed')
          return
        }
        await new Promise(r => setTimeout(r, 150))
      }
      cancel()
      throw new Error('the search did not finish in time')
    } finally {
      req.signal?.removeEventListener('abort', cancel)
    }
  }
}
