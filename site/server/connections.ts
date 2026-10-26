// ctx.openai and ctx.elevenlabs: OpenAI and ElevenLabs through Fridolin
// (docs/SITE.md, "OpenAI and ElevenLabs"; docs/CONNECTIONS.md).
//
// Fridolin holds the keys. A module never sees one: it asks the bridge on
// its site listener, with the token the site got through the secrets pipe
// and its own id in X-Fridolin-Module. The bridge checks that the live
// commit's manifest of that module declares the call and the model, checks
// the module's monthly budget, calls the provider with the key and logs the
// usage. This side only refuses early what the manifest does not declare,
// so that a builder sees the mistake in a preview too.

import type { Bridge } from './builtin.ts'

/** The operations of each connection, as module.json names them in "use". */
export const CONNECTION_OPS = {
  openai: ['transcribe', 'responses'],
  elevenlabs: ['tts', 'transcribe', 'voices'],
} as const

export type ConnectionId = keyof typeof CONNECTION_OPS
export type OpenAIOp = typeof CONNECTION_OPS.openai[number]
export type ElevenLabsOp = typeof CONNECTION_OPS.elevenlabs[number]

/** What a module declares in capabilities.openai or capabilities.elevenlabs. */
export type ConnectionCapability<Op extends string> = {
  /** The calls it makes. */
  use: Op[]
  /** The models it may name; absent means any. */
  models?: string[]
  /** Its estimated monthly limit in US dollars; absent means none. */
  monthly_usd?: number
}

/** Audio for a transcription: bytes, or a Blob (a File keeps its name). */
export type AudioInput = Uint8Array | ArrayBuffer | Blob

export type OpenAITranscribeOptions = {
  audio: AudioInput
  /** The audio's type, e.g. "audio/webm". Default: the Blob's own type. */
  contentType?: string
  /** The file name OpenAI sees; its extension tells OpenAI the format. Default: from contentType. */
  filename?: string
  /** Default: gpt-4o-mini-transcribe. */
  model?: string
  /** The spoken language, e.g. "de". */
  language?: string
  /** Words and names to expect. */
  prompt?: string
  signal?: AbortSignal
}

export type ElevenLabsTranscribeOptions = {
  audio: AudioInput
  contentType?: string
  filename?: string
  /** Default: scribe_v1. */
  model?: string
  language?: string
  signal?: AbortSignal
}

export type ElevenLabsTTSOptions = {
  text: string
  /** A voice id. Default: the one set in Fridolin's dashboard. */
  voice?: string
  /** Default: eleven_multilingual_v2. */
  model?: string
  /** Default: mp3_44100_128. */
  format?: string
  language?: string
  voiceSettings?: Record<string, unknown>
  signal?: AbortSignal
}

export type OpenAIConnection = {
  /** Transcribes a clip (at most 25 MiB). */
  transcribe: (opts: OpenAITranscribeOptions) => Promise<{ text: string }>
  /** OpenAI's Responses API: the request body as OpenAI takes it ("model" required, no streaming); answers OpenAI's JSON. */
  responses: (body: Record<string, unknown>, opts?: { signal?: AbortSignal }) => Promise<any>
}

export type ElevenLabsConnection = {
  /** Text to speech: the audio and its type (audio/mpeg by default). */
  tts: (opts: ElevenLabsTTSOptions) => Promise<{ audio: Buffer; contentType: string }>
  /** Speech to text (at most 25 MiB). */
  transcribe: (opts: ElevenLabsTranscribeOptions) => Promise<{ text: string; language?: string }>
  /** The voices of the account. */
  voices: (opts?: { signal?: AbortSignal }) => Promise<Array<{ id: string; name: string }>>
}

/**
 * What a call throws: status is the bridge's HTTP status (400 a bad request,
 * 403 not declared, 429 over the monthly budget, 502 the provider failed,
 * 503 no key or no bridge, 504 the provider timed out), upstreamStatus and
 * code what the provider answered, when the bridge passed them on.
 */
export class ConnectionError extends Error {
  readonly status: number
  readonly upstreamStatus?: number
  readonly code?: string

  constructor(message: string, status: number, upstreamStatus?: number, code?: string) {
    super(message)
    this.name = 'ConnectionError'
    this.status = status
    if (upstreamStatus !== undefined) this.upstreamStatus = upstreamStatus
    if (code !== undefined) this.code = code
  }
}

/** The most audio a transcription takes, as the bridge and the providers do. */
export const MAX_AUDIO_BYTES = 25 << 20

const TITLES: Record<ConnectionId, string> = { openai: 'OpenAI', elevenlabs: 'ElevenLabs' }

