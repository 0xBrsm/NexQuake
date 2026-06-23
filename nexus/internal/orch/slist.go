package orch

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"time"
)

// Quake wire-protocol constants (see quakedef.h / net.h).
const (
	netFlagLengthMask  uint32 = 0x0000ffff
	netFlagCtl         uint32 = 0x80000000
	netProtocolVersion byte   = 3
	ccreqServerInfo    byte   = 0x02
	ccrepServerInfo    byte   = 0x83
)

// buildCCREQServerInfo constructs a CCREQ_SERVER_INFO datagram.
func buildCCREQServerInfo() []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, 0, 0, 0, 0) // placeholder header
	buf = append(buf, ccreqServerInfo)
	buf = appendCString(buf, "QUAKE")
	buf = append(buf, netProtocolVersion)

	control := netFlagCtl | uint32(len(buf))
	binary.BigEndian.PutUint32(buf[0:4], control)
	return buf
}

// parseCCREPServerInfo extracts server info from a CCREP_SERVER_INFO response.
func parseCCREPServerInfo(payload []byte) (hostname, mapName string, players, maxPlayers, protocol byte, ok bool) {
	if len(payload) < 5 {
		return "", "", 0, 0, 0, false
	}
	control := binary.BigEndian.Uint32(payload[:4])
	if (control&^netFlagLengthMask) != netFlagCtl || int(control&netFlagLengthMask) != len(payload) || payload[4] != ccrepServerInfo {
		return "", "", 0, 0, 0, false
	}
	i := 5

	// server_address (ignored)
	_, next, ok := readCString(payload, i)
	if !ok {
		return "", "", 0, 0, 0, false
	}
	i = next

	hostname, next, ok = readCString(payload, i)
	if !ok {
		return "", "", 0, 0, 0, false
	}
	i = next
	mapName, next, ok = readCString(payload, i)
	if !ok {
		return "", "", 0, 0, 0, false
	}
	i = next

	if i+3 > len(payload) {
		return "", "", 0, 0, 0, false
	}
	players = payload[i]
	maxPlayers = payload[i+1]
	protocol = payload[i+2]
	return hostname, mapName, players, maxPlayers, protocol, protocol == netProtocolVersion
}

func appendCString(buf []byte, s string) []byte {
	buf = append(buf, []byte(s)...)
	buf = append(buf, 0)
	return buf
}

func readCString(buf []byte, start int) (string, int, bool) {
	if start < 0 || start >= len(buf) {
		return "", 0, false
	}
	rest := buf[start:]
	n := bytes.IndexByte(rest, 0)
	if n < 0 {
		return "", 0, false
	}
	return string(rest[:n]), start + n + 1, true
}

// Quake client hostcache field limits (see net.h in the patched client).
const (
	hostcacheNameMax  = 23
	hostcacheFieldMax = 15
)

// serverListEntry describes a single server entry in the aggregated slist response.
type serverListEntry struct {
	ListenPort int    // UDP port the server is listening on
	Hostname   string // server hostname (truncated to hostcacheNameMax)
	MapName    string // current map (truncated to hostcacheFieldMax)
	GameDir    string // active game directory (truncated to hostcacheFieldMax)
	Users      uint16 // current player count
	MaxUsers   uint16 // server capacity
	Instances  uint16 // instance instance count for autoscaled servers; 0 hides the server suffix
}

func normalizeServerListEntry(e serverListEntry) serverListEntry {
	if e.Hostname == "" {
		e.Hostname = "UNNAMED"
	}
	if e.MapName == "" {
		e.MapName = "?"
	}
	if e.GameDir == "" {
		e.GameDir = "id1"
	}
	e.Hostname = truncateSlistField(e.Hostname, hostcacheNameMax)
	e.MapName = truncateSlistField(e.MapName, hostcacheFieldMax)
	e.GameDir = truncateSlistField(e.GameDir, hostcacheFieldMax)
	return e
}

func truncateSlistField(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes]
}

// serverListEntriesLocked builds the live server-list entries. It is a pure
// read of current server state — no side effects. (Autoscale demand is noted
// by a real join signal via noteServerDemandLocked, never from listing the
// servers; see snapshotForSlist.)
func serverListEntriesLocked(mgr *ServerManager) []serverListEntry {
	out := make([]serverListEntry, 0, len(mgr.serversByID))
	for _, s := range mgr.serversLocked() {
		if s.aggregateInstances == 0 {
			continue
		}
		instancePort, ok := mgr.pickServerInstanceLocked(s, true)
		if !ok {
			continue
		}
		instances := uint16(0)
		if s.Autoscales {
			instances = s.joinableInstances
		}
		out = append(out, serverListEntry{
			ListenPort: instancePort,
			Hostname:   s.DisplayHostname,
			MapName:    s.DisplayMap,
			GameDir:    s.DisplayGameDir,
			// Advertise the joinable set so users/max/instances are consistent;
			// draining/warming instances inflate aggregate* but aren't joinable.
			Users:     s.joinableUsers,
			MaxUsers:  s.joinableMaxUsers,
			Instances: instances,
		})
	}
	return out
}

// SlistEntry is one server in the server-list snapshot the SSE state channel
// streams (GET /events). Fields mirror serverListEntry and the client
// hostcache_t. The JSON tags are the on-the-wire shape consumed by the client.
type SlistEntry struct {
	Port      int    `json:"port"`
	Hostname  string `json:"hostname"`
	Map       string `json:"map"`
	GameDir   string `json:"gamedir"`
	Users     uint16 `json:"users"`
	MaxUsers  uint16 `json:"maxusers"`
	Instances uint16 `json:"instances"`
}

