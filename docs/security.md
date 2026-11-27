# Authentication and remote access

API and viewer access allow keyboard/mouse control of the target. Bind to loopback
and leave `debug-keys` disabled unless diagnosing input. Browser view-only mode
only affects that viewer.

Keep the shared secret in a private file so it can be updated separately:

```sh
umask 077
openssl rand -hex 32 > ./autopaws.token
```

Set `token-file: /absolute/path/to/autopaws.token` in the shared YAML file.
Alternatively, store the secret directly as `token: "REPLACE_WITH_A_RANDOM_SECRET"`.
Token values must be nonempty UTF-8; surrounding whitespace is trimmed.
Both processes read the configuration and secret once at startup; restart them
after changing either. Nonempty `token` and `token-file` together are rejected.
Leaving both empty disables authentication.

Open `http://127.0.0.1:9330/#token=ENCODED_SECRET`, using `encodeURIComponent(secret)`.
The viewer removes the fragment and stores the token in origin-scoped
`sessionStorage` for that tab. New tabs and installed-app launches may need the
token URL again; open it in a browser tab, then use the browser's Open in app
action where available. The viewer sends the token in a WebSocket subprotocol
offer, which the daemon verifies before opening capture or accepting input.
API clients send the `token` field or use `apiclient.NewWithToken`.

Tokens do not encrypt traffic. For a remote daemon bound to loopback, forward
both ports:

```sh
ssh -L 9329:127.0.0.1:9329 -L 9330:127.0.0.1:9330 kvmhost
```

Connect MCP to local port 9329 and open `http://127.0.0.1:9330` for the viewer.
The loopback IP provides a secure context for AudioWorklet. Plain HTTP to a LAN
address does not; built-in HTTPS and reverse-proxy deployments are unsupported.

Without a token, the viewer accepts WebSocket requests only for `localhost` or
IP-literal hosts. Configure a token when accessing it by another hostname.
Viewer responses deny framing by other pages. HTTP requests have 10-second
header/read/write deadlines and a 60-second idle timeout, with a 64 KiB header
limit. Upgraded WebSockets use the viewer's own write deadlines.
