# RCON Commands

Nexus implements a system-wide admin protocol using the `rcon` command. You can issue these commands using the in-game console (`~` key) if you are authenticated as an admin.

Authentication is handled either by connection-level OIDC JWT auth or by in-game `rcon_password`. For OIDC mode, `AUTH_ADMIN_ID` is optional: if unset, any valid JWT accepted by `AUTH_ISSUER`/`AUTH_AUDIENCE` is treated as admin. The rcon password is a non-archived cvar that will not be saved to `config.cfg` when set. Adding it to your `config.cfg` directly isn't recommended, though this will enable automatic elevation on connection.

## Targeting

| Form | Behavior |
|------|----------|
| `rcon <cmd>` | If connected to a server, sends to that server. If disconnected, sends to Nexus (`port 0`). |
| `rcon nexus <cmd>` | Forces a Nexus command even while connected to a game server. |
| `rcon <host> <cmd>` | Targets a server by `slist` hostname cache entry. |
| `rcon <port> <cmd>` | Targets a server by listen port. |

## Nexus Command Reference

| Command | Usage | Description |
|---------|-------|-------------|
| **help** | `rcon nexus help` | Show the list of Nexus rcon commands. |
| **tail** | `rcon nexus tail` | Show the last 10 Nexus log lines. Server tail is `rcon <host\|port> tail`. |
| **slist** | `rcon nexus slist` | List all managed game servers and their current status (running, stopped, crashed). Displays server name, port, game directory, player count, and state. |
| **session list** | `rcon nexus session list` | List all connected client sessions. Displays role, user identity, server, and port. If no authenticated user identity is available, shows the public source IP in parentheses. |
| **session info** | `rcon nexus session info <idx>` | Show details for one session index from `session list`, including NQIP/source identity and status-derived player slot/address when connected to a server. |
| **session ban** | `rcon nexus session ban <idx>` | Ban one session index from all servers and disconnect it immediately. Nexus issues server `kick` by status slot first (best effort), then closes session sockets and blocks route identity until Nexus restart. Admin sessions cannot be banned. |
| **start** | `rcon nexus start <idx\|port\|all>` | Start a specific server by slot index (1-based from `slist`) or listen port. Use `all` to start every server defined in `servers.ini`. |
| **stop** | `rcon nexus stop <idx\|port\|all>` | Stop a specific server or all servers. Sends a graceful `quit` command first, then terminates if it doesn't exit. |
| **restart** | `rcon nexus restart <idx\|port\|all>` | Restart a specific server or all servers. Equivalent to `stop` followed by `start`. |
| **remove** | `rcon nexus remove <idx\|port>` | Remove a **stopped** server from the runtime registry. Useful for cleaning up temporary `launch` instances. |
| **launch** | `rcon nexus launch <binary> [args...]` | Launch and register a new server instance dynamically. Example: `rcon nexus launch nqserver -dedicated -game ctf +map ctf2m3`. |

## Privileged Gameplay Commands (`please`)

Retail Quake contained a server admin feature, but it was compile-flag gated and not generally available in stock retail builds. NexQuake's dedicated server build enables it with `-DIDGODS`.

Usage flow:

1. Enable the feature on the target server (`idgods 1` in config, or launch with `+idgods 1`).
2. As a global Nexus rcon admin:
    1. Run `rcon <host|port> status` (if not connected) or just `status` if connected to the target server.
    2. Note the number of the player you want to promote.
    3. Run `rcon <host|port> please # <player number>`.
3. That player is promoted to admin on that server only.
4. The promoted player can then use `cmd <server command>` from their client console, and can also run privileged gameplay commands directly (`god`, `notarget`, `noclip`, `fly`, `give`, `kick`, `ban`). Note that `ban` in this case is only for that particular game server. To ban a player from the entire NexQuake instance, you must be a global admin and use `rcon session ban`.
