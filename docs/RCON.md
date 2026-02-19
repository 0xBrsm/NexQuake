# RCON Commands

Nexus implements a system-wide admin protocol using the `rcon` command. You can issue these commands using the in-game console (`~` key) if you are authenticated as an admin.

Authentication is handled either by connection-level OIDC JWT auth or by in-game `rcon_password`. The rcon password is a non-archived cvar that will not be saved to config.cfg when set and it's recommended not to add it to config.cfg directly, though this will enable automatic elevation on connection.

## Command Reference

The rcon syntax is `rcon <host|port|nexus> <cmd>`. If you are connected to a server `rcon <cmd>` will send directly to that server. If you are connected to no server `rcon <cmd>` will send directly to Nexus.

| Command | Usage | Description |
|---------|-------|-------------|
| **help** | `rcon help` | Show the list of Nexus rcon commands. |
| **tail** | `rcon tail [idx\|port]` | Show the last 10 lines of log output. With no argument, shows Nexus service logs. With a server index or port, shows that server's console log. Useful for debugging startup issues or monitoring traffic. |
| **slist** | `rcon slist` | List all managed game servers and their current status (running, stopped, crashed). Displays port, hostname, game directory, player count, and state. |
| **sessions** | `rcon sessions` | List all connected client sessions. Shows their virtual NQIP (127.x.x.x), real IP, role (admin/client), and which server/port they are currently routed to. |
| **start** | `rcon start <idx\|port\|all>` | Start a specific server by its slot index (1-based from `slist`) or listen port. Use `all` to start every server defined in `servers.ini`. |
| **stop** | `rcon stop <idx\|port\|all>` | Stop a specific server or all servers. Sends a graceful `quit` command first, then terminates if it doesn't exit. |
| **restart** | `rcon restart <idx\|port\|all>` | Restart a specific server or all servers. Equivalent to `stop` followed by `start`. |
| **remove** | `rcon remove <idx\|port>` | Remove a **stopped** server from the runtime registry. Useful for cleaning up temporary `launch` instances. You cannot remove a server defined in `servers.ini` permanently (it will reappear on Nexus restart). |
| **launch** | `rcon launch <binary> [args...]` | Launch and register a new server instance dynamically. <br>Example: `rcon launch nqserver -dedicated -game ctf +map ctf2m3` |
| **ban** | `rcon ban <idx\|NQIP>` | Ban a session (by index from `sessions`) or specific NQIP address from all servers. Bans persist until Nexus is restarted. Admins cannot be banned. |
