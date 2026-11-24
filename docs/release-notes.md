# v0.4.0

- MCP keyboard and mouse tools support optional `wait_for` conditions and
  `wait_timeout_ms`, returning the screenshot checked by an OpenAI-compatible
  vision service.
- Reduced MCP screenshot capture and transfer overhead.
- `autopaws-kvm` and `autopaws-mcp` can read the same YAML file with `-config`,
  sharing the API address and either a `token-file` or inline `token`.
- Enabled vision checks default to `reasoning_effort: "none"`. An explicit
  `vision-request-json` object replaces that default; `{}` lets the provider
  choose.

Verify the returned screenshot before continuing. A failed or timed-out wait
preserves successful input; do not replay it automatically.

See [configuration](https://github.com/linkdata/autopaws/blob/v0.4.0/README.md#configuration-file)
and [visual waits](https://github.com/linkdata/autopaws/blob/v0.4.0/README.md#input-with-a-visual-condition).
