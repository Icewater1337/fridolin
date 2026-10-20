# Security model

Fridolin permits allowed WhatsApp senders to initiate local agent runs. Treat
messages, model output and files written by brains as untrusted input. The
[README security section](../README.md#security) describes the operational
boundaries; [Privacy](../PRIVACY.md) inventories stored data and external services.

With the macOS sandbox enabled, each run receives an explicit filesystem policy
protecting bridge state, other brains and helper executables. Path approval,
per-run authorization, filtered environments and process supervision supplement
that policy. They do not make arbitrary agent instructions harmless or stop data
already available to an allowed tool from being sent to its service.

In the `fridolin-core` container each run is sandboxed with bubblewrap: a user,
mount and PID namespace of its own in which only the brain's folder, state and
media inbox are writable, the system is read-only, and nothing else of the data
volume (other brains, `bridge.db`, secrets, config, tokens) is mounted. The
container itself runs without capabilities, with a seccomp profile that allows
only the namespaces bubblewrap needs beyond Docker's defaults
([CORE.md](CORE.md#the-brains-sandbox-in-the-container)).

A bare Linux install has no equivalent per-brain sandbox. Brains run with the
bridge's user rights and can read bridge tokens, pairing state and other brain
data. A dedicated OS user limits exposure to the rest of the system; it does not
isolate brains from the bridge. Disabling the macOS or container sandbox has the
same broad consequence.

On the tailnet (`core.tailnet`) the dashboard always requires its token or a
session; the caller's tailnet identity is logged and audited but is not
authentication by itself.

Provider approval and OS sandboxing are separate controls. Without the provider's
skip-permissions option, tools requiring approval are denied in noninteractive
runs while permitted tools can still run. See the exact settings and defaults in
[Configuration](CONFIGURATION.md); an omitted setting is not a recommendation to
weaken it.

The dashboard checks Host/Origin and authentication; mutations also require its
custom header and JSON content type. Script and brain APIs have their own token
scopes. Keep tokens, SQLite pairing state, provider credentials and backup secrets
out of source control and diagnostics. Report vulnerabilities through the
[private reporting procedure](../SECURITY.md).