/**
 * How long a call waits for the bridge: a little over the bridge's own
 * timeout for the provider, so that the bridge's 504 comes first.
 */
const WAIT_MS: Record<string, number> = {
  'openai transcribe': 150_000,
  'openai responses': 210_000,
  'elevenlabs tts': 150_000,
  'elevenlabs transcribe': 210_000,
  'elevenlabs voices': 60_000,
}

/** A file extension for an audio type, so that the provider knows the format. */
const EXTENSIONS: Record<string, string> = {
  'audio/mpeg': 'mp3', 'audio/mp3': 'mp3', 'audio/mpga': 'mpga',
  'audio/wav': 'wav', 'audio/x-wav': 'wav', 'audio/wave': 'wav',
  'audio/webm': 'webm', 'video/webm': 'webm',
  'audio/ogg': 'ogg', 'audio/opus': 'opus',
  'audio/flac': 'flac', 'audio/x-flac': 'flac',
  'audio/mp4': 'm4a', 'audio/m4a': 'm4a', 'audio/x-m4a': 'm4a', 'audio/aac': 'aac',
  'video/mp4': 'mp4',
}

export type Connections = { openai: OpenAIConnection; elevenlabs: ElevenLabsConnection }

/**
 * ctx.openai and ctx.elevenlabs for one module. bridge is unset in previews
 * and checks; fetch is swappable for tests.
 */
export function makeConnections(
  moduleId: string,
  caps: { openai?: ConnectionCapability<string>; elevenlabs?: ConnectionCapability<string> } | undefined,
  bridge: Bridge | undefined,
  doFetch: typeof fetch = fetch,
): Connections {
  // Checked here first, so that a preview names a missing declaration too.
  const ready = (conn: ConnectionId, op: string) => {
    if (!caps?.[conn]?.use?.includes(op)) {
      throw new ConnectionError(
        `module "${moduleId}" uses ${conn}.${op}, which its module.json does not declare: add "${op}" to capabilities.${conn}.use`, 403)
    }
    if (!bridge) {
      throw new ConnectionError(`${TITLES[conn]} is not available in previews and checks, only on the live site`, 503)
    }
    return bridge
  }

  /** One request to the bridge; read takes the answer's body while the call's time runs. */
  const call = async <T>(
    conn: ConnectionId, op: string,
    init: { method: 'GET' | 'POST'; body?: FormData | string; signal?: AbortSignal },
    read: (res: Response) => Promise<T>,
  ): Promise<T> => {
    const b = ready(conn, op)
    const url = new URL(b.url)
    url.pathname = `/site-api/connections/${conn}/${op}`
    const headers: Record<string, string> = {
      Authorization: `Bearer ${b.token}`,
      'X-Fridolin-Module': moduleId,
    }
    // FormData sets its own type, with the boundary.
    if (typeof init.body === 'string') headers['Content-Type'] = 'application/json'
    const wait = WAIT_MS[`${conn} ${op}`]
    const timeout = AbortSignal.timeout(wait)
    const signal = init.signal ? AbortSignal.any([init.signal, timeout]) : timeout
    // The module's own abort comes back as it is.
    const stopped = () => {
      if (init.signal?.aborted) throw init.signal.reason
      if (timeout.aborted) throw new ConnectionError(`${TITLES[conn]}: Fridolin did not answer within ${wait / 1000} seconds`, 504)
    }
    let res: Response
    try {
      res = await doFetch(url.href, { method: init.method, headers, body: init.body, signal, redirect: 'error' })
    } catch (err) {
      stopped()
      throw new ConnectionError(`${TITLES[conn]}: Fridolin did not answer; is the bridge running? (${err instanceof Error ? err.message : err})`, 502)
    }
    if (!res.ok) throw await errorOf(res, conn)
    try {
      return await read(res)
    } catch (err) {
      stopped()
      const what = err instanceof SyntaxError ? 'is not JSON' : 'broke off'
      throw new ConnectionError(`${TITLES[conn]}: Fridolin's answer ${what}`, 502)
    }
  }
  const json = (res: Response): Promise<unknown> => res.json()

  // The form fields each provider's transcription takes besides the file.
  const fields: Record<ConnectionId, string[]> = { openai: ['model', 'language', 'prompt'], elevenlabs: ['model', 'language'] }
  const transcribe = (conn: ConnectionId) => async (opts: OpenAITranscribeOptions | ElevenLabsTranscribeOptions) => {
    ready(conn, 'transcribe')
    const form = new FormData()
    const { blob, name } = audioFile(opts, `ctx.${conn}.transcribe`)
    form.append('file', blob, name)
    for (const k of fields[conn]) {
      const v = (opts as Record<string, unknown>)[k]
      if (v === undefined) continue
      if (typeof v !== 'string') throw new ConnectionError(`ctx.${conn}.transcribe: "${k}" must be a string`, 400)
      form.append(k, v)
    }
    return call(conn, 'transcribe', { method: 'POST', body: form, signal: opts.signal }, json)
  }

  return {
    openai: {
      transcribe: async (opts) => {
        const r = await transcribe('openai')(opts) as { text?: unknown }
        return { text: typeof r?.text === 'string' ? r.text : '' }
      },
      responses: async (body, opts = {}) => {
        ready('openai', 'responses')
        if (typeof body !== 'object' || body === null || Array.isArray(body)) {
          throw new ConnectionError('ctx.openai.responses needs the request as an object, e.g. { model, input }', 400)
        }
        return call('openai', 'responses', { method: 'POST', body: JSON.stringify(body), signal: opts.signal }, json)
      },
    },
    elevenlabs: {
      tts: async (opts) => {
        ready('elevenlabs', 'tts')
        if (typeof opts?.text !== 'string' || !opts.text.trim()) throw new ConnectionError('ctx.elevenlabs.tts needs text', 400)
        const body = {
          text: opts.text,
          voice: opts.voice,
          model: opts.model,
          format: opts.format,
          language: opts.language,
          voice_settings: opts.voiceSettings,
        }
        return call('elevenlabs', 'tts', { method: 'POST', body: JSON.stringify(body), signal: opts.signal }, async (res) => ({
          audio: Buffer.from(await res.arrayBuffer()),
          contentType: res.headers.get('content-type') || 'application/octet-stream',
        }))
      },
      transcribe: async (opts) => {
        const r = await transcribe('elevenlabs')(opts) as { text?: unknown; language?: unknown }
        const out: { text: string; language?: string } = { text: typeof r?.text === 'string' ? r.text : '' }
        if (typeof r?.language === 'string' && r.language) out.language = r.language
        return out
      },
      voices: async (opts = {}) => {
        const r = await call('elevenlabs', 'voices', { method: 'GET', signal: opts.signal }, json) as { voices?: unknown }
        if (!Array.isArray(r?.voices)) return []
        return r.voices
          .filter((v): v is { id: string; name?: unknown } => typeof v?.id === 'string')
          .map(v => ({ id: v.id, name: typeof v.name === 'string' ? v.name : '' }))
      },
    },
  }
}

