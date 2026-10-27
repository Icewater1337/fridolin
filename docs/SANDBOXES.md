# Docker Sandboxes for brains

A brain's run can happen in a Docker Sandbox: a microVM of Docker's
`sbx`, on a node of yours. The VM is the wall around the run, instead of
the namespaces of bubblewrap. Each brain gets its own sandbox, with an
egress allowlist of its own, and its run token never enters the VM.

Both runtimes stay:

- **bubblewrap** is the default, and today's sandbox: bubblewrap in the
  fridolin-core container, sandbox-exec on a native Mac. Nothing changes
  for a brain on it.
- **docker-sandbox** runs the brain in a sandbox on a node. When that
  sandbox is not available, the run falls back to bubblewrap by itself,
  and you hear of it.

## What you need

- The core and the node on the same machine, with the brain folders at
  the same paths: the live setup, with the core in its container on the
  Mac and the folders bound in from the Mac. There is no relay in this
  version. A brain whose folder the node cannot see falls back.
- The node with `sbx` installed, signed in, and set up by fridolin:
  `fridolin node sandbox setup` (docs/NODES.md, "Docker Sandboxes"). The
  node offers the `sandbox` capability, and you approve it like FrIDE:
  `!approve`, the Nodes page, or `fridolin nodes approve ID --commands`.
- For Claude: the node's Claude token in its host secret store, the same
  one the node's other runs use. The core never sends it.
- For Codex: sbx's ChatGPT login, `sbx secret set openai --oauth`, once on
  the Mac. It serves every sandbox there.

## Choosing the runtime

In config.json, the default for every brain:

```json
"sandbox": {
  "runtime": "docker-sandbox",
  "node": ""
}
```

- `sandbox.runtime`: `bubblewrap` (the default) or `docker-sandbox`.
- `sandbox.node`: the node, by id or name, whose sandboxes run the
  brains. Empty takes the one approved node that offers them; with
  several, name one.

In a brain's brain.json, to differ from it:

```json
"sandbox": {
  "runtime": "docker-sandbox",
  "fallback": "bubblewrap",
  "egress": ["api.github.com", "*.wikipedia.org"]
}
```

- `runtime` overrides config.json's.
- `fallback`: `bubblewrap` (the default) or `refuse`.
- `egress`: hosts the brain may reach beyond its provider and the core's
  API, in sbx's host patterns (`host`, `*.domain`, `**`, with an optional
  `:port`), at most 50. None by default. A brain that browses freely
  needs `"**:443"`.

These are brain.json fields like the rest: you set them on the brain's
page, and a change the brain makes itself waits for your approval. The
approval names what would loosen: a run under bubblewrap instead of a
Docker Sandbox, a fallback to bubblewrap instead of refusing, and each
new host.

## When the sandbox is not available

A run of a brain on docker-sandbox falls back to bubblewrap when the
node is offline, sbx is missing, not signed in or its daemon is down,
the node's settings or template are not fridolin's, the provider's login
is not there, or the brain's folder is not the node's (the marker, below).

- You get one WhatsApp message per outage, and one when the sandboxes
  are back. The brain's card shows it, and the run's audit line has
  `runtime`.
- A brain with `"fallback": "refuse"` does not run instead. Its chat is
  told why, and asked to send the message again later.
- A sandbox that starts but whose mount, secret or rule cannot be set up
  refuses the run. It does not fall back.
- A run never changes runtime halfway.

The core looks at the node at most every 30 seconds. The "back" message
comes with the next look: the next run of such a brain, or the dashboard.

## What you see

