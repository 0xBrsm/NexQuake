# Admin Commands

Nexus implements a system-wide admin protocol using the `rcon` command. You can issue these commands using the in-game console (`~` key) if you are authenticated as an admin.

Authentication is handled either by connection-level OIDC JWT auth or by shared-secret rcon password. For OIDC mode, `AUTH_ADMIN_ID` is optional: if unset, any valid JWT accepted by `AUTH_ISSUER`/`AUTH_AUDIENCE` is treated as admin. For password mode, the client sends `Authorization: Rcon <password>`; in-game this is driven by the `rcon_password` cvar. The rcon password is a non-archived cvar that will not be saved to `config.cfg` when set. Adding it to your `config.cfg` directly isn't recommended, though this will enable automatic elevation on connection.

External tools can reach the same command surface over HTTP by POSTing JSON-RPC 2.0 envelopes to `/rcon` with the same `Authorization` header. Use `rpc.discover` to list methods and `rcon.help` for the in-game form.

## Targeting

| Form | Behavior |
|------|----------|
| `rcon <cmd...>` | If connected to a server, forwards to that server's console. If disconnected, dispatches to Nexus admin. |
| `rcon nexus <cmd...>` | Forces a Nexus admin dispatch even while connected to a game server. |
| `rcon <port> <cmd...>` | Forwards a raw console command to the instance listening on that port (1–65535). |

## Nexus Command Reference

| Command | Usage | Description |
|---------|-------|-------------|
| **help** | `rcon nexus help` | Show the list of Nexus rcon commands. |
| **tail** | `rcon nexus tail` | Show the last 10 Nexus log lines. Instance tail is `rcon <port> tail`. |
| **server list** | `rcon nexus server list [<idx\|all>]` | With no argument, list managed servers (name, candidate connect port, game directory, player count, state). With `all` or a server index, list instances in the grouped format for every server or the selected one. |
| **server start** | `rcon nexus server start <idx\|all>` | Start a specific server by the server index shown in `rcon nexus server list`. Use `all` to start every server defined in `servers.ini`. |
| **server stop** | `rcon nexus server stop <idx\|all> [secs]` | Stop a specific server or all servers. Sends a graceful `quit` command first, then terminates if it doesn't exit within the grace window. |
| **server restart** | `rcon nexus server restart <idx\|all> [secs]` | Restart a specific server or all servers. Equivalent to `stop` followed by `start`. |
| **server remove** | `rcon nexus server remove <idx>` | Remove a **stopped** server from the runtime registry. Useful for cleaning up temporary `server launch` instances. |
| **server launch** | `rcon nexus server launch <binary> [args...]` | Launch and register a new server dynamically. Example: `rcon nexus server launch nqserver -dedicated -game ctf +map ctf2m3`. |
| **session list** | `rcon nexus session list` | List all connected client sessions. Displays role, user identity, server, and port. If no authenticated user identity is available, shows the public source IP in parentheses. |
| **session info** | `rcon nexus session info <nqip>` | Show details for one session by NQIP, including source identity and status-derived player slot/address when connected to a server. |
| **session ban** | `rcon nexus session ban <nqip>` | Ban one session by NQIP from all servers and disconnect it immediately. Nexus issues server `kick` by status slot first (best effort), then closes session sockets and blocks route identity until Nexus restart. Admin sessions cannot be banned. |

## Privileged Gameplay Commands (`please`)

Retail Quake contained a server admin feature, but it was compile-flag gated and not generally available in stock retail builds. NexQuake's dedicated server build enables it with `-DIDGODS`.

Usage flow:

1. Enable the feature on the target server (`idgods 1` in config, or launch with `+idgods 1`).
2. As a global Nexus rcon admin:
    1. Run `rcon <port> status` (if not connected) or just `status` if connected to the target server.
    2. Note the number of the player you want to promote.
    3. Run `rcon <port> please # <player number>`.
3. That player is promoted to admin on that server only.
4. The promoted player can then use `cmd <server command>` from their client console, and can also run privileged gameplay commands directly (`god`, `notarget`, `noclip`, `fly`, `give`, `kick`, `ban`). Note that `ban` in this case is only for that particular game server. To ban a player from the entire NexQuake instance, you must be a global admin and use `rcon session ban`.
