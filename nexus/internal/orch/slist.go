package orch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/nqnet"
)

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
	udpConn, err := net.ListenUDP("udp4", nqnet.RelayListenAddr())
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

func fillRunningPorts(mgr *ServerManager, dst []int) []int {
	dst = dst[:0]

	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	for port, ids := range mgr.serverIDsByPort {
		if port < 1 || port > 65535 {
			continue
		}
		for _, serverID := range ids {
			rec := mgr.serversByID[serverID]
			if rec == nil || rec.Running == nil || rec.Running.Cmd == nil || rec.Running.Cmd.Process == nil {
				continue
			}
			if !isProcessAlive(rec.Running.Cmd.Process) {
				continue
			}
			dst = append(dst, port)
			break
		}
	}
	return dst
}

func staleAfterForTargets(targetCount int) time.Duration {
	pollPeriod := time.Duration(targetCount) * serverInfoPollStep
	staleAfter := pollPeriod * 10
	if staleAfter < 30*time.Second {
		staleAfter = 30 * time.Second
	}
	return staleAfter
}

func SnapshotForSlist(mgr *ServerManager) []nqnet.ServerListEntry {
	ports := fillRunningPorts(mgr, nil)
	staleAfter := staleAfterForTargets(len(ports))
	now := time.Now()

	mgr.mu.RLock()
	out := make([]nqnet.ServerListEntry, 0, len(ports))
	for _, port := range ports {
		ids := mgr.serverIDsByPort[port]
		var rec *serverRecord
		for _, serverID := range ids {
			r := mgr.serversByID[serverID]
			if r == nil || r.Running == nil || r.Running.Cmd == nil || r.Running.Cmd.Process == nil {
				continue
			}
			if !isProcessAlive(r.Running.Cmd.Process) {
				continue
			}
			rec = r
			break
		}
		if rec == nil {
			continue
		}
		if now.Sub(rec.LastSeen) > staleAfter {
			continue
		}

		if rec.spec == nil {
			continue
		}

		out = append(out, nqnet.ServerListEntry{
			ListenPort: port,
			Hostname:   rec.Hostname,
			MapName:    rec.MapName,
			GameDir:    activeGameDir(rec.spec.SearchPath),
			Players:    rec.Players,
			MaxPlayers: rec.MaxPlayers,
		})
	}
	mgr.mu.RUnlock()

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
	req := nqnet.BuildCCREQServerInfo()

	var ports []int

	// Prime quickly at startup.
	ports = fillRunningPorts(p.mgr, ports)
	for _, port := range ports {
		dst := nqnet.ServerUDPAddr(p.serverIP, port)
		if _, err := conn.WriteToUDP(req, dst); err != nil && errors.Is(err, net.ErrClosed) {
			return
		}
	}

	pollOne := func(port int) bool {
		dst := nqnet.ServerUDPAddr(p.serverIP, port)
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

		srcPort, ok := nqnet.ServerSourcePortFromAddr(src)
		if !ok {
			continue
		}

		hostname, mapName, players, maxPlayers, _, ok := nqnet.ParseCCREPServerInfo(buf[:n])
		if !ok {
			continue
		}

		p.mgr.updateGameState(srcPort, hostname, mapName, players, maxPlayers)
	}
}
