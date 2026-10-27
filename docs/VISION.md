# Vision

What each part of Fridolin is for, and which technology serves that best.
The owner set these roles on 2026-10-27. Check a change against this page:
a change that serves none of the roles below, or works against one, needs a
reason.

## The roles

### The core and its brain

The core is the bridge and its main brain (the brain with `role: core`). It
runs the whole system.

- It orchestrates everything. It has the most power of all brains.
- It controls a machine. It does that through the node on that machine,
  never by reaching out of its own container (see below).
- It holds the secrets for everyone else, and hands each part only what it
  needs.
- It owns the settings.
- It hands large tasks, and above all coding tasks, to FrIDE.
- It delegates work to nodes when its own machine runs short.
- It reaches the internet and the links it is given freely, when a task
  needs it.
- It takes orders only from the channels its brain.json names. Steps that
  cannot be undone keep going through the owner's approval.

### Other brains

A brain that is not the core is an assistant behind WhatsApp.

- It builds its own knowledge base from what reaches it in its chats.
- It does small tasks: do this, do that, process this document, tell me
  that.
- It reaches the internet and the links it is given freely, when a task
  needs it.
- It has no FrIDE. Coding work goes through the core.

### Nodes

A node exists only to help the core.

- It takes over work from the core.
- It receives the secrets the core gives it for that work.
- It keeps backups for the core.
- It can become the core.

### FrIDE

FrIDE is the coding assistant, and the assistant for heavy tasks. The core
brain and the nodes use it; other brains do not.

- The core starts a FrIDE workspace for a job. For example: "build this in
  my work repositories", or the fixer handing over a bug in fridolin.
- The workspace has the access the job needs, and no more: its
  repositories, its git identity and token (GitHub, a self-hosted GitLab),
  its network.
- It can push, and open pull and merge requests.
- It can bring a project's stack up and test it end to end.

## The technology for each role

| Role | Runs as | Why |
| --- | --- | --- |
| Core | A container ("box"), the image plus the `fridolin-data` volume | It moves between machines, backs up and comes back from a backup. |
| Brains (the core's too) | bubblewrap inside the core's container | Cheap, fast and online. The container already walls them off from the host; on a Mac it sits inside Docker Desktop's Linux VM. |
| Node | One native agent per machine | Only a native process can start VMs for FrIDE (a container cannot), and the same agent keeps backups and runs the core in Docker when the core moves there. A box is the fallback where Docker Sandboxes cannot run. |
| FrIDE | Instances in Docker Sandboxes (`sbx`), one per trust domain | Each is a microVM with its own Docker, so stacks start inside it. It cannot reach the host's files, its tokens stay outside as placeholders, and its network is an allowlist. |

### Why not Docker Sandboxes for brains

A brain already runs in a wall, and a VM per brain costs about 1 GB of
memory, ties the brain to one machine and adds a young tool to every run.
The benefits a VM would bring brains (a network allowlist, tokens kept out
of the run) can come more cheaply inside the core's container if they are
ever needed. The Docker Sandbox runtime for brains, built on 2026-10-26
and 27, is removed again.

### How the core starts a FrIDE

Nothing inside a container can start a VM: neither the core's container nor
a box. So the core asks the native node agent of a machine that can, and
that agent runs `sbx`:

```
core (container) ──"start FrIDE 'work', 6 GB"──▶ node agent (native)
                                                  └─▶ sbx ─▶ microVM: FrIDE + its Docker
```

### FrIDE instances

- One instance per trust domain, for example `fridolin` (GitHub) and `work`
  (the work GitLab and its registries). Workspaces inside one instance share
  its VM, so the instance, not the workspace, is the wall between them.
- An instance is named in the config with its repositories, its git
  identities, its network allowlist and where it may run.
- The core starts one when a task needs it, and stops it when it has been
  idle for a while.
- The core decides an instance's memory when it starts it, from the task
  (code only, or the project's stack too) and from what the nodes report as
  free. `sbx` cannot resize a running VM: a task that needs more gets the
  instance made again, bigger, with its workspaces kept.

### Secrets

The core holds every secret. It hands out scoped tokens: a FrIDE instance
gets the identities its config names, and inside a Docker Sandbox only
placeholders that the sandbox's proxy swaps for the real token. The work
GitLab token never goes to the cloud: it stays out of snapshots, the vault
and core exports, and a core move carries it sealed from machine to
machine.

### Work code and AI

FrIDE may work on the owner's work repositories. The owner checked that the
AI providers may process that code, set up so that they keep none of it.

## The plan (2026-10-27)

1. Write this page.
2. Remove the Docker Sandbox runtime for brains.
3. FrIDE instances: config, the core's tools to start and stop them, memory
   decided at start, idle stop, the GitLab connection and git identities.
   They replace the second node a sandboxed FrIDE needed, and later the
   native companion FrIDE.
4. The core controls the Mac through the Mac's node: workstation mode and
   a wider exec list, with approvals.
5. Nodes become one native agent per machine, icebase included.
