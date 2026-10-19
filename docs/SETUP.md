# Setup

Use the [README setup instructions](../README.md#setup) for installation,
requirements, pairing and service configuration. Installation builds and tests
the program, writes a starter config and installs/restarts a service. Review the
[upgrade and rollback notes](../README.md#upgrading-and-rolling-back) before
replacing an existing installation.

A second WhatsApp number is recommended. Pair through the dashboard using the
phone's Linked Devices screen. Allow only the contacts and groups that should
reach a brain. Provider authentication and Google/Plaud authorization remain
interactive owner actions; do not put their credentials into an issue or chat.

## Development without WhatsApp

Build with `make build`. Create a scratch config outside your real data folder,
using an unused loopback port and a distinct data directory. For example:

```json
{
  "version": 2,
  "data_dir": "/tmp/fridolin-dev/data",
  "http_listen": "127.0.0.1:18765",
  "allowed_numbers": [],
  "brain_dirs": []
}
```

Save it as `/tmp/fridolin-dev/config.json`, then run:

```sh
bin/fridolin --no-whatsapp --config /tmp/fridolin-dev/config.json
```

The Dev page simulates inbound messages through the same access and routing
policy. Add a scratch brain and allow a test sender in the dashboard before
expecting replies. Dev mode can still invoke a real provider for that brain;
`make smoke` uses a fake provider when you only need automated verification.

A scratch binary does not update the installed service. Do not run `setup.sh`
or `service install` merely to test a code change. See [Contributing](../CONTRIBUTING.md).
