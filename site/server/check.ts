// `node --import tsx/esm server/check.ts <modules dir>`: load and mount the
// modules the way the site does and print a JSON report as the last line:
// {"mounted": [...], "broken": [{"id", "error"}]}. Fridolin runs it for a
// builder brain, inside that brain's sandbox (site_check).

import express from 'express'
import { loadModules, mountModules } from './modules.ts'

const dir = process.argv[2]
if (!dir) {
  console.error('usage: check.ts <modules dir>')
  process.exit(2)
}
const pass: express.RequestHandler = (_req, _res, next) => next()
const report = await mountModules(express(), loadModules(dir), pass)
console.log(JSON.stringify(report))
// Module code may have left timers or servers running.
process.exit(0)
