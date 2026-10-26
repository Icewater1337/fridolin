# My site

The modules of a website that Fridolin
serves. Fridolin owns sign-in, users and roles; this repository holds only the
parts of the site, one folder per module in `modules/`.

- `modules/<id>/module.json` says where a module lives, who may open it and
  what it touches. The format is in Fridolin's `docs/SITE.md`.
- Nothing reaches the live site until it is approved in Fridolin's dashboard
  (Site → Deploys). Fridolin then runs `npm install`, builds every module that
  has an `app.build` command and restarts the site.
- `CLAUDE.md` holds this site's conventions for whoever builds modules here.

Never commit secrets: modules declare the secrets they need by name in
`capabilities.secrets`, the values are set in Fridolin's dashboard (Site →
Secrets), and server code reads them with `ctx.secret(name)`. OpenAI and
ElevenLabs need no secret: declare `capabilities.openai` or
`capabilities.elevenlabs` and call `ctx.openai` or `ctx.elevenlabs`, which
use Fridolin's keys. On macOS the
live site runs in a sandbox that holds module code to what `capabilities`
declares.
