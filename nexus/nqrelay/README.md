# nqrelay

`nqrelay` is the Nexus networking package that bridges browser WebSocket traffic to NetQuake UDP servers. This document is for engineers importing or vendoring `github.com/0xBrsm/NexQuake/nexus/nqrelay`.

## Import and Versioning

- **Go module:** `github.com/0xBrsm/NexQuake/nexus` (see `src/nexus/go.mod`).
- **Package import path:** `github.com/0xBrsm/NexQuake/nexus/nqrelay`.
- **Versioning model:** the package is versioned with NexQuake commits/tags, not as a standalone module.

## Public API Map

| Area | Symbols | Purpose | Caller Contract |
|------|---------|---------|-----------------|
| WebSocket upgrade | `Upgrader` | Shared Gorilla upgrader (`binary` subprotocol, no compression). | `CheckOrigin` defaults to allow-all; set a stricter policy if needed. |
| Relay construction | `NewRelay`, `FrameDispatch`, `Relay` | Creates one WebSocket-to-UDP relay per client session. | `alloc` and `sessions` must be non-nil. `warnf` and `debugf` may be nil. |
| Relay lifecycle | `(*Relay).Run`, `(*Relay).Close` | Starts relay loops, cleans up sockets/session/IP. | `Run` blocks until closed. `Close` is idempotent. |
| Relay metadata | `VirtualClientIP`, `ClientIP`, `SourceKey`, `SourceIP`, `UserID`, `SessionID`, `IsAdmin`, `PromoteAdmin`, `NoteServerRoutePort` | Session identity and tracking helpers. | `NoteServerRoutePort` ignores invalid ports. |
| Relay control replies | `SendControlReply`, `SendAdminReply` | Sends control-channel data to client. | Empty payloads/messages are ignored. |
| Session snapshots | `NewSessionRegistry`, `(*SessionRegistry).SnapshotAll`, `(*SessionRegistry).SnapshotByVirtualIP`, `SessionSnapshot`, `BanTarget` | Active-session inspection and ban target projection. | `SnapshotByVirtualIP` returns `(nil, nil)` on invalid/unknown VIP. |
| Virtual IP allocator | `NewIPAllocator`, `(*IPAllocator).ReserveAndBlock`, `(*IPAllocator).IsBlocked` | Deterministic relay VIP allocation and ban-state tracking. | VIP space is `127.x.x.x`; blocked source keys cannot allocate. |
| Client identity parsing | `ParseClientIP`, `ResolveClientSourceIP`, `ResolveClientSourceKey` | Parses forwarded/client source identity from HTTP requests. | Treat request headers as untrusted input. |
| Quake server query codec | `BuildCCREQServerInfo`, `ParseCCREPServerInfo` | Build/parse `CCREQ_SERVER_INFO`/`CCREP_SERVER_INFO`. | `ParseCCREPServerInfo` validates framing and protocol version. |
| UDP helpers | `DefaultNQServerIP`, `ListenAddr`, `ServerUDPAddr`, `ServerSourcePortFromAddr` | Shared UDP address and port utilities. | `ServerSourcePortFromAddr` rejects non-UDP or invalid port values. |
| Frame constants | `ControlPort`, `MinServerPort`, `MaxServerPort`, `WSPortHeaderSize` | Tunnel frame contract values. | Keep client and server implementations aligned with these values. |

## WebSocket Client Contract

Every tunnel frame is a WebSocket binary message with a two-byte big-endian port header.

```text
0               1               2...
+---------------+---------------+----------------------+
| dst/src port (uint16, BE)     | payload bytes        |
+---------------+---------------+----------------------+
```

| Direction | Port Header | Payload | Behavior |
|-----------|-------------|---------|----------|
| Client -> relay | `0` (`ControlPort`) | Control data (application-defined). | Routed to `FrameDispatch.HandleControlFrame`. |
| Client -> relay | `1..65535` | UDP payload for a server port. | Forwarded over UDP if allowed by `IsAllowedPort`. |
| Relay -> client | `0` (`ControlPort`) | Control reply bytes. | Sent when control handler returns a non-empty response or when `SendControlReply` is used. |
| Relay -> client | `1..65535` | UDP datagram bytes read from server. | Source port is encoded in header. |

On relay startup, the client receives a control-channel identity frame with payload:

```text
"NQIP" + 4 bytes virtual IPv4
```

Use this to learn the relay-assigned virtual IP for session/admin workflows.

## Relay Integration Contract

`NewRelay` is the main constructor and enforces core invariants:

1. Allocates a unique virtual client IP via `IPAllocator`.
2. Binds a UDP socket on that VIP (`udp4`, ephemeral source port).
3. Tracks the session in `SessionRegistry`.
4. Uses `FrameDispatch` callbacks for control frames and close hooks.

Important runtime behavior:

- Non-binary WebSocket messages are ignored.
- Invalid tunnel frames and invalid destination ports are dropped.
- If `FrameDispatch.IsAllowedPort` is set, disallowed ports are dropped.
- The relay write queue is bounded (`1024` frames). Drop-enabled sends are silently dropped with a warning log when full.

## FrameDispatch Callback Contract

| Callback | Trigger | Return/Effect | Notes |
|----------|---------|---------------|-------|
| `HandleControlFrame` | Incoming frame on `ControlPort` (`0`). | Return bytes to send back to client on control channel; return `nil`/empty to send nothing. | Payload ownership stays with caller logic; copy if you need to retain data. |
| `IsAllowedPort` | Incoming non-control frame before UDP write. | `true` allows write; `false` drops frame. | Use this as the main policy gate for managed/unmanaged UDP targets. |
| `HandleClose` | Relay close path (`Close`) executes once. | No return. | Called before session untrack/socket teardown. |

## Minimal Integration Example

```go
package main

import (
	"net"
	"net/http"

	"github.com/0xBrsm/NexQuake/nexus/nqrelay"
)

var (
	alloc    = nqrelay.NewIPAllocator(net.ParseIP(nqrelay.DefaultNQServerIP).To4())
	sessions = nqrelay.NewSessionRegistry()
)

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := nqrelay.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	sourceIP := nqrelay.ResolveClientSourceIP(r)
	sourceKey := nqrelay.ResolveClientSourceKey(r)

	dispatch := nqrelay.FrameDispatch{
		HandleControlFrame: func(relay *nqrelay.Relay, payload []byte) []byte {
			return nil
		},
		IsAllowedPort: func(port int) bool { return port >= 26000 && port <= 27000 },
	}

	relay, err := nqrelay.NewRelay(conn, sourceKey, sourceIP, "", false, alloc, sessions, dispatch, nil, nil)
	if err != nil {
		_ = conn.Close()
		return
	}
	relay.Run()
}
```

## Vendoring Checklist

1. Keep client and relay frame formats identical (`WSPortHeaderSize`, control port, big-endian port encoding).
2. Keep `ControlPort` payload semantics consistent with your own `HandleControlFrame`.
3. Decide and enforce your own `Upgrader.CheckOrigin` policy.
4. Define `IsAllowedPort` so clients cannot target unmanaged UDP ports.
5. Keep `SourceKey` stable across reconnects if you depend on deterministic VIP mapping.

See the [Nexus README](../README.md) for package-level context and wiring.
