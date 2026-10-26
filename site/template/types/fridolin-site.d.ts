// Types for module server code: `import type { ModuleContext } from '../../types/fridolin-site'`.

import type { Request, Router } from 'express'

export type ModuleContext = {
  /** The module's id (its folder name). */
  id: string
  /** The module's folder. */
  dir: string
  /** A path declared in module.json's capabilities.files. Throws for any other name. */
  file: (name: string) => string
  /**
   * A secret declared in module.json's capabilities.secrets, with the value
   * set in Fridolin's dashboard (Site → Secrets). Throws for any other name,
   * for one without a value yet, and in previews and checks, which get none.
   */
  secret: (name: string) => string
  /** A folder of the module's own that survives deploys: logs, state, uploads on their way to a brain. */
  dataDir: string
  /**
   * Hands files to a brain listed in capabilities.ingest, as a WhatsApp
   * attachment would reach it. The files are moved. Only the live site can.
   */
  ingest: (req: { brain: string; files: string[]; text?: string; sender?: string }) => Promise<{ status: 'accepted' }>
  /**
   * Runs a read-only Claude agent in the folder of a brain listed in
   * capabilities.search, under that brain's sandbox; yields its stream-json events.
   */
  search: (req: { brain: string; prompt: string; signal?: AbortSignal }) => AsyncGenerator<unknown>
  /**
   * OpenAI through Fridolin, which holds the key: the calls listed in
   * capabilities.openai.use, with the models in .models. Throws a
   * ConnectionError for a call the manifest does not list, when Fridolin or
   * OpenAI says no, and in previews and checks.
   */
  openai: OpenAIConnection
  /** ElevenLabs through Fridolin, as ctx.openai: the calls in capabilities.elevenlabs.use. */
  elevenlabs: ElevenLabsConnection
  log: (...args: unknown[]) => void
}

/** Audio to transcribe: bytes (a Buffer too), or a Blob (a File keeps its name). */
export type AudioInput = Uint8Array | ArrayBuffer | Blob

export type OpenAIConnection = {
  /** Transcribes a clip of at most 25 MiB. Default model: gpt-4o-mini-transcribe. */
  transcribe: (opts: {
    audio: AudioInput
    /** e.g. "audio/webm"; the file name's extension tells OpenAI the format. */
    contentType?: string
    filename?: string
    model?: string
    /** The spoken language, e.g. "de". */
    language?: string
    /** Words and names to expect. */
    prompt?: string
    signal?: AbortSignal
  }) => Promise<{ text: string }>
  /**
   * OpenAI's Responses API: the request body as OpenAI takes it ("model" is
   * required; "stream" and "background" are refused). Answers OpenAI's JSON
   * as it is.
   */
  responses: (body: Record<string, unknown>, opts?: { signal?: AbortSignal }) => Promise<any>
}

export type ElevenLabsConnection = {
  /**
   * Text to speech, at most 10,000 characters. voice: a voice id, default
   * the one set in Fridolin's dashboard. Defaults: model
   * eleven_multilingual_v2, format mp3_44100_128.
   */
  tts: (opts: {
    text: string
    voice?: string
    model?: string
    format?: string
    language?: string
    voiceSettings?: Record<string, unknown>
    signal?: AbortSignal
  }) => Promise<{ audio: Buffer; contentType: string }>
  /**
   * Speech to text, at most 25 MiB. Default model: scribe_v1. language is
   * ElevenLabs's code as it is, which may have three letters, e.g. "deu".
   */
  transcribe: (opts: {
    audio: AudioInput
    contentType?: string
    filename?: string
    model?: string
    language?: string
    signal?: AbortSignal
  }) => Promise<{ text: string; language?: string }>
  /** The voices of Fridolin's ElevenLabs account. */
  voices: (opts?: { signal?: AbortSignal }) => Promise<Array<{ id: string; name: string }>>
}

/**
 * What ctx.openai and ctx.elevenlabs throw. Tell it by its name:
 * `err instanceof Error && err.name === 'ConnectionError'`.
 */
export interface ConnectionError extends Error {
  name: 'ConnectionError'
  /**
   * Fridolin's HTTP status: 400 a bad request, 403 not declared in
   * module.json, 404 the module is not live, 429 over the module's monthly
   * budget, 502 the provider failed, 503 no key set or a preview, 504 the
   * provider timed out.
   */
  status: number
  /** The provider's own HTTP status, when it answered an error. */
  upstreamStatus?: number
  /** The provider's error code, e.g. "model_not_found". */
  code?: string
}

/** The default export of a module's server entry. */
export type ModuleServer = (ctx: ModuleContext) => Router | Promise<Router>

/** What `req.user` holds behind sign-in. */
export type SessionUser = {
  id: string
  email: string
  name: string
  role: string
  authType: 'google' | 'password'
  picture?: string
}

export type SignedInRequest = Request & { user: SessionUser }
