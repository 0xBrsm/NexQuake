# Configuration Reference

Configure Nexus with the following environment variables. Defaults are provided for a standard Docker setup.

NexQuake is designed to be drop-in compatible with any protocol 15 (NetQuake) dedicated server binary. The bundled `nqserver` is the stock WinQuake engine with minimal patches, but you can substitute any conformant server binary by placing it in `BIN_DIR` and referencing it in `servers.ini`.

## Networking

| Variable | Default | Description |
|------|---------|-------------|
| `HTTP_PORT` | `1337` | Main HTTP and WebSocket listener port. |
| `CORS_ALLOWED_ORIGIN` | empty | CORS `Access-Control-Allow-Origin` header value. Only set when serving assets cross-origin. |
| `NQSERVER_IP` | `127.13.37.9` | Internal loopback IP address for the dedicated servers. Nexus binds each client's UDP relay socket to a virtual IP and forwards datagrams to `NQSERVER_IP:<port>`. |

## Authentication

| Variable | Default | Description |
|------|---------|-------------|
| `AUTH_RCON_PASSWORD` | empty | Shared secret for `rcon_password`. If set, enables admin sessions. |
| `AUTH_ISSUER` | empty | OIDC Issuer URL (e.g. `https://accounts.google.com`). |
| `AUTH_AUDIENCE` | empty | OIDC Audience (Client ID). |
| `AUTH_JWT_HEADER` | `Authorization` | Custom HTTP header for OIDC JWT token. |
| `AUTH_ADMIN_ID` | empty | Comma-separated list of OIDC claims (`email:user@example.com`, `groups:admins`) required for admin access. Logs will identify users by their request token's `email`, `preferred_username`, or `sub`. |
| `AUTH_CLIENT_IP_HEADER` | empty | HTTP header to trust for client IP resolution (e.g. `CF-Connecting-IP`, `X-Forwarded-For`, `X-Real-IP`). If unset or the header value is invalid, falls back to the direct connection IP. |

## Paths & Data

| Variable | Default | Description |
|------|---------|-------------|
| `GAME_DIR` | `/app/game` | Root directory for game data (`id1`, mods). |
| `CD_DIR` | `/app/cd` | Root directory for CD audio tracks (`.ogg`/`.mp3`). |
| `LOGS_DIR` | `/app/logs` | Where Nexus and server logs are written. |
| `BIN_DIR` | `/app/bin` | Location of server binaries (`nqserver`). |
| `CLIENT_DIR` | `/app/bin/nqwasm` | Location of WASM client assets (`index.wasm`, `index.html`). |
| `CLIENT_BATCH_SIZE` | `16` | Concurrency for VFS prefetching (PAK streaming). |
| `DEBUG_STARTUP` | `0` | Set to `1` for verbose startup logging (artifact fingerprints). |

## Startup Options

| Variable | Default | Description |
|------|---------|-------------|
| `QUICKSTART` | `minimal` | Bootstrap manifest name. Supports comma-separated lists (e.g. `minimal,ctf4`). See `src/manifests/README.md`. |
| `LOG_LEVEL` | `info` | Logging verbosity. Accepts: `error`, `warn`, `info`, `debug`. |
| `LOG_CONSOLE_TIMESTAMPS` | `on` | Timestamps on operator console log lines. Accepts: `on`/`off` (or `1`/`0`, `true`/`false`, `yes`/`no`). |
