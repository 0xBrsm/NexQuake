package orch

import (
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

	"github.com/0xBrsm/NexQuake/nexus/nqrelay"
)

// Quake client hostcache field limits (see net.h in the patched client).
const (
	hostcacheNameMax  = 23
	hostcacheFieldMax = 15
)

// serverListEntry describes a single server for the aggregated slist response.
type serverListEntry struct {
	ListenPort int
	Hostname   string
	MapName    string
	GameDir    string
	Users      uint16
	MaxUsers   uint16
	Instances  uint16
}

// buildCCREPServerList builds an aggregated CCREP_SERVER_INFO response
// containing multiple server entries. Returns the datagram and entry count.
func buildCCREPServerList(entries []serverListEntry) ([]byte, int) {
	const (
		netFlagLengthMask  uint32 = 0x0000ffff
		netFlagCtl         uint32 = 0x80000000
		netProtocolVersion byte   = 3
		ccrepServerInfo    byte   = 0x83
		maxNetDatagramSize        = 1024 + 8
	)

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

// isSlistRequest reports whether payload is a valid CCREQ_SERVER_INFO datagram.
func isSlistRequest(payload []byte) bool {
	const (
		netFlagLengthMask  uint32 = 0x0000ffff
		netFlagCtl         uint32 = 0x80000000
		ccreqServerInfo    byte   = 0x02
		netProtocolVersion byte   = 3
	)
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

// NewControlHandler returns a FrameDispatch HandleControlFrame function that
// handles CCREQ_SERVER_INFO slist requests via mgr, and delegates all other
// port-0 traffic to handleAdmin.
func NewControlHandler(mgr *ServerManager, handleAdmin func(relay *nqrelay.Relay, payload []byte)) func(*nqrelay.Relay, []byte) []byte {
	return func(relay *nqrelay.Relay, payload []byte) []byte {
		if isSlistRequest(payload) {
			entries := snapshotForSlist(mgr)
			data, _ := buildCCREPServerList(entries)
			return data
		}
		if handleAdmin != nil {
			handleAdmin(relay, payload)
		}
		return nil
	}
}

const serverInfoPollStep = 500 * time.Millisecond

type serverInfoPoller struct {
	mgr      *ServerManager
	serverIP net.IP

	pollConn *net.UDPConn
	stopOnce sync.Once
}

func NewServerInfoPoller(mgr *ServerManager, serverIP net.IP) *serverInfoPoller {
	return &serverInfoPoller{mgr: mgr, serverIP: serverIP}
}

func (p *serverInfoPoller) Start(ctx context.Context) error {
	udpConn, err := net.ListenUDP("udp4", nqrelay.ListenAddr())
	if err != nil {
		return fmt.Errorf("server info poller: listen udp: %w", err)
	}

	p.pollConn = udpConn

	go p.readLoop(ctx, udpConn)
	go p.pollLoop(ctx, udpConn)

	return nil
}

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
	req := nqrelay.BuildCCREQServerInfo()

	var ports []int

	// Prime quickly at startup.
	ports = fillRunningPorts(p.mgr, ports)
	for _, port := range ports {
		dst := nqrelay.ServerUDPAddr(p.serverIP, port)
		if _, err := conn.WriteToUDP(req, dst); err != nil && errors.Is(err, net.ErrClosed) {
			return
		}
	}

	pollOne := func(port int) bool {
		dst := nqrelay.ServerUDPAddr(p.serverIP, port)
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

		srcPort, ok := nqrelay.ServerSourcePortFromAddr(src)
		if !ok {
			continue
		}

		hostname, mapName, players, maxPlayers, _, ok := nqrelay.ParseCCREPServerInfo(buf[:n])
		if !ok {
			continue
		}

		p.mgr.updateGameState(srcPort, hostname, mapName, players, maxPlayers)
	}
}