/** The bridge's error JSON as a ConnectionError: {"error", "upstream_status"?, "upstream_code"?}. */
async function errorOf(res: Response, conn: ConnectionId): Promise<ConnectionError> {
  let raw: unknown
  try {
    raw = JSON.parse(await res.text())
  } catch { /* not JSON, or cut off */ }
  const e = (typeof raw === 'object' && raw !== null ? raw : {}) as Record<string, unknown>
  const message = typeof e.error === 'string' && e.error ? e.error : `${TITLES[conn]}: Fridolin answered ${res.status}`
  return new ConnectionError(
    message,
    res.status,
    typeof e.upstream_status === 'number' ? e.upstream_status : undefined,
    typeof e.upstream_code === 'string' && e.upstream_code ? e.upstream_code : undefined,
  )
}

/** The audio as a Blob of its type, and the file name the provider sees. */
function audioFile(opts: { audio: AudioInput; contentType?: string; filename?: string }, where: string): { blob: Blob; name: string } {
  const a = opts?.audio
  let blob: Blob
  if (a instanceof Blob) {
    blob = opts.contentType && opts.contentType !== a.type ? new Blob([a], { type: opts.contentType }) : a
  } else if (a instanceof Uint8Array || a instanceof ArrayBuffer) {
    blob = new Blob([a instanceof ArrayBuffer ? new Uint8Array(a) : a], { type: opts.contentType ?? '' })
  } else {
    throw new ConnectionError(`${where} needs the audio as a Uint8Array, a Buffer, an ArrayBuffer or a Blob`, 400)
  }
  if (blob.size === 0) throw new ConnectionError(`${where}: the audio is empty`, 400)
  if (blob.size > MAX_AUDIO_BYTES) throw new ConnectionError(`${where}: the audio is larger than 25 MiB`, 413)
  if (opts.filename !== undefined && typeof opts.filename !== 'string') throw new ConnectionError(`${where}: "filename" must be a string`, 400)
  const own = typeof File !== 'undefined' && a instanceof File ? a.name : ''
  const type = blob.type.split(';')[0].trim().toLowerCase()
  const name = opts.filename || own || `audio.${EXTENSIONS[type] ?? 'bin'}`
  return { blob, name }
}
