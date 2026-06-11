# Environment Reference

Configure Nexus with the following environment variables. Defaults are provided for a standard Docker setup.

NexQuake is designed to be drop-in compatible with any `protocol 15` (NetQuake) dedicated server binary. The bundled `nqserver` is the stock WinQuake engine with minimal patches. Put custom server binaries in `SERVER_DIR`; Nexus prepends that directory to `PATH` ahead of `BIN_DIR`.

## Startup Options

| Variable | Default | Description |
|------|---------|-------------|
| `HTTP_PORT` | `1337` | Main HTTP and WebSocket listener port; also the WebTransport UDP/QUIC port when `WT_HOST` is set (see [WebTransport](#webtransport)). |
| `QUICKSTART` | `ffa` | Quickstart catalog entries from `CFG_DIR/game.json` (for example `ctf,arena` or `all`). Invalid names are ignored. See [Quickstart Catalog](../etc/README.md). |
| `LOG_LEVEL` | `info` | Operator console verbosity. Accepts: `error`, `warn`, `info`, `debug`. The on-disk `nexus.log` always records full debug detail regardless. |
| `CONSOLE_TIMESTAMPS` | `1` | Timestamps on operator console log lines. Accepts: `0`, `1`. |
| `DEBUG_RELAY` | `0` | Logs UDP relay traffic with source/destination, length, and byte preview. Accepts: `0`, `1`. |
| `SV_MAX_INSTANCES` | `1` | Per-line `servers.ini` scaling cap for `-port 0` startup entries (minimum `1`). Controls the maximum instances a server may spawn. `1` (the default) disables autoscaling and hides the `slist` instance-count suffix for those entries. Set higher (for example `10`) to enable demand-driven scale-out. |

## WebTransport

WebTransport gives the tunnel UDP-like delivery (unreliable QUIC datagrams) instead of WebSocket's TCP semantics, which removes head-of-line blocking during network hiccups. It is optional: when `WT_HOST` is unset, clients use WebSocket only.

| Variable | Default | Description |
|------|---------|-------------|
| `WT_HOST` | empty | Externally-reachable WebTransport host, optionally with a port (for example `quake.example.com` or `10.0.0.5:1337`). Used verbatim as the advertised URL authority and pushed to clients via `/start`. Empty disables WebTransport entirely. |
| `WT_CERT_DIR` | `/app/tls` | Directory for the auto-generated self-signed certificate and key. |
| `WT_CERT_ROTATE_DAYS` | `9` | Certificate rotation interval in days (range `1`-`12`; browsers cap hash-pinned certificates at 14 days and rotation must land a day before expiry). Out-of-range values warn and fall back to the default. |

The QUIC listener binds UDP on `HTTP_PORT` — TCP and UDP socket spaces don't collide, so the WebSocket and WebTransport listeners share the port number. Any published UDP port must map to `HTTP_PORT` on the container, even when `WT_HOST` advertises a different external port. A mismatched mapping black-holes silently (the page and `/start` ride TCP and look fine; only QUIC fails). For example, advertising port `4443`:

```bash
docker run -p 1337:1337 -p 4443:1337/udp -e WT_HOST=quake.example.com:4443 ...
```

No CA-signed certificate is needed: Nexus generates a short-lived self-signed certificate and pins its hash to clients through the `/start` manifest.

### Deployment Shapes

Reverse proxies and CDNs do not forward WebTransport (HTTP/3 over QUIC), so the UDP path must reach Nexus directly. Pick the shape that matches who owns the public endpoint:

- **Direct or proxy-on-same-host (recommended):** set `WT_HOST` to the same hostname players load the page from, and forward that host's UDP port straight to Nexus while TCP continues through the proxy. Same host means same address space, so browsers connect silently — no permission prompts or warnings.
- **CDN or tunnel in front:** the proxied hostname cannot carry WebTransport. Point `WT_HOST` at a direct DNS record (or `IP:port`) that bypasses the CDN. Public page to public endpoint also connects silently.
- **Public page, LAN endpoint:** setting `WT_HOST` to a private address (for example `pi.local`) while the page is served from a public origin triggers the browser's Local Network Access permission prompt. The client handles this gracefully — it plays over WebSocket until the prompt is granted and picks up WebTransport on the next connection — but prefer one of the shapes above when possible.

Clients treat WebTransport as an upgrade, never a requirement: the session warms up in the background at page load, connections use it only once its handshake has already landed, and WebSocket carries play in the meantime. An unreachable `WT_HOST` costs nothing beyond a background retry every 20 seconds.

## Authentication

| Variable | Default | Description |
|------|---------|-------------|
| `AUTH_ISSUER` | empty | OIDC Issuer URL (e.g. `https://accounts.google.com`). |
| `AUTH_AUDIENCE` | empty | OIDC Audience (Client ID). |
| `AUTH_JWT_HEADER` | `Authorization` | HTTP header for OIDC JWT token. |
| `AUTH_ADMIN_ID` | empty | Optional comma-separated list of OIDC claim matchers (e.g. `email:user@example.com`, `group:admins`) required for admin access. If left empty, any successfully verified JWT from `AUTH_ISSUER` + `AUTH_AUDIENCE` is treated as admin. Logs identify users by `email`, `preferred_username`, `name`, or `sub`. |
| `AUTH_CLIENT_IP_HEADER` | empty | HTTP header to trust for client IP resolution (e.g. `CF-Connecting-IP`, `X-Forwarded-For`, `X-Real-IP`). If unset or the header value is invalid, falls back to the direct connection IP. |
| `AUTH_RCON_PASSWORD` | empty | Shared-secret password for admin access. Clients present it as `Authorization: Rcon <password>` (in-game, driven by the `rcon_password` cvar). |

## Client-Side Options

| Variable | Default | Description |
|------|---------|-------------|
| `CL_CONCURRENCY` | `16` | Sets the number of game files to download simultaneously. Set `0` for unbounded (capped by queue size). |
| `CL_SMENU` | `0` | If `1`, auto-opens the server search menu on client start up to make joining a server easier. |
| `CL_ARGS` | empty | Set launch arguments for client start up in shell-style strings (i.e. use single quotes for multiple args). |
| `CL_URL_ARGS` | `1` | If `1`, lets players add extra client startup arguments in the URL after `?`, separated by `&`. Set `0` to disable URL argument passthrough. |

### CL*ARGS Examples

#### Example: Always Disable Sound

```bash
CL_ARGS=-nosound
CL_URL_ARGS=0
```

#### Example: Name With Quotes (Safe Shell Form)

This works in `bash` because the whole value is single-quoted:

```bash
CL_ARGS='-nosound +name "Player1"'
CL_URL_ARGS=0
```

If you prefer double quotes around the whole value, escape the inner quotes:

```bash
CL_ARGS="-nosound +name \"Player1\""
CL_URL_ARGS=0
```

#### Example: Allow User Options

Enable URL arguments:

```bash
CL_ARGS='-nosound +skill 3 +name "BrowserPlayer"'
CL_URL_ARGS=1
```

Now a link can add extra startup arguments:

```text
https://quake.example.com/?+exec&ctf.cfg&+name&TheShadow
```

Notes:
- Use `&` between arguments.
- If you need a space inside an argument, write it as `%20` (for example `Player%20One`).

## Paths

| Variable | Default | Description |
|------|---------|-------------|
| `GAME_DIR` | `/app/game` | Root directory for game data (`id1`, mods). |
| `CFG_DIR` | `/app/etc` | Default configuration directory (`game.json`, `servers.ini`, cfg templates). |
| `CD_DIR` | `/app/cd` | Root directory for CD audio tracks (`.ogg`/`.mp3`). |
| `LOGS_DIR` | `/app/logs` | Where Nexus and server logs are written. |
| `BIN_DIR` | `/app/bin` | Core runtime binaries and bundled assets (`nexus`, `nqserver`, `nqwasm/`). |
| `SERVER_DIR` | `/app/server` | Bind-mountable override directory for custom server binaries. Searched before `BIN_DIR`. |
| `CLIENT_DIR` | `/app/bin/nqwasm` | Location of WASM client assets (`index.wasm`, `index.html`). |
