// Entry point. Fridolin starts this with FRIDOLIN_SITE_RUNTIME pointing at the
// runtime.json it wrote; see config.ts.

import { readRuntime } from './config.ts'
import { createSite } from './app.ts'

const runtimePath = process.env.FRIDOLIN_SITE_RUNTIME
if (!runtimePath) {
  console.error('[site] FRIDOLIN_SITE_RUNTIME is not set — Fridolin starts this process, see docs/SITE.md')
  process.exit(2)
}

const rt = readRuntime(runtimePath)
const app = await createSite(rt)

const i = rt.listen.lastIndexOf(':')
const host = rt.listen.slice(0, i) || '127.0.0.1'
const port = Number(rt.listen.slice(i + 1))
const server = app.listen(port, host, () => {
  console.log(`[site] ${rt.preview_label ? `preview of ${rt.preview_label}` : rt.title} on http://${rt.listen}/`)
})

for (const sig of ['SIGTERM', 'SIGINT'] as const) {
  process.on(sig, () => {
    server.close(() => process.exit(0))
    setTimeout(() => process.exit(0), 3000).unref()
  })
}
