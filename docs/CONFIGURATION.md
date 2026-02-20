# Configuration Reference

Configure Nexus with the following environment variables. Defaults are provided for a standard Docker setup.

NexQuake is designed to be drop-in compatible with any protocol 15 (NetQuake) dedicated server binary. The bundled `nqserver` is the stock WinQuake engine with minimal patches, but you can substitute any conformant server binary by placing it in `BIN_DIR` and referencing it in `servers.ini`.

## Networking

| Variable | Default | Description |
|------|---------|-------------|
| `HTTP_PORT` | `1337` | Main HTTP and WebSocket listener port. |
| `CORS_ALLOWED_ORIGIN` | empty | CORS `Access-Control-Allow-Origin` header value. Only set when serving assets cross-origin. |

## Authentication

| Variable | Default | Description |
|------|---------|-------------|
| `AUTH_RCON_PASSWORD` | empty | Shared secret for `rcon_password`. If set, enables admin sessions. |
| `AUTH_ISSUER` | empty | OIDC Issuer URL (e.g. `https://accounts.google.com`). |
| `AUTH_AUDIENCE` | empty | OIDC Audience (Client ID). |
| `AUTH_JWT_HEADER` | `Authorization` | HTTP header for OIDC JWT token. |
| `AUTH_ADMIN_ID` | empty | Comma-separated list of OIDC claims (e.g. `email:user@example.com`, `group:admins`) required for admin access. Logs will identify users by their request token's `email`, `preferred_username`, or `sub`. |
| `AUTH_CLIENT_IP_HEADER` | empty | HTTP header to trust for client IP resolution (e.g. `CF-Connecting-IP`, `X-Forwarded-For`, `X-Real-IP`). If unset or the header value is invalid, falls back to the direct connection IP. |

## Paths & Data

| Variable | Default | Description |
|------|---------|-------------|
| `GAME_DIR` | `/app/game` | Root directory for game data (`id1`, mods). |
| `CD_DIR` | `/app/cd` | Root directory for CD audio tracks (`.ogg`/`.mp3`). |
| `LOGS_DIR` | `/app/logs` | Where Nexus and server logs are written. |
| `BIN_DIR` | `/app/bin` | Location of server binaries (`nqserver`). |
| `CLIENT_DIR` | `/app/bin/nqwasm` | Location of WASM client assets (`index.wasm`, `index.html`). |
| `CL_CONCURRENCY` | `16` | Concurrency for VFS prefetching (PAK streaming). Set `0` for unbounded (capped by queue size). |
| `CL_SMENU` | `0` | Auto-open server search menu once on first client start (`1`/`0`). Delivered as a runtime one-shot flag (not replayed via `stuffcmds`). |
| `CL_SEND_ARGS` | empty | Shell-style startup tokens for the client command line (for example `-nosound +skill 3`). `+` tokens follow standard Quake `stuffcmds` behavior. |
| `CL_URL_ARGS` | `0` | Append URL query tokens as startup tokens (`1`/`0`). Query parsing splits on `&` (standard URL query separator), so each `&`-separated value becomes one argv token. Only non `key=value` tokens are forwarded (for example `?-nosound&+skill&3`) and follow standard Quake command-line behavior. |

## Startup Options

| Variable | Default | Description |
|------|---------|-------------|
| `QUICKSTART` | `id1` | Bootstrap manifest name. Supports comma-separated lists (e.g. `id1,ctf4`). See `src/manifests/README.md`. |
| `LOG_LEVEL` | `info` | Logging verbosity. Accepts: `error`, `warn`, `info`, `debug`. |
| `CONSOLE_TIMESTAMPS` | `1` | Timestamps on operator console log lines. Accepts: `0`, `1`. |
| `DEBUG_RELAY` | `0` | Logs UDP relay traffic with source/destination, length, and byte preview. Accepts: `0`, `1`. |

## Argument Examples

### Server-Side Args (`servers.ini`)

```ini
# GAME_DIR/servers.ini
nqserver -dedicated 16 -port 26000 -game id1 +hostname "FragFest" +exec server.cfg
nqserver -dedicated 16 -port 26001 -game ctf +hostname "CTF Night" +map e1m2
```

### Client-Side Args (`CL_SEND_ARGS`, `CL_URL_ARGS`)

```bash
# Environment
CL_SEND_ARGS='-nosound +skill 3 +name "BrowserPlayer"'
CL_URL_ARGS=1
```

```text
# URL
https://quake.example.com/?-window&+exec&autoexec.cfg&+mlook&1
```

`&` is URL tokenization, not Quake spacing syntax. The URL above becomes argv tokens: `-window`, `+exec`, `autoexec.cfg`, `+mlook`, `1`.

For spaced values, URL-encode spaces and keep one token per `&` segment. Example:

```text
https://quake.example.com/?+name&Player%20One
```

`CL_SEND_ARGS` and URL tokens are appended into the Quake argv. `+` tokens run through normal `stuffcmds` parsing, and `key=value` URL parameters are ignored.
