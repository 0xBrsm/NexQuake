# Configuration Reference

Configure Nexus with the following environment variables. Defaults are provided for a standard Docker setup.

NexQuake is designed to be drop-in compatible with any protocol 15 (NetQuake) dedicated server binary. The bundled `nqserver` is the stock WinQuake engine with minimal patches. Put custom server binaries in `SERVER_BIN`; Nexus prepends that directory to `PATH` ahead of `BIN_DIR`.

## Startup Options

| Variable | Default | Description |
|------|---------|-------------|
| `HTTP_PORT` | `1337` | Main HTTP and WebSocket listener port. |
| `QUICKSTART` | `ffa` | Quickstart catalog entries from `CFG_DIR/game.json` (for example `ctf,arena` or `all`). Invalid names are ignored. See [QUICKSTART](QUICKSTART.md). |
| `LOG_LEVEL` | `info` | Logging verbosity. Accepts: `error`, `warn`, `info`, `debug`. |
| `CONSOLE_TIMESTAMPS` | `1` | Timestamps on operator console log lines. Accepts: `0`, `1`. |
| `DEBUG_RELAY` | `0` | Logs UDP relay traffic with source/destination, length, and byte preview. Accepts: `0`, `1`. |

## Authentication

| Variable | Default | Description |
|------|---------|-------------|
| `AUTH_ISSUER` | empty | OIDC Issuer URL (e.g. `https://accounts.google.com`). |
| `AUTH_AUDIENCE` | empty | OIDC Audience (Client ID). |
| `AUTH_JWT_HEADER` | `Authorization` | HTTP header for OIDC JWT token. |
| `AUTH_ADMIN_ID` | empty | Comma-separated list of OIDC claims (e.g. `email:user@example.com`, `group:admins`) required for admin access. Logs will identify users by their request token's `email`, `preferred_username`, or `sub`. |
| `AUTH_CLIENT_IP_HEADER` | empty | HTTP header to trust for client IP resolution (e.g. `CF-Connecting-IP`, `X-Forwarded-For`, `X-Real-IP`). If unset or the header value is invalid, falls back to the direct connection IP. |
| `AUTH_RCON_PASSWORD` | empty | Legacy-style shared secret for in-game `rcon_password`. |

## Client-Side Options

| Variable | Default | Description |
|------|---------|-------------|
| `CL_CONCURRENCY` | `16` | Sets the number of game files to download simultaneously. Set `0` for unbounded (capped by queue size). |
| `CL_SMENU` | `0` | If `1`, auto-opens the server search menu on client start up to make joining a server easier. |
| `CL_ARGS` | empty | Set launch arguments for client start up in shell-style strings (i.e. use single quotes for multiple args). |
| `CL_URL_ARGS` | `0` | If `1`, lets players add extra client startup arguments in the URL after `?`, separated by `&`. |

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
| `SERVER_BIN` | `/app/server` | Bind-mountable override directory for custom server binaries. Searched before `BIN_DIR`. |
| `CLIENT_DIR` | `/app/bin/nqwasm` | Location of WASM client assets (`index.wasm`, `index.html`). |
