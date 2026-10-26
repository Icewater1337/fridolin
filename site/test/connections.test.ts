// ctx.openai and ctx.elevenlabs: what module.json may declare, and the
// calls to the bridge's site listener, against a fake bridge.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, symlinkSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { randomBytes } from 'node:crypto'
import type { AddressInfo } from 'node:net'
import { validateManifest, type ModuleContext } from '../server/modules.ts'
import { ConnectionError, makeConnections, MAX_AUDIO_BYTES, type Connections } from '../server/connections.ts'
import { createSite } from '../server/app.ts'
import { readRuntime } from '../server/config.ts'
import { hashPassword } from '../server/auth.ts'
import type { ModuleContext as StarterContext } from '../template/types/fridolin-site.d.ts'

const SITE_DIR = join(dirname(fileURLToPath(import.meta.url)), '..')

// The starter's types for module code describe the ctx the site hands out:
// tsc (npm run typecheck) fails here when the two drift apart.
const asStarter = (ctx: ModuleContext): StarterContext => ctx
void asStarter

// ── module.json ───────────────────────────────────────────────────────────────

const base = {
  version: 1, id: 'demo', title: 'Demo', route: '/demo',
  access: { roles: ['admin'] },
  server: { entry: 'server.ts' },
}
const withCaps = (capabilities: Record<string, unknown>) => () => validateManifest({ ...base, capabilities }, 'demo')

test('validateManifest takes capabilities.openai and .elevenlabs as the contract has them', () => {
  const m = withCaps({
    openai: { use: ['transcribe', 'responses'], models: ['gpt-4o-transcribe', 'gpt-4o-mini-transcribe'], monthly_usd: 20 },
    elevenlabs: { use: ['tts', 'transcribe', 'voices'], models: ['eleven_multilingual_v2'], monthly_usd: 5 },
  })()
  assert.deepEqual(m.capabilities?.openai?.use, ['transcribe', 'responses'])
  assert.equal(m.capabilities?.elevenlabs?.monthly_usd, 5)
  // models and monthly_usd are optional; any model, no Fridolin limit.
  withCaps({ openai: { use: ['responses'] } })()
  withCaps({ elevenlabs: { use: ['voices'], monthly_usd: 10_000 } })()
  withCaps({ openai: { use: ['transcribe'], monthly_usd: 0.5, models: ['a', 'A.b_c:d-1', 'x'.repeat(64)] } })()
})