// SlistEntries returns the current server-list entries, applying the same
// normalization (UNNAMED/?/id1 defaults, field truncation) and bad-port drop the
// wire response used. The caller composes and marshals the snapshot (the SSE
// state channel nests these under "servers").
func (m *ServerManager) SlistEntries() []SlistEntry {
	return slistEntriesFrom(snapshotForSlist(m))
}

// slistEntriesFrom normalizes and filters raw server-list entries into the
// client-facing shape (split out so the normalization is unit-testable).
func slistEntriesFrom(entries []serverListEntry) []SlistEntry {
	out := make([]SlistEntry, 0, len(entries))
	for _, e := range entries {
		if e.ListenPort <= 0 || e.ListenPort > 65535 {
			continue
		}
		e = normalizeServerListEntry(e)
		out = append(out, SlistEntry{
			Port:      e.ListenPort,
			Hostname:  e.Hostname,
			Map:       e.MapName,
			GameDir:   e.GameDir,
			Users:     e.Users,
			MaxUsers:  e.MaxUsers,
			Instances: e.Instances,
		})
	}
	return out
}

const serverInfoPollStep = 500 * time.Millisecond

// serverInfoPoller polls running instances via CCREQ_SERVER_INFO and forwards
// replies to the [ServerManager] to keep game-state metadata current.
type serverInfoPoller struct {
	mgr      *ServerManager
	serverIP net.IP

	pollConn *net.UDPConn
	stopOnce sync.Once
}

// StartInfoPoller starts a server-info poller for mgr and returns a stop
// function. If the UDP socket cannot be bound, it logs a warning and returns
// a no-op stop function. The poller also stops when ctx is cancelled.
func (mgr *ServerManager) StartInfoPoller(ctx context.Context, serverIP net.IP) func() {
	p := &serverInfoPoller{mgr: mgr, serverIP: serverIP}
	if err := p.Start(ctx); err != nil {
		slog.Warn(fmt.Sprintf("Server info poller disabled: %v", err))
		return func() {}
	}
	return p.Stop
}

// Start opens the UDP socket and launches the poll and read goroutines.
// It returns an error if the UDP socket cannot be bound.
// The poller stops when ctx is cancelled or Stop is called.
func (p *serverInfoPoller) Start(ctx context.Context) error {
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("server info poller: listen udp: %w", err)
	}

	p.pollConn = udpConn

	go p.readLoop(ctx, udpConn)
	go p.pollLoop(ctx, udpConn)

	return nil
}

// Stop closes the UDP socket and terminates both goroutines.
// Safe to call more than once.
func (p *serverInfoPoller) Stop() {
	p.stopOnce.Do(func() {
		if p.pollConn != nil {
			_ = p.pollConn.Close()
		}
	})
}

func fillRunningPortsLocked(mgr *ServerManager, dst []int) []int {
	dst = dst[:0]

	for port, ids := range mgr.instanceIDsByPort {
		if port < 1 || port > 65535 {
			continue
		}
		for _, serverID := range ids {
			rec := mgr.instancesByID[serverID]
			if !mgr.instanceRunningLocked(rec) {
				continue
			}
			dst = append(dst, port)
			break
		}
	}
	return dst
}

func fillRunningPorts(mgr *ServerManager, dst []int) []int {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return fillRunningPortsLocked(mgr, dst)
}

func snapshotForSlist(mgr *ServerManager) []serverListEntry {
	// Listing servers has no demand side effect: the SSE hub polls this ~2x/sec
	// per connected client (DEC-020), so treating a list as join intent made
	// every autoscaling server fabricate demand and spin replicas with zero
	// players. Demand comes from a real join signal (see noteServerDemandLocked).
	mgr.mu.Lock()
	out := serverListEntriesLocked(mgr)
	mgr.mu.Unlock()

	slices.SortFunc(out, func(a, b serverListEntry) int {
		return cmp.Or(
			cmp.Compare(strings.ToLower(a.Hostname), strings.ToLower(b.Hostname)),
			cmp.Compare(a.ListenPort, b.ListenPort),
		)
	})
	return out
}

func (p *serverInfoPoller) pollLoop(ctx context.Context, conn *net.UDPConn) {
	req := buildCCREQServerInfo()

	var ports []int

	// Prime quickly at startup.
	ports = fillRunningPorts(p.mgr, ports)
	for _, port := range ports {
		if _, err := conn.WriteToUDP(req, &net.UDPAddr{IP: p.serverIP, Port: port}); err != nil && errors.Is(err, net.ErrClosed) {
			return
		}
	}

	pollOne := func(port int) bool {
		if _, err := conn.WriteToUDP(req, &net.UDPAddr{IP: p.serverIP, Port: port}); err != nil {
			return !errors.Is(err, net.ErrClosed)
		}
		return true
	}

	ticker := time.NewTicker(serverInfoPollStep)
	defer ticker.Stop()

	slog.Debug(fmt.Sprintf("Server info poller: polling %d targets every %s (round-robin step %s)",
		len(ports), time.Duration(len(ports))*serverInfoPollStep, serverInfoPollStep))

	rrNext := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.mgr.reconcileAllServers()
			ports = fillRunningPorts(p.mgr, ports)
			if len(ports) == 0 {
				continue
			}
			if rrNext >= len(ports) {
				rrNext = 0
			}
			port := ports[rrNext]
			rrNext++
			if !pollOne(port) {
				return
			}
		}
	}
}

func (p *serverInfoPoller) readLoop(ctx context.Context, conn *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if ctx.Err() != nil {
				return
			}
			if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
				continue
			}
			continue
		}

		if src.Port < 1 || src.Port > 65535 {
			continue
		}

		hostname, mapName, players, maxPlayers, _, ok := parseCCREPServerInfo(buf[:n])
		if !ok {
			continue
		}

		p.mgr.updateGameState(src.Port, hostname, mapName, players, maxPlayers)
	}
}
