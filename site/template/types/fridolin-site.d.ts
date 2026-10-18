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
  log: (...args: unknown[]) => void
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
