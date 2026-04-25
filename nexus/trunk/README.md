# Trunk

Per-client browser↔UDP tunnel library. One `Conn` per connected client: a
pluggable `Transport` (WebSocket today) carries binary frames with a 2-byte
big-endian port header, which the `Conn` demultiplexes to UDP datagrams
aimed at a localhost backend. Each client is assigned a deterministic
127.x.x.x VirtualIP via `VirtualIPAllocator` so the backend sees distinct
source addresses.

**Import path:** `github.com/0xBrsm/NexQuake/nexus/trunk`
**Module:** `github.com/0xBrsm/NexQuake/nexus` (`src/nexus/go.mod`)
**Versioned with:** NexQuake commits/tags (not a separate module)

Full API documentation is in the Go source — run `go doc ./...` or visit
[pkg.go.dev](https://pkg.go.dev/github.com/0xBrsm/NexQuake/nexus/trunk).

## Vendoring checklist

1. Frame format is fixed: 2-byte big-endian port header + payload. Keep client and relay in sync.
2. Port `0` is the control channel. Define your own payload semantics in `HandleControlFrame`.
3. Override `Upgrader.CheckOrigin` for production deployments.
4. Set `IsAllowedPort` to restrict clients to specific UDP destinations.
5. Keep `sourceKey` stable across reconnects if deterministic VirtualIP identity matters.
