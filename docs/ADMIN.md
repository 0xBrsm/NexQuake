# Admin Commands

Nexus implements a system-wide admin protocol using the `rcon` command. You can issue these commands using the in-game console (`~` key) if you are authenticated as an admin.

Authentication is handled either by connection-level OIDC JWT auth or by shared-secret rcon password. For OIDC mode, `AUTH_ADMIN_ID` is optional: if unset, any valid JWT accepted by `AUTH_ISSUER`/`AUTH_AUDIENCE` is treated as admin. For password mode, the client sends `Authorization: Rcon <password>`; in-game this is driven by the `rcon_password` cvar. The rcon password is a non-archived cvar that will not be saved to `config.cfg` when set. Adding it to your `config.cfg` directly isn't recommended, though this will enable automatic elevation on connection.

External tools can reach the same command surface over HTTP by POSTing JSON-RPC 2.0 envelopes to `/rcon` with the same `Authorization` header. See [HTTP API](#http-api) below for the full reference.

Throughout this document, **NQIP** refers to a client's NexQuake IP: the deterministic `127.x.x.x` loopback address Nexus assigns per-client from a hashed source key. Clients are identified by NQIP in `client.info`, `client.ban`, and the `client.list` output; inside running game servers the NQIP appears as the player's address in commands like `status`.

## Targeting

| Form | Behavior |
|------|----------|
| `rcon <cmd...>` | If connected to a server, forwards to that server's console. If disconnected, dispatches to Nexus admin. |
| `rcon nexus <cmd...>` | Forces a Nexus admin dispatch even while connected to a game server. |
| `rcon <port> <cmd...>` | Forwards a raw console command to the instance listening on that port (1–65535). |
| `rcon login` | Browser/WASM client only. Opens an OIDC popup for an edge-gated `/rcon`; on success Nexus pushes a `rcon: authenticated.` echo back via the trunk control channel. Has no JSON-RPC equivalent (the API has no popup to open). |

## Nexus Command Reference

| Command | Usage | Description |
|---------|-------|-------------|
| **help** | `rcon nexus help` | Show the list of Nexus rcon commands. |
| **tail** | `rcon nexus tail [N]` | Show the last `N` Nexus log lines (default 10). Instance tail is `rcon <port> tail`. |
| **server list** | `rcon nexus server list [<idx\|all>]` | With no argument, list managed servers (name, candidate connect port, game directory, player count, state). With `all` or a server index, list instances in the grouped format for every server or the selected one. |
| **server start** | `rcon nexus server start <idx\|all>` | Start a specific server by the server index shown in `rcon nexus server list`. Use `all` to start every server defined in `servers.ini`. |
| **server stop** | `rcon nexus server stop <idx\|all> [secs]` | Stop a specific server or all servers. Sends a graceful `quit` command first, then terminates if it doesn't exit within the grace window. |
| **server restart** | `rcon nexus server restart <idx\|all> [secs]` | Restart a specific server or all servers. Equivalent to `stop` followed by `start`. |
| **server remove** | `rcon nexus server remove <idx>` | Remove a **stopped** server from the runtime registry. Useful for cleaning up temporary `server launch` instances. |
| **server launch** | `rcon nexus server launch <binary> [args...]` | Launch and register a new server dynamically. Example: `rcon nexus server launch nqserver -dedicated -game ctf +map ctf2m3`. |
| **client list** | `rcon nexus client list` | List all connected clients with NQIP, source IP tagged with the tunnel transport (`(wt)`/`(ws)`), and user identity when available. |
| **client info** | `rcon nexus client info <nqip>` | Show details for one active client by NQIP, including the tunnel transport. |
| **client ban** | `rcon nexus client ban <nqip>` | Ban one client's source IP until Nexus restart and disconnect it immediately. |

## Privileged Gameplay Commands (`please`)

Retail Quake contained a server admin feature, but it was compile-flag gated and not generally available in stock retail builds. NexQuake's dedicated server build enables it with `-DIDGODS`.

Usage flow:

1. Enable the feature on the target server (`idgods 1` in config, or launch with `+idgods 1`).
2. As a global Nexus rcon admin:
    1. Run `rcon <port> status` (if not connected) or just `status` if connected to the target server.
    2. Note the number of the player you want to promote.
    3. Run `rcon <port> please # <player number>`.
3. That player is promoted to admin on that server only.
4. The promoted player can then use `cmd <server command>` from their client console, and can also run privileged gameplay commands directly (`god`, `notarget`, `noclip`, `fly`, `give`, `kick`, `ban`). Note that `ban` in this case is only for that particular game server. To ban a player from the entire NexQuake instance, you must be a global admin and use `rcon client ban`.

## HTTP API

Nexus exposes the same admin command surface over HTTP as JSON-RPC 2.0 at `POST /rcon`. The in-game `rcon` client, the WASM shell, and external tools all dispatch through this endpoint.

### Endpoint and Authentication

- **Endpoints**: `POST /rcon` (JSON-RPC) and `GET /rcon` (auto-closing landing page used by the WASM shell's `rcon login` popup) on the Nexus HTTP listener (`HTTP_PORT`, default `1337`).
- **Content-Type**: `application/json` (POST). `GET /rcon` returns `text/html`.
- **Body size limit**: 8 KiB (POST).
- **Authentication** — one of:
  - `Authorization: Rcon <password>` — shared secret from `AUTH_RCON_PASSWORD`.
  - `Authorization: Bearer <jwt>` — OIDC JWT. See `AUTH_ISSUER`, `AUTH_AUDIENCE`, and `AUTH_JWT_HEADER` in [ENVIRONMENT.md](./ENVIRONMENT.md). If `AUTH_JWT_HEADER` is set, Nexus reads the JWT from that header instead of `Authorization` — useful behind a proxy that writes the token into a non-standard header.
- **Pre-auth IP block**: requests from source IPs banned via `client.ban` are rejected with HTTP `403 Forbidden` before JSON parsing.
- **Unauthorized**: a valid envelope with an unauthenticated or non-admin caller returns JSON-RPC error `-32000 unauthorized`. The error envelope's `data.hint` field carries an operator-facing one-liner describing how to authenticate against this Nexus, derived from what's actually configured (`set rcon_password <secret>`, `authenticate via OIDC`, both, or "admin auth not configured").
- **GET /rcon**: returns a small auto-closing HTML page. Reaching it proves an upstream OIDC gate (e.g. Cloudflare Access) let the request through; Nexus then pushes a `rcon: authenticated.` console echo down the trunk control channel to every active session sharing the request's source IP, so the in-game shell sees the success without any popup-tracking machinery (which COOP-same-origin breaks). Gated behind the same admin check as POST so it can't be used as an unauthenticated amplification primitive.

### Request and Response Envelope

All requests use the JSON-RPC 2.0 envelope:

```json
{"jsonrpc": "2.0", "method": "<method>", "params": { ... }, "id": 1}
```

- `jsonrpc` must be exactly `"2.0"`.
- `method` is the method name (required).
- `params` is the method-specific params object (omit or pass `null` for methods that take no params).
- `id` is echoed back in the response. Any JSON value works. Batching is not supported — one request per body.

Success response:

```json
{"jsonrpc": "2.0", "result": { ... }, "id": 1}
```

Error response:

```json
{"jsonrpc": "2.0", "error": {"code": -32601, "message": "method \"foo\" not found"}, "id": 1}
```

### Error Codes

| Code     | Name           | Meaning                                                             |
|---------:|----------------|---------------------------------------------------------------------|
| `-32700` | ParseError     | Body is not valid JSON.                                             |
| `-32600` | InvalidReq     | Envelope missing `jsonrpc`/`method`, or body empty.                 |
| `-32601` | MethodNotFound | Unknown method name.                                                |
| `-32602` | InvalidParams  | Params failed validation.                                           |
| `-32603` | Internal       | Unclassified server error.                                          |
| `-32000` | Unauthorized   | Valid envelope but the caller is not an admin. `error.data.hint` carries auth instructions. |
| `-32001` | NotFound       | Target does not exist (e.g. no client for the given NQIP).          |
| `-32002` | Conflict       | Operation not allowed in the current state (e.g. banning an admin). |
| `-32003` | Dispatch       | Downstream operation failed (server/instance manager, PTY, etc.).   |
| `-32004` | Unavailable    | Required capability is not wired in this build.                     |

### Method Reference

#### `rcon.help`
Render the in-game command help text.

- Params: _none_.
- Result: `{"text": "<multi-line help string>"}`.

#### `rpc.discover`
List all available methods with their descriptions.

- Params: _none_.
- Result: `{"methods": [{"name": "server.list", "description": "list managed servers"}, ...]}`.

#### `logs.tail`
Return the last N lines of the Nexus log ring buffer.

- Params: `{"lines": <int>}` (optional; default 10).
- Result: `{"lines": ["<timestamped log line>", ...]}`.

#### `server.list`
List managed servers (one per `servers.ini` line) with aggregate state across their running instances.

- Params: _none_.
- Result: `{"servers": [ServerInfo, ...]}`.

`ServerInfo` fields: `line` (0-based servers.ini line), `hostname`, `map_name`, `candidate_port`, `listen_port`, `game_dir`, `players`, `max_players`, `instances`, `state`.

#### `server.instances`
List running instances grouped by server.

- Params: `{"index": <int>}` — 1-based server index; omit to get all servers.
- Result: `{"servers": [{"index": 1, "hostname": "...", "game_dir": "...", "state": "running", "candidate_port": 26000, "players": 2, "max_players": 16, "instances": [ServerInfo, ...]}, ...]}`.

#### `server.start`
Start one server or all servers.

- Params: `{"target": "<idx>|all"}` — 1-based server index as a string, or `"all"`.
- Result: `{"ok": true}`.

#### `server.stop`
Gracefully stop one server or all servers. Sends `quit` to the server console first, then SIGTERM, then SIGKILL if still alive.

- Params: `{"target": "<idx>|all", "grace_seconds": <int>}` — `grace_seconds` optional (default 2).
- Result: `{"ok": true}`.

#### `server.restart`
Stop then start a server (or all). Same params and result as `server.stop`.

#### `server.remove`
Remove a stopped server from the runtime registry. Intended for cleaning up `server.launch` entries.

- Params: `{"index": <int>}` — 1-based server index.
- Result: `{"removed": true}`.

#### `server.launch`
Launch and register a new server dynamically. Binary is resolved under `SERVER_DIR` first, then `BIN_DIR`.

- Params: `{"binary": "<name>", "args": ["<arg>", ...]}`.
- Result: `{"ok": true}`.

#### `server.instance.command`
Forward a raw console command to a specific instance's PTY and capture the reply. This is the method the in-game `rcon <port> <cmd...>` form calls.

- Params: `{"port": <int>, "cmd": "<text>"}` — `port` is the listen port of a live instance (1..65535).
- Result: `{"reply": "<captured console output>"}`.

#### `client.list`
List all active clients.

- Params: _none_.
- Result: `{"clients": [Connection, ...]}`.

`Connection` fields: `nqip`, `source_ip`, `identity` (caller's OIDC identity if available), `transport`, `server_port`, `server_host`, `connected_at`.

#### `client.info`
Detail for one active client.

- Params: `{"nqip": "<127.x.x.x>"}`.
- Result: `{"client": Connection}`.
- Errors: `-32001 NotFound` if the NQIP has no active client.

#### `client.ban`
Disconnect one active client matching an NQIP and block its source IP until Nexus restart. Nexus sends the client a console message, then closes the connection.

- Params: `{"nqip": "<127.x.x.x>"}`.
- Result: `{"nqip": "<nqip>", "source_ips": ["<ip>"], "disconnected": 1}`.
- Errors: `-32001 NotFound` if the NQIP has no active client.

### Example Flows

Discover available methods:

```bash
curl -sH 'Authorization: Rcon s3cret' \
     -H 'Content-Type: application/json' \
     -d '{"jsonrpc":"2.0","method":"rpc.discover","id":1}' \
     http://nexus:1337/rcon | jq '.result.methods[].name'
```

List servers, then stop server 1 with a 5-second grace:

```bash
curl -sH 'Authorization: Rcon s3cret' -H 'Content-Type: application/json' \
     -d '{"jsonrpc":"2.0","method":"server.list","id":1}' \
     http://nexus:1337/rcon

curl -sH 'Authorization: Rcon s3cret' -H 'Content-Type: application/json' \
     -d '{"jsonrpc":"2.0","method":"server.stop","params":{"target":"1","grace_seconds":5},"id":2}' \
     http://nexus:1337/rcon
```

Forward a console command to the instance listening on port 26000:

```bash
curl -sH 'Authorization: Rcon s3cret' -H 'Content-Type: application/json' \
     -d '{"jsonrpc":"2.0","method":"server.instance.command","params":{"port":26000,"cmd":"status"},"id":3}' \
     http://nexus:1337/rcon
```

Ban a client by NQIP:

```bash
curl -sH 'Authorization: Rcon s3cret' -H 'Content-Type: application/json' \
     -d '{"jsonrpc":"2.0","method":"client.ban","params":{"nqip":"127.100.10.1"},"id":4}' \
     http://nexus:1337/rcon
```

### Audit Log

Each authorized dispatch and each unauthorized valid RPC request emits one or more audit lines to the Nexus log. The format is:

```
admin-rcon <direction> actor="<id>" method=<method> <direction>=<payload>
```

- `direction` is `request` (admitted), `result` (handler succeeded), or `error` (authorization failure, missing method, invalid params, or handler error).
- `actor` is the caller's OIDC identity if available, otherwise their source IP. If neither is available, Nexus logs `admin` for an authorized request and `anonymous` for an unauthorized caller.
- `<payload>` carries the params (on `request`), result (on `result`), or error message (on `error`), with CR/LF collapsed to spaces and truncated at 512 characters.

Example:

```
admin-rcon request actor="alice@example.com" method=server.stop request={"target":"1","grace_seconds":5}
admin-rcon result actor="alice@example.com" method=server.stop result={"ok":true}
admin-rcon error actor="198.51.100.11" method=client.ban error="nqip is required"
```
