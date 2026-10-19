# Tools used by brains

Brain processes receive the bridge's supported CLI and tools location through
their projected environment. Prefer the supported `fridolin history`,
`fridolin polls` and `fridolin calendar` commands to direct database access.
Their API requests use run-scoped authorization; [API](API.md) documents the
brain and script endpoints, and [Calendar](CALENDAR.md) describes calendar scope.

The Python helpers in `tools/` include history search, poll result lookup,
calendar operations and audio transcription. Run each helper with `--help` for
its argument grammar. `toolkit.py` holds shared client behavior. Transcription
can use local models or a configured hosted provider; see
[Transcription](../README.md#transcription) and [Privacy](../PRIVACY.md) before
choosing where audio is processed.

Keep helper paths inside the installed tools layout. Provider processes receive
an allowlisted environment rather than the bridge's complete environment.
`child_env_passthrough` is an explicit extension; never use it to pass dashboard
or unrelated service secrets to a brain.

For helper development, run `make test-py` and `make lint-py`. Install the pinned
linter from `tools/requirements-dev.txt` in a development virtual environment.
The helpers' tests use synthetic data and support Python 3.9.
