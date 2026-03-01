package orch

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
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
	Instances  uint16 // backend instance count (≥1)
}

// buildCCREPServerList builds an aggregated CCREP_SERVER_INFO response
// containing multiple server entries. Returns the datagram and entry count.
func buildCCREPServerList(entries []serverListEntry) ([]byte, int) {
	const maxNetDatagramSize = 1024 + 8

	buf := make([]byte, 0, 512)
	buf = append(buf, 0, 0, 0, 0) // placeholder header
	buf = append(buf, ccrepServerInfo)
	countIndex := len(buf)
	buf = append(buf, 0) // count placeholder

	count := 0
	for _, e := range entries {
		serverPort := e.ListenPort
		if serverPort <= 0 || serverPort > 65535 {
			continue
		}
		hostname := e.Hostname
		mapName := e.MapName
		gameDir := e.GameDir
		if hostname == "" {
			hostname = "UNNAMED"
		}
		if mapName == "" {
			mapName = "?"
		}
		if gameDir == "" {
			gameDir = "id1"
		}

		serverPortText := strconv.Itoa(serverPort)
		hostname = truncateSlistField(hostname, hostcacheNameMax)
		mapName = truncateSlistField(mapName, hostcacheFieldMax)
		gameDir = truncateSlistField(gameDir, hostcacheFieldMax)

		instances := e.Instances
		if instances == 0 {
			instances = 1
		}

		entrySize := len(serverPortText) + 1 + len(hostname) + 1 + len(mapName) + 1 + len(gameDir) + 1 + 7
		if len(buf)+entrySize > maxNetDatagramSize {
			break
		}

		buf = appendSlistCString(buf, serverPortText)
		buf = appendSlistCString(buf, hostname)
		buf = appendSlistCString(buf, mapName)
		buf = appendSlistCString(buf, gameDir)
		buf = append(buf, byte(e.Users&0xff), byte(e.Users>>8))
		buf = append(buf, byte(e.MaxUsers&0xff), byte(e.MaxUsers>>8))
		buf = append(buf, byte(instances&0xff), byte(instances>>8))
		buf = append(buf, netProtocolVersion)
		count++
	}

	buf[countIndex] = byte(count)
	control := netFlagCtl | uint32(len(buf))
	binary.BigEndian.PutUint32(buf[0:4], control)
	return buf, count
}

func truncateSlistField(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes]
}

func appendSlistCString(buf []byte, s string) []byte {
	buf = append(buf, s...)
	return append(buf, 0)
}

// IsSlistRequest reports whether payload is a valid CCREQ_SERVER_INFO datagram.
func IsSlistRequest(payload []byte) bool {
	if len(payload) < 4+1 {
		return false
	}
	control := binary.BigEndian.Uint32(payload[0:4])
	if (control&^netFlagLengthMask) != netFlagCtl || int(control&netFlagLengthMask) != len(payload) {
		return false
	}
	if payload[4] != ccreqServerInfo {
		return false
	}
	i := 5
	end := i
	for end < len(payload) && payload[end] != 0 {
		end++
	}
	if end >= len(payload) || string(payload[i:end]) != "QUAKE" {
		return false
	}
	proto := end + 1
	return proto < len(payload) && payload[proto] == netProtocolVersion
}

// BuildSlistResponse builds an aggregated CCREP_SERVER_INFO response datagram
// from the current state of all managed servers.
func (m *ServerManager) BuildSlistResponse() []byte {
	entries := snapshotForSlist(m)
	data, _ := buildCCREPServerList(entries)
	return data
}

// listenAddr returns an unspecified UDP listen address (any port).
func listenAddr() *net.UDPAddr {
	return &net.UDPAddr{IP: net.IPv4zero, Port: 0}
}

// serverUDPAddr returns the UDP address for a server on the given port.
func serverUDPAddr(serverIP net.IP, port int) *net.UDPAddr {
	return &net.UDPAddr{IP: serverIP, Port: port}
}

// serverSourcePortFromAddr extracts the source port from a UDP address.
func serverSourcePortFromAddr(addr net.Addr) (int, bool) {
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok || udpAddr.Port < 1 || udpAddr.Port > 65535 {
		return 0, false
	}
	return udpAddr.Port, true
}

const serverInfoPollStep = 500 * time.Millisecond

// serverInfoPoller polls running backends via CCREQ_SERVER_INFO and forwards
// replies to the [ServerManager] to keep game-state metadata current.
type serverInfoPoller struct {
	mgr      *ServerManager
	serverIP net.IP

	pollConn *net.UDPConn
	stopOnce sync.Once
}

func newServerInfoPoller(mgr *ServerManager, serverIP net.IP) *serverInfoPoller {
	return &serverInfoPoller{mgr: mgr, serverIP: serverIP}
}