test('validateManifest refuses a connection it cannot read, with a message that says why', () => {
  const cases: [Record<string, unknown>, RegExp][] = [
    [{ openai: true }, /"capabilities.openai" must be \{ "use"/],
    [{ openai: ['transcribe'] }, /"capabilities.openai" must be/],
    [{ openai: {} }, /"capabilities.openai.use" must list at least one of: transcribe, responses/],
    [{ openai: { use: [] } }, /"capabilities.openai.use" must list at least one/],
    [{ openai: { use: 'transcribe' } }, /"capabilities.openai.use" must list/],
    [{ openai: { use: ['tts'] } }, /"capabilities.openai.use" entry "tts" is not one of: transcribe, responses/],
    [{ elevenlabs: { use: ['responses'] } }, /"capabilities.elevenlabs.use" entry "responses" is not one of: tts, transcribe, voices/],
    [{ elevenlabs: { use: ['tts', 'voices', 'tts'] } }, /"capabilities.elevenlabs.use" lists "tts" twice/],
    [{ openai: { use: ['responses'], models: 'gpt-5' } }, /"capabilities.openai.models" must be a list of model names/],
    [{ openai: { use: ['responses'], models: [] } }, /"capabilities.openai.models" must not be empty; leave it out/],
    [{ openai: { use: ['responses'], models: ['gpt 5'] } }, /"capabilities.openai.models" entry "gpt 5" must be 1 to 64/],
    [{ openai: { use: ['responses'], models: [''] } }, /"capabilities.openai.models" entry ""/],
    [{ openai: { use: ['responses'], models: ['x'.repeat(65)] } }, /must be 1 to 64/],
    [{ elevenlabs: { use: ['tts'], models: ['../voice'] } }, /"capabilities.elevenlabs.models" entry "..\/voice"/],
    [{ openai: { use: ['responses'], monthly_usd: 0 } }, /"capabilities.openai.monthly_usd" must be a number of US dollars above 0 and at most 10000/],
    [{ openai: { use: ['responses'], monthly_usd: -5 } }, /monthly_usd/],
    [{ openai: { use: ['responses'], monthly_usd: 10_001 } }, /monthly_usd/],
    [{ openai: { use: ['responses'], monthly_usd: '20' } }, /monthly_usd/],
    // A typo must not quietly mean "no limit".
    [{ openai: { use: ['responses'], monthly_limit: 20 } }, /"capabilities.openai" has an unknown field "monthly_limit"/],
  ]
  for (const [caps, re] of cases) assert.throws(withCaps(caps), re, JSON.stringify(caps))
})

// ── The calls, against a fake bridge ──────────────────────────────────────────

type Seen = { method: string; path: string; headers: IncomingMessage['headers']; body: Buffer }
type Answer = { status?: number; type?: string; body?: string | Buffer; hang?: boolean }

/** A bridge's site listener on the loopback: it records each request and answers what answer() says. */
async function fakeBridge(t: import('node:test').TestContext, answer: (s: Seen) => Answer) {
  const seen: Seen[] = []
  const server = createServer((req: IncomingMessage, res: ServerResponse) => {
    const chunks: Buffer[] = []
    req.on('data', (c: Buffer) => chunks.push(c))
    req.on('end', () => {
      const s = { method: req.method!, path: req.url!, headers: req.headers, body: Buffer.concat(chunks) }
      seen.push(s)
      const a = answer(s)
      if (a.hang) return
      res.writeHead(a.status ?? 200, { 'Content-Type': a.type ?? 'application/json' })
      res.end(a.body ?? '{}')
    })
  })
  server.listen(0, '127.0.0.1')
  await new Promise(r => server.once('listening', r))
  t.after(() => { server.closeAllConnections(); server.close() })
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  return { url, seen }
}

/** The multipart body a fake bridge got, parsed as the bridge would. */
async function formOf(s: Seen): Promise<FormData> {
  return await new Response(new Uint8Array(s.body), { headers: { 'content-type': String(s.headers['content-type']) } }).formData()
}

const ALL = {
  openai: { use: ['transcribe', 'responses'] },
  elevenlabs: { use: ['tts', 'transcribe', 'voices'] },
}

function connect(url: string, caps: Parameters<typeof makeConnections>[1] = ALL): Connections {
  return makeConnections('hands', caps, { url, token: 'bridge-token' })
}

/** Asserts that p rejects with a ConnectionError like want. */
async function rejectsWith(p: Promise<unknown>, want: { status: number; message?: RegExp; upstreamStatus?: number; code?: string }) {
  await assert.rejects(p, (err: unknown) => {
    assert.ok(err instanceof ConnectionError, String(err))
    assert.equal(err.name, 'ConnectionError')
    assert.equal(err.status, want.status, err.message)
    if (want.message) assert.match(err.message, want.message)
    assert.equal(err.upstreamStatus, want.upstreamStatus)
    assert.equal(err.code, want.code)
    return true
  })
}

test('ctx.openai.transcribe sends the audio as multipart with the token and the module', async (t) => {
  const bridge = await fakeBridge(t, () => ({ body: JSON.stringify({ text: 'Hallo Welt' }) }))
  const c = connect(bridge.url)
  const audio = Buffer.from('RIFF....WAVEfmt fake audio')
  assert.deepEqual(await c.openai.transcribe({ audio, contentType: 'audio/wav', language: 'de', prompt: 'Fridolin, Acme' }), { text: 'Hallo Welt' })

  const s = bridge.seen[0]
  assert.equal(s.method, 'POST')
  assert.equal(s.path, '/site-api/connections/openai/transcribe')
  assert.equal(s.headers.authorization, 'Bearer bridge-token')
  assert.equal(s.headers['x-fridolin-module'], 'hands')
  assert.match(String(s.headers['content-type']), /^multipart\/form-data; boundary=/)
  const form = await formOf(s)
  const file = form.get('file') as File
  assert.equal(file.name, 'audio.wav')
  assert.equal(file.type, 'audio/wav')
  assert.deepEqual(Buffer.from(await file.arrayBuffer()), audio)
  assert.equal(form.get('language'), 'de')
  assert.equal(form.get('prompt'), 'Fridolin, Acme')
  assert.equal(form.get('model'), null, 'no model: the bridge picks its default')

  // A File keeps its name, a given filename wins, an ArrayBuffer works too.
  await c.openai.transcribe({ audio: new File([audio], 'clip.m4a', { type: 'audio/mp4' }), model: 'gpt-4o-transcribe' })
  let f = await formOf(bridge.seen[1])
  assert.equal((f.get('file') as File).name, 'clip.m4a')
  assert.equal(f.get('model'), 'gpt-4o-transcribe')
  await c.openai.transcribe({ audio: new Uint8Array(audio).buffer, contentType: 'audio/webm;codecs=opus', filename: 'note.webm' })
  f = await formOf(bridge.seen[2])
  assert.equal((f.get('file') as File).name, 'note.webm')
  assert.deepEqual(Buffer.from(await (f.get('file') as File).arrayBuffer()), audio)
  await c.openai.transcribe({ audio: new Blob([audio], { type: 'audio/webm;codecs=opus' }) })
  assert.equal(((await formOf(bridge.seen[3])).get('file') as File).name, 'audio.webm')
})

test('ctx.openai.responses passes the body through as JSON and answers OpenAI\'s JSON', async (t) => {
  const answer = { id: 'resp_1', output_text: 'Hi', output: [{ type: 'message' }], usage: { input_tokens: 3, output_tokens: 1 } }
  const bridge = await fakeBridge(t, () => ({ body: JSON.stringify(answer) }))
  const body = { model: 'gpt-5-mini', input: [{ role: 'user', content: 'Hi' }], text: { format: { type: 'json_object' } } }
  assert.deepEqual(await connect(bridge.url).openai.responses(body), answer)
  const s = bridge.seen[0]
  assert.equal(s.path, '/site-api/connections/openai/responses')
  assert.equal(s.headers['content-type'], 'application/json')
  assert.equal(s.headers['x-fridolin-module'], 'hands')
  assert.deepEqual(JSON.parse(s.body.toString()), body)
})

test('ctx.elevenlabs.tts sends JSON and answers the audio bytes with their type', async (t) => {
  const mp3 = Buffer.from([0xff, 0xfb, 0x90, 0x00, 0x01, 0x02, 0xfe])
  const bridge = await fakeBridge(t, () => ({ type: 'audio/mpeg', body: mp3 }))
  const c = connect(bridge.url)
  const out = await c.elevenlabs.tts({ text: 'Guten Morgen', voice: 'voice-1', model: 'eleven_turbo_v2_5', format: 'mp3_22050_32', language: 'de', voiceSettings: { stability: 0.5 } })
  assert.ok(Buffer.isBuffer(out.audio))
  assert.deepEqual(out.audio, mp3)
  assert.equal(out.contentType, 'audio/mpeg')
  const s = bridge.seen[0]
  assert.equal(s.method, 'POST')
  assert.equal(s.path, '/site-api/connections/elevenlabs/tts')
  assert.equal(s.headers['content-type'], 'application/json')
  assert.equal(s.headers.authorization, 'Bearer bridge-token')
  assert.equal(s.headers['x-fridolin-module'], 'hands')
  assert.deepEqual(JSON.parse(s.body.toString()), {
    text: 'Guten Morgen', voice: 'voice-1', model: 'eleven_turbo_v2_5', format: 'mp3_22050_32', language: 'de', voice_settings: { stability: 0.5 },
  })
  // Only the text: the bridge's defaults for the rest.
  await c.elevenlabs.tts({ text: 'Hi' })
  assert.deepEqual(JSON.parse(bridge.seen[1].body.toString()), { text: 'Hi' })
})

test('ctx.elevenlabs.transcribe and .voices', async (t) => {
  const bridge = await fakeBridge(t, (s) => s.path.endsWith('/voices')
    ? { body: JSON.stringify({ voices: [{ id: 'v1', name: 'Rachel' }, { id: 'v2', name: 'Adam' }] }) }
    : { body: JSON.stringify({ text: 'Grüezi', language: 'de' }) })
  const c = connect(bridge.url)
  assert.deepEqual(await c.elevenlabs.transcribe({ audio: Buffer.from('ogg'), contentType: 'audio/ogg', model: 'scribe_v1', language: 'de' }), { text: 'Grüezi', language: 'de' })
  const s = bridge.seen[0]
  assert.equal(s.path, '/site-api/connections/elevenlabs/transcribe')
  assert.equal(s.headers['x-fridolin-module'], 'hands')
  const form = await formOf(s)
  assert.equal((form.get('file') as File).name, 'audio.ogg')
  assert.equal(form.get('model'), 'scribe_v1')
  assert.equal(form.get('language'), 'de')
  assert.equal(form.get('prompt'), null)

  assert.deepEqual(await c.elevenlabs.voices(), [{ id: 'v1', name: 'Rachel' }, { id: 'v2', name: 'Adam' }])
  const v = bridge.seen[1]
  assert.equal(v.method, 'GET')
  assert.equal(v.path, '/site-api/connections/elevenlabs/voices')
  assert.equal(v.body.length, 0)
  assert.equal(v.headers.authorization, 'Bearer bridge-token')
  assert.equal(v.headers['x-fridolin-module'], 'hands')
})

test('the bridge\'s errors become a ConnectionError with its status and the provider\'s', async (t) => {
  const answers: Record<string, Answer> = {
    '/site-api/connections/openai/responses': {
      status: 502, body: JSON.stringify({ error: 'OpenAI: The model `gpt-9` does not exist', upstream_status: 404, upstream_code: 'model_not_found' }),
    },
    '/site-api/connections/openai/transcribe': {
      status: 429, body: JSON.stringify({ error: 'module:hands is over its monthly budget of $20 for OpenAI' }),
    },
    '/site-api/connections/elevenlabs/tts': { status: 503, body: JSON.stringify({ error: 'no ElevenLabs key: set one in the dashboard under Integrations' }) },
    '/site-api/connections/elevenlabs/voices': { status: 500, type: 'text/plain', body: 'boom' },
    '/site-api/connections/elevenlabs/transcribe': { status: 403, body: JSON.stringify({ error: 'model "scribe_v2" is not in capabilities.elevenlabs.models' }) },
  }
  const bridge = await fakeBridge(t, (s) => answers[s.path])
  const c = connect(bridge.url)
  await rejectsWith(c.openai.responses({ model: 'gpt-9', input: 'x' }), { status: 502, message: /^OpenAI: The model `gpt-9` does not exist$/, upstreamStatus: 404, code: 'model_not_found' })
  await rejectsWith(c.openai.transcribe({ audio: Buffer.from('x'), contentType: 'audio/wav' }), { status: 429, message: /over its monthly budget of \$20/ })
  await rejectsWith(c.elevenlabs.tts({ text: 'Hi' }), { status: 503, message: /no ElevenLabs key/ })
  await rejectsWith(c.elevenlabs.voices(), { status: 500, message: /^ElevenLabs: Fridolin answered 500$/ })
  await rejectsWith(c.elevenlabs.transcribe({ audio: Buffer.from('x'), model: 'scribe_v2' }), { status: 403, message: /not in capabilities.elevenlabs.models/ })

  // A 200 whose body is not JSON is the bridge's fault, not the module's.
  answers['/site-api/connections/openai/responses'] = { type: 'text/html', body: '<html>' }
  await rejectsWith(c.openai.responses({ model: 'gpt-5', input: 'x' }), { status: 502, message: /^OpenAI: Fridolin's answer is not JSON$/ })
})

test('a call the manifest does not declare throws before anything reaches the bridge', async (t) => {
  const bridge = await fakeBridge(t, () => ({ body: '{}' }))
  const c = connect(bridge.url, { openai: { use: ['transcribe'] } })
  await rejectsWith(c.openai.responses({ model: 'gpt-5', input: 'x' }),
    { status: 403, message: /module "hands" uses openai.responses, which its module.json does not declare: add "responses" to capabilities.openai.use/ })
  for (const call of [() => c.elevenlabs.tts({ text: 'x' }), () => c.elevenlabs.voices(), () => c.elevenlabs.transcribe({ audio: Buffer.from('x') })]) {
    await rejectsWith(call(), { status: 403, message: /capabilities.elevenlabs.use/ })
  }
  const none = makeConnections('hands', undefined, { url: bridge.url, token: 'bridge-token' })
  await rejectsWith(none.openai.transcribe({ audio: Buffer.from('x') }), { status: 403, message: /add "transcribe" to capabilities.openai.use/ })
  assert.equal(bridge.seen.length, 0)
})

test('arguments the bridge would refuse are refused here, without a request', async (t) => {
  const bridge = await fakeBridge(t, () => ({ body: '{}' }))
  const c = connect(bridge.url)
  await rejectsWith(c.openai.transcribe({} as never), { status: 400, message: /needs the audio as a Uint8Array/ })
  await rejectsWith(c.openai.transcribe({ audio: 'not bytes' } as never), { status: 400, message: /needs the audio/ })
  await rejectsWith(c.openai.transcribe({ audio: new Uint8Array(0) }), { status: 400, message: /the audio is empty/ })
  await rejectsWith(c.elevenlabs.transcribe({ audio: Buffer.alloc(MAX_AUDIO_BYTES + 1) }), { status: 413, message: /larger than 25 MiB/ })
  await rejectsWith(c.openai.transcribe({ audio: Buffer.from('x'), language: 7 } as never), { status: 400, message: /"language" must be a string/ })
  await rejectsWith(c.openai.responses([] as never), { status: 400, message: /needs the request as an object/ })
  await rejectsWith(c.elevenlabs.tts({ text: '  ' }), { status: 400, message: /needs text/ })
  assert.equal(bridge.seen.length, 0)
})

test('without a bridge (previews and checks) every call says it is not available', async () => {
  const c = makeConnections('hands', ALL, undefined)
  await rejectsWith(c.openai.transcribe({ audio: Buffer.from('x') }), { status: 503, message: /^OpenAI is not available in previews and checks, only on the live site$/ })
  await rejectsWith(c.openai.responses({ model: 'gpt-5', input: 'x' }), { status: 503, message: /not available in previews/ })
  await rejectsWith(c.elevenlabs.tts({ text: 'x' }), { status: 503, message: /^ElevenLabs is not available in previews/ })
  await rejectsWith(c.elevenlabs.transcribe({ audio: Buffer.from('x') }), { status: 503, message: /not available in previews/ })
  await rejectsWith(c.elevenlabs.voices(), { status: 503, message: /not available in previews/ })
  // An undeclared call still names the declaration, so a preview shows the mistake.
  await rejectsWith(makeConnections('hands', {}, undefined).openai.responses({}), { status: 403, message: /capabilities.openai.use/ })
})

test('a bridge that does not answer, and a call the module aborts', async (t) => {
  // Nothing listens on a port the test just closed.
  const closed = createServer()
  closed.listen(0, '127.0.0.1')
  await new Promise(r => closed.once('listening', r))
  const port = (closed.address() as AddressInfo).port
  await new Promise(r => closed.close(r))
  await rejectsWith(connect(`http://127.0.0.1:${port}`).elevenlabs.voices(), { status: 502, message: /Fridolin did not answer; is the bridge running\?/ })

  const bridge = await fakeBridge(t, () => ({ hang: true }))
  const abort = new AbortController()
  const call = connect(bridge.url).openai.responses({ model: 'gpt-5', input: 'x' }, { signal: abort.signal })
  setTimeout(() => abort.abort(), 50)
  await assert.rejects(call, { name: 'AbortError' })
})

// ── In the site, as module code gets it ───────────────────────────────────────

test('module code gets ctx.openai and ctx.elevenlabs with the bridge from the secrets pipe, previews without it', async (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'fridolin-connections-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const mod = join(dir, 'modules', 'talker')
  mkdirSync(mod, { recursive: true })
  writeFileSync(join(dir, 'package.json'), '{ "type": "module" }')
  symlinkSync(join(SITE_DIR, 'node_modules'), join(dir, 'node_modules'))
  writeFileSync(join(mod, 'module.json'), JSON.stringify({
    version: 1, id: 'talker', title: 'Talker', route: '/talker', access: { roles: ['*'] }, server: { entry: 'server.ts' },
    capabilities: { openai: { use: ['responses'], models: ['gpt-5-mini'], monthly_usd: 3 }, elevenlabs: { use: ['voices'] } },
  }))
  // Each call's answer, or its error's name, status and message.
  writeFileSync(join(mod, 'server.ts'), `import express from 'express'
export default (ctx) => express.Router().get('/try', async (_req, res) => {
  const out = {}
  const calls = {
    responses: () => ctx.openai.responses({ model: 'gpt-5-mini', input: 'Hi' }),
    voices: () => ctx.elevenlabs.voices(),
    tts: () => ctx.elevenlabs.tts({ text: 'Hi' }),
  }
  for (const [name, call] of Object.entries(calls)) {
    try { out[name] = await call() } catch (e) { out[name] = { error: e.name, status: e.status, message: e.message } }
  }
  res.json(out)
})
`)
  const sam = { id: 'u_sam', kind: 'password', username: 'sam', name: 'Sam', role: 'member', password_hash: hashPassword('correct horse', 100_000) }
  writeFileSync(join(dir, 'users.json'), JSON.stringify({ version: 1, users: [sam] }))
  writeFileSync(join(dir, 'session.key'), randomBytes(32).toString('hex'))

  const calls: { url: string; headers: Headers; method: string; body?: string }[] = []
  const bridgeFetch = (async (input: string | URL | Request, init?: RequestInit) => {
    calls.push({ url: String(input), headers: new Headers(init?.headers), method: init?.method ?? 'GET', body: typeof init?.body === 'string' ? init.body : undefined })
    const body = String(input).endsWith('/voices') ? { voices: [{ id: 'v1', name: 'Rachel' }] } : { id: 'resp_1', output_text: 'Hello' }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } })
  }) as typeof fetch

  const start = async (bridge: boolean) => {
    const rt = join(dir, `runtime-${bridge}.json`)
    writeFileSync(rt, JSON.stringify({
      listen: '127.0.0.1:0', modules_dir: join(dir, 'modules'), data_dir: join(dir, 'data'),
      users_file: join(dir, 'users.json'), session_key_file: join(dir, 'session.key'),
      ...(bridge ? { bridge_url: 'http://127.0.0.1:1' } : {}),
    }))
    const app = await createSite(readRuntime(rt), {
      secrets: bridge ? { _fridolin: { bridge_token: 'bridge-token' } } : {},
      bridgeFetch,
    })
    const server = app.listen(0, '127.0.0.1')
    await new Promise(r => server.once('listening', r))
    t.after(() => { server.close() })
    const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
    const login = await fetch(`${url}/auth/login`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: 'sam', password: 'correct horse' }),
    })
    const cookie = login.headers.getSetCookie().find(c => c.startsWith('fridolin_site='))!.split(';')[0]
    return await (await fetch(`${url}/talker/api/try`, { headers: { cookie } })).json() as Record<string, { error?: string; status?: number; message?: string }>
  }

  const live = await start(true)
  assert.deepEqual(live.responses, { id: 'resp_1', output_text: 'Hello' })
  assert.deepEqual(live.voices, [{ id: 'v1', name: 'Rachel' }])
  assert.equal(live.tts.error, 'ConnectionError')
  assert.equal(live.tts.status, 403)
  assert.match(live.tts.message!, /add "tts" to capabilities.elevenlabs.use/)
  assert.deepEqual(calls.map(c => [c.method, c.url]), [
    ['POST', 'http://127.0.0.1:1/site-api/connections/openai/responses'],
    ['GET', 'http://127.0.0.1:1/site-api/connections/elevenlabs/voices'],
  ])
  for (const c of calls) {
    assert.equal(c.headers.get('authorization'), 'Bearer bridge-token')
    assert.equal(c.headers.get('x-fridolin-module'), 'talker')
  }
  assert.deepEqual(JSON.parse(calls[0].body!), { model: 'gpt-5-mini', input: 'Hi' })

  // No bridge, as in a preview: nothing goes out.
  calls.length = 0
  const preview = await start(false)
  assert.equal(preview.responses.status, 503)
  assert.match(preview.responses.message!, /not available in previews/)
  assert.equal(preview.voices.status, 503)
  assert.equal(calls.length, 0)
})
