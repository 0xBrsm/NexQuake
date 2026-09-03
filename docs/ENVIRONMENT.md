# Environment Reference

Configure Nexus with the following environment variables. Defaults are provided for a standard Docker setup.

NexQuake is designed to be drop-in compatible with any `protocol 15` (NetQuake) dedicated server binary. The bundled `nqserver` is the stock WinQuake engine with minimal patches. Put custom server binaries in `SERVER_DIR`; Nexus prepends that directory to `PATH` ahead of `BIN_DIR`.

## Startup Options

| Variable | Default | Description |
|------|---------|-------------|
| `HTTP_PORT` | `1337` | Main HTTP(S) and WebSocket listener port; also the WebTransport UDP/QUIC port when WebTransport is enabled (see [WebTransport](#webtransport)). |
| `QUICKSTART` | `ffa` | Quickstart catalog entries from `CFG_DIR/game.json` (for example `ctf,arena` or `all`). Invalid names are ignored. See [Quickstart Catalog](../etc/README.md). |
| `LOG_LEVEL` | `info` | Operator console verbosity. Accepts: `error`, `warn`, `info`, `debug`. The on-disk `nexus.log` always records full debug detail regardless. |
| `CONSOLE_TIMESTAMPS` | `1` | Timestamps on operator console log lines. Accepts: `0`, `1`. |
| `DEBUG_RELAY` | `0` | Logs UDP relay traffic with source/destination, length, and byte preview. Accepts: `0`, `1`. |
| `SV_MAX_INSTANCES` | `1` | Per-line `servers.ini` scaling cap for `-port 0` startup entries (minimum `1`). Controls the maximum instances a server may spawn. `1` (the default) disables autoscaling and hides the `slist` instance-count suffix for those entries. Set higher (for example `10`) to enable demand-driven scale-out. |

## Run Paths

One variable decides the deployment shape: `EXTERNAL_URL` — the server's public identity.

| Setup | Shape | Minimum env beyond defaults | Transports |
|-------|-------|------------------------------|------------|
| Default | LAN, dev, or behind any reverse proxy / tunnel | none (behind a front: `AUTH_CLIENT_IP_HEADER`) | HTTP + WebSocket |
| `EXTERNAL_URL` set | Public server — Nexus owns the endpoint with a real certificate | `EXTERNAL_URL=https://quake.example.com` | HTTPS + WebSocket + WebTransport |

**Default (no `EXTERNAL_URL`)** is plain HTTP and WebSocket only — NexQuake as it worked before 1.11. `docker run -p 1337:1337` and play at `http://localhost:1337` or from the LAN; no certificates anywhere. It is also the shape for running behind a reverse proxy or tunnel that owns public TLS (Cloudflare Tunnel being the canonical case): no public IP, no open inbound ports, and an access gate like Cloudflare Access can require IdP login for *everything* — page, assets, and play — with no Nexus configuration. A second access policy scoped to the `/rcon` path gates admin the same way (see [Authentication](#authentication)). One opt-in variable makes Nexus front-aware: `AUTH_CLIENT_IP_HEADER` (e.g. `CF-Connecting-IP`) so bans and audit logs target players instead of the proxy edge.

**`EXTERNAL_URL` set** is the public server: the hostname becomes the certificate identity (automatic Let's Encrypt), the listener serves HTTPS/WSS, and WebTransport is advertised alongside — nothing separate to configure:

```bash
docker run -p 443:1337 -p 443:1337/udp \
  -e EXTERNAL_URL=https://quake.example.com \
  -v nexquake-cert:/app/cert ...
```

Requires the hostname to resolve to your public IP and the public port serving the page (`443` here) to reach `HTTP_PORT` over both TCP and UDP. The certificate is obtained over the `TLS-ALPN-01` challenge on the main TLS listener — no plain-HTTP port is needed.

## TLS

| Variable | Default | Description |
|------|---------|-------------|
| `EXTERNAL_URL` | empty | The server's public identity (`https://host`, nothing else). Setting it enables HTTPS, WSS, and WebTransport; the hostname is the certificate identity. There is no port to configure — clients are routed by the authority each request arrives on. See [Run Paths](#run-paths). |
| `CERT_DIR` | `/app/cert` | Certificate directory. If it holds `cert.pem` + `key.pem`, Nexus serves that bring-your-own cert directly. Otherwise it's the ACME account/cert cache (under `acme/`) — persist it across restarts to avoid re-issuing. |

## WebTransport

WebTransport gives the tunnel UDP-like delivery (unreliable QUIC datagrams) instead of WebSocket's TCP semantics, which removes head-of-line blocking during network hiccups. It is automatic whenever `EXTERNAL_URL` is set and absent otherwise. There is nothing to configure: each `/gamedir` response advertises the WebTransport URL at the exact authority the request arrived on, so whatever address and port work for the page work for QUIC.

The QUIC listener binds UDP on `HTTP_PORT` — TCP and UDP socket spaces don't collide, so the WebSocket and WebTransport listeners share the port number. The one deployment rule: whatever public port serves the page must also reach `HTTP_PORT` over UDP (e.g. `443:1337/udp` next to `443:1337`). A mismatched mapping black-holes silently: the page and `/gamedir` ride TCP and look fine; only QUIC fails.

Clients treat WebTransport as an upgrade, never a requirement: the session warms up in the background at page load, connections use it only once its handshake has already landed, and WebSocket carries play in the meantime. An unreachable WebTransport endpoint costs nothing beyond a background retry every 20 seconds.

## Authentication

Admin access (`/rcon`) takes two credentials: a shared-secret **password**, or **SSO** — a verified OIDC login. Nexus is always *verify-only* — it never holds a client secret or runs a server-side login flow. How the browser obtains the SSO token (an OIDC JWT) depends on the deployment:

- **Behind a front:** let the access gate (e.g. Cloudflare Access) handle IdP login. The simple recipe: one access policy for the site, a second admins-only policy scoped to the `/rcon` path, and `AUTH_RCON_PASSWORD` as the Nexus-side credential. The in-game `rcon login` opens `/rcon` as a popup so the front can run its IdP round-trip. For per-admin identity in Nexus's own audit logs, additionally verify the JWT the front injects (e.g. `AUTH_JWT_HEADER=Cf-Access-Jwt-Assertion`).
- **Direct exposure (`EXTERNAL_URL` set, no front):** set `AUTH_ISSUER` + `AUTH_AUDIENCE` and the in-game `rcon login` runs an Authorization Code + PKCE flow as a **public client** (no secret). The browser drives the authorize redirect and the `/rcon` callback, then hands the code + PKCE verifier to Nexus at `POST /rcon/session`; Nexus does the token exchange server-to-server and returns the verified id_token in an **httpOnly `nq_session` cookie** that the verify-only layer reads on later `/rcon` calls. The token never enters page JavaScript, and the server-side hop sidesteps IdPs whose token endpoint isn't browser-CORS-reachable (e.g. Cloudflare Access). On the IdP side, register the client as **public / SPA** (no client secret, PKCE required) with its redirect URI set to your site origin + `/rcon` (e.g. `https://play.example.com/rcon`).

PKCE engages only in the direct-exposure shape: `EXTERNAL_URL` is set **and** `AUTH_JWT_HEADER` is the default (`Authorization`). Behind a front, `EXTERNAL_URL` is unset, so PKCE stays off and the browser uses the edge-gated popup while Nexus reads the token from the header the front asserts. Both conditions are required because a front may inject the JWT via the standard `Authorization: Bearer` (oauth2-proxy, Pomerium, Envoy), which the header alone can't tell apart from direct exposure — `EXTERNAL_URL` (set only when standing alone) is the disambiguator. The two modes are mutually exclusive and chosen automatically.

The login scopes aren't configured directly — they're derived from `AUTH_ADMIN_ID` so you never have to map a scope name to a claim by hand. `openid profile email` is always requested (spec-defined, accepted everywhere, and the source of audit-log identity); if any admin rule keys on `groups`, the `groups` scope is added too. Group-based gating still requires your IdP to actually emit a `groups` claim (some need it switched on in token config or a login action; the claim key is almost always `groups`, but Entra emits group GUIDs and Auth0 requires a namespaced claim). If a verified token is denied, check the debug log for the claim keys it carried.

| Variable | Default | Description |
|------|---------|-------------|
| `AUTH_ISSUER` | empty | OIDC Issuer URL (e.g. `https://accounts.google.com`). Both this and `AUTH_AUDIENCE` must be set to enable JWT verification. |
| `AUTH_AUDIENCE` | empty | OIDC Audience (Client ID) the JWT must be issued for. |
| `AUTH_CLIENT_ID` | `AUTH_AUDIENCE` | Public client id used by the PKCE login (browser authorize + the server-side `/rcon/session` exchange). Defaults to `AUTH_AUDIENCE` (the common case where the id_token's `aud` is the client id); set it only when your IdP separates the API audience from the client id. Login flow only — does not affect token verification. |
| `AUTH_JWT_HEADER` | `Authorization` | HTTP header carrying the OIDC JWT. The default verifies a `Bearer <jwt>` from scripted callers and the `nq_session` cookie the PKCE login sets; set a front-injected header (e.g. `Cf-Access-Jwt-Assertion`) to verify the identity your access gate asserts (which also disables PKCE). |
| `AUTH_CLIENT_IP_HEADER` | empty | Trusted header for real client IPs behind a front (e.g. `CF-Connecting-IP`, `X-Forwarded-For`). Behind-a-front deployments only — without it, source-IP bans would hit the proxy edge and block everyone. Falls back to the direct connection IP when unset or unparseable. |
| `AUTH_ADMIN_ID` | empty | Admin-grant policy for verified SSO logins. **Empty (default): no login grants admin** (fail-closed). Set a comma-separated list of OIDC claim matchers (e.g. `email:user@example.com`, `groups:admins`) to grant admin to claims matching any one of them. Set to `any` to grant admin to **every** verified login — i.e. delegate authorization entirely to your IdP/edge (who it lets log in). A malformed entry (not `key:value` and not `any`) is a fatal startup error. Matching is case-insensitive; logs identify users by `email`, `preferred_username`, `name`, or `sub`. |
| `AUTH_RCON_PASSWORD` | empty | Shared-secret password for admin access. Clients present it as `Authorization: Rcon <password>` (in-game, driven by the `rcon_password` cvar). The old-school option — works with or without SSO. |

## Client-Side Options

| Variable | Default | Description |
|------|---------|-------------|
| `CL_CONCURRENCY` | `16` | Sets the number of game files to download simultaneously. Set `0` for unbounded (capped by queue size). |
| `CL_SMENU` | `0` | If `1`, auto-opens the server search menu on client start up to make joining a server easier. Skipped when the client is already headed into a game (`+connect`, `+map`, `+playdemo`), including from `CL_ARGS`, URL args, or `autoexec.cfg`. |
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