- **The Brains page**: each brain's `sandbox` in `GET /api/brains`:
  `configured`, `effective`, `fallback`, `node`, `ready`, `reason` and
  `egress`. `effective` is what the next run uses: `docker-sandbox`,
  `bubblewrap`, `sandbox-exec` (a native Mac's own sandbox) or `refused`
  (fallback `refuse` while the sandbox is not ready). The key is
  `sandbox`, since `runtime` there is the brain's run state.
- **The Nodes page**: the node's `sandbox` capability with what sbx
  reports.
- **`fridolin doctor`**: `brain-runtime` gives each brain on
  docker-sandbox, where it runs now and what its fallback does, and warns
  when one falls back. `fridolin doctor --node` on the node checks sbx.
- **The audit log**: `run_end` has `runtime` for runs of brains on
  docker-sandbox: `docker-sandbox`, `bubblewrap` when it fell back, or
  `refused`.

## How a run goes

1. The core writes its runtime id to `.fridolin-runtime-id` in the
   brain's folder. The id changes with every start of the core, and so
   with every move.
2. `sandbox.ensure` makes the brain's sandbox, `fridolin-<brain>`, or
   remakes it when its mounts changed. The node checks the marker: a
   folder that holds another id is a copy a core move left behind, and
   the run falls back.
3. `sandbox.run` starts the CLI in the VM with the arguments a run gets
   under bubblewrap, the prompt on stdin. The run's MCP config names the
   VM's fridolin (`bin_path` from `sandbox.info`); the node sets
   `FRIDOLIN_API`, `FRIDOLIN_BRAIN_TOKEN_FILE`, `FRIDOLIN_BIN`,
   `FRIDOLIN_TOOLS_DIR` and the login. The run token goes to the node
   alone, and sbx's proxy puts it into the run's calls to the core. A
   run with a GitHub grant gets its token the same way (below).
4. `sandbox.read` brings the output back as it comes, into the same
   parser, notices and deadlines as under bubblewrap.
5. A stop or a timeout calls `sandbox.stop`, which ends the run inside
   the VM. The sandbox stops by itself 30 seconds after the run.

## GitHub in a run

A run with a GitHub grant (docs/GITHUB.md) gets its token through sbx,
not as gh's login:

- The core sends the token to the node in `sandbox.run`'s
  `github_token`, never in the run's environment or arguments.
- The node stores it, through stdin, as the sandbox's own `github`
  secret. sbx's proxy sets it on every call to GitHub's hosts, whatever
  the client sends: gh's `Authorization: Bearer` and git's Basic auth
  alike. The VM has only sbx's placeholder in `GH_TOKEN`.
- So `gh` and git over HTTPS work as they are, with no credential
  helper.
- While the run goes on, the sandbox may reach `github.com`,
  `api.github.com`, `codeload.github.com`,
  `objects.githubusercontent.com` and `raw.githubusercontent.com`. The
  node adds these rules for the run, and removes them and the secret
  when it ends. A host the brain's own `egress` names keeps its rule.
- A run without a grant reaches none of them, unless the brain's egress
  names them. Its `GH_TOKEN` is the placeholder, which GitHub refuses.
- A brain's runs go one at a time, so one secret per sandbox is enough.
  Two runs that overlap share it until the last one ends.
- If the node's agent stops during a run, the brain's next ensure
  removes what that run opened: `state.json` names the rules.

Tested with sbx v0.50.0: `gh api user` and `git ls-remote` of a private
repository answered, and a scan of the VM as root found no token.

## The agent's sessions

Claude keeps each session in `~/.claude/projects` in the VM. sbx's kit
puts that folder on a volume of the sandbox's own, which goes when the
sandbox is made again: when its template, mounts, memory or egress
change.

So the node keeps the sessions on the host, in
`<state dir>/sandbox/history/<brain>/claude` (`codex` for Codex's
`~/.codex/sessions`). It binds that folder over the VM's after each
create, with `sbx mount`, and sbx restores the bind at each start.
Nothing is copied, and a rebuild loses nothing. Removing a brain's
sandbox keeps its sessions; delete the folder to drop them.

Brain runs do not resume a session: each starts from the chat's recent
history, as under bubblewrap, and Codex runs with `--ephemeral`. The
sessions are there for you to read.

A sandbox made before this version is made again once, at its next run.
The sessions in it until then go with it.

## Limits of this version

- No relay: the node must be on the core's machine.
- Message attachments are in the core's data folder, which the VM does
  not see.
- `sandbox.deny_paths` inside a brain folder cannot be hidden in the VM.
- A brain allowed to build the site keeps its builder's plugin only under
  bubblewrap.
- Claude's login is the node's setup-token in the run's environment, as
  under bubblewrap. TODO: sbx's own `/login`, which keeps the token out
  of the VM, once it is tested.

FrIDE, with its coding agents, can run in a Docker Sandbox of its own
too: docs/NODES.md, "FrIDE in a Docker Sandbox".
