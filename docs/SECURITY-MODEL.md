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

Linux has no equivalent per-brain sandbox here. Brains run with the bridge's user
rights and can read bridge tokens, pairing state and other brain data. A dedicated
OS user limits exposure to the rest of the system; it does not isolate brains
from the bridge. Disabling the macOS sandbox has the same broad consequence.

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