// StartInfoPoller starts a server-info poller for mgr and returns a stop
// function. If the UDP socket cannot be bound, it logs a warning and returns
// a no-op stop function. The poller also stops when ctx is cancelled.
func (mgr *ServerManager) StartInfoPoller(ctx context.Context, serverIP net.IP) func() {
	p := newServerInfoPoller(mgr, serverIP)
	if err := p.Start(ctx); err != nil {
		mgr.warnf("Server info poller disabled: %v", err)
		return func() {}
	}
	return p.Stop
}

// Start opens the UDP socket and launches the poll and read goroutines.
// It returns an error if the UDP socket cannot be bound.
// The poller stops when ctx is cancelled or Stop is called.
func (p *serverInfoPoller) Start(ctx context.Context) error {
	udpConn, err := net.ListenUDP("udp4", listenAddr())
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

	for port, ids := range mgr.serverIDsByPort {
		if port < 1 || port > 65535 {
			continue
		}
		for _, serverID := range ids {
			rec := mgr.serversByID[serverID]
			if !mgr.serverRecordRunningLocked(rec) {
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

func staleAfterForTargets(targetCount int) time.Duration {
	pollPeriod := time.Duration(targetCount) * serverInfoPollStep
	staleAfter := pollPeriod * 10
	if staleAfter < 30*time.Second {
		staleAfter = 30 * time.Second
	}
	return staleAfter
}

func snapshotForSlist(mgr *ServerManager) []serverListEntry {
	now := time.Now()

	mgr.mu.Lock()
	ports := fillRunningPortsLocked(mgr, nil)
	staleAfter := staleAfterForTargets(len(ports))
	out := make([]serverListEntry, 0, len(ports)+len(mgr.poolsByID))

	pools := make([]*serverPool, 0, len(mgr.poolsByID))
	for _, pool := range mgr.poolsByID {
		if pool != nil {
			pools = append(pools, pool)
		}
	}
	sort.Slice(pools, func(i, j int) bool {
		return pools[i].Line < pools[j].Line
	})
	for _, pool := range pools {
		if pool.AggregateInstances == 0 {
			continue
		}
		backendPort, ok := mgr.pickPoolBackendLocked(pool)
		if !ok {
			continue
		}
		notePoolDemandLocked(pool, now)
		out = append(out, serverListEntry{
			ListenPort: backendPort,
			Hostname:   pool.DisplayHostname,
			MapName:    pool.DisplayMap,
			GameDir:    pool.DisplayGameDir,
			Users:      pool.AggregateUsers,
			MaxUsers:   pool.AggregateMaxUsers,
			Instances:  pool.AggregateInstances,
		})
	}

	for _, port := range ports {
		ids := mgr.serverIDsByPort[port]
		var rec *serverRecord
		for _, serverID := range ids {
			r := mgr.serversByID[serverID]
			if !mgr.serverRecordRunningLocked(r) || mgr.poolByServerID[r.id] != nil {
				continue
			}
			rec = r
			break
		}
		if rec == nil || now.Sub(rec.LastSeen) > staleAfter || rec.spec == nil {
			continue
		}

		out = append(out, serverListEntry{
			ListenPort: port,
			Hostname:   rec.Hostname,
			MapName:    rec.MapName,
			GameDir:    activeGameDir(rec.spec.SearchPath),
			Users:      uint16(rec.Players),
			MaxUsers:   uint16(rec.MaxPlayers),
			Instances:  1,
		})
	}
	mgr.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		hi := out[i].Hostname
		hj := out[j].Hostname
		if hi == "" {
			hi = "UNNAMED"
		}
		if hj == "" {
			hj = "UNNAMED"
		}
		hi = strings.ToLower(hi)
		hj = strings.ToLower(hj)
		if hi != hj {
			return hi < hj
		}
		return out[i].ListenPort < out[j].ListenPort
	})
	return out
}

func (p *serverInfoPoller) pollLoop(ctx context.Context, conn *net.UDPConn) {
	req := buildCCREQServerInfo()

	var ports []int

	// Prime quickly at startup.
	ports = fillRunningPorts(p.mgr, ports)
	for _, port := range ports {
		dst := serverUDPAddr(p.serverIP, port)
		if _, err := conn.WriteToUDP(req, dst); err != nil && errors.Is(err, net.ErrClosed) {
			return
		}
	}

	pollOne := func(port int) bool {
		dst := serverUDPAddr(p.serverIP, port)
		if _, err := conn.WriteToUDP(req, dst); err != nil {
			return !errors.Is(err, net.ErrClosed)
		}
		return true
	}

	ticker := time.NewTicker(serverInfoPollStep)
	defer ticker.Stop()

	p.mgr.debugf("Server info poller: polling %d targets every %s (round-robin step %s)",
		len(ports), time.Duration(len(ports))*serverInfoPollStep, serverInfoPollStep)

	rrNext := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.mgr.reconcileAllPools()
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

		srcPort, ok := serverSourcePortFromAddr(src)
		if !ok {
			continue
		}

		hostname, mapName, players, maxPlayers, _, ok := parseCCREPServerInfo(buf[:n])
		if !ok {
			continue
		}

		p.mgr.updateGameState(srcPort, hostname, mapName, players, maxPlayers)
	}
}
