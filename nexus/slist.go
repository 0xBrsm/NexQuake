package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

const serverInfoPollStep = 500 * time.Millisecond

type ServerInfoPoller struct {
	mgr *ServerManager

	pollConn *net.UDPConn
	stopOnce sync.Once
}

func NewServerInfoPoller(mgr *ServerManager) *ServerInfoPoller {
	return &ServerInfoPoller{mgr: mgr}
}

func (p *ServerInfoPoller) Start(ctx context.Context) error {
	udpConn, err := net.ListenUDP("udp4", relayListenAddr())
	if err != nil {
		return fmt.Errorf("server info poller: listen udp: %w", err)
	}

	p.pollConn = udpConn

	go p.readLoop(ctx, udpConn)
	go p.pollLoop(ctx, udpConn)

	return nil
}

func (p *ServerInfoPoller) Stop() {
	p.stopOnce.Do(func() {
		if p.pollConn != nil {
			_ = p.pollConn.Close()
		}
	})
}

func fillRunningPorts(mgr *ServerManager, dst []int) []int {
	dst = dst[:0]
	if mgr == nil {
		return dst
	}

	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	for port, recs := range mgr.serversByPort {
		if port < 1 || port > 65535 {
			continue
		}
		for _, rec := range recs {
			if rec == nil || rec.running == nil || rec.running.cmd == nil || rec.running.cmd.Process == nil {
				continue
			}
			if !isProcessAlive(rec.running.cmd.Process) {
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

func snapshotForSlist(mgr *ServerManager) []serverListEntry {
	if mgr == nil {
		return nil
	}

	ports := fillRunningPorts(mgr, nil)
	staleAfter := staleAfterForTargets(len(ports))
	now := time.Now()

	mgr.mu.RLock()
	out := make([]serverListEntry, 0, len(ports))
	for _, port := range ports {
		recs := mgr.serversByPort[port]
		var rec *serverRecord
		for _, r := range recs {
			if r == nil || r.running == nil || r.running.cmd == nil || r.running.cmd.Process == nil {
				continue
			}
			if !isProcessAlive(r.running.cmd.Process) {
				continue
			}
			rec = r
			break
		}
		if rec == nil {
			continue
		}
		if now.Sub(rec.lastSeen) > staleAfter {
			continue
		}

		gameDir := rec.launch.spec.ModName

		out = append(out, serverListEntry{
			ListenPort: port,
			Hostname:   rec.hostname,
			MapName:    rec.mapName,
			GameDir:    gameDir,
			Players:    rec.players,
			MaxPlayers: rec.maxPlayers,
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

func (p *ServerInfoPoller) pollLoop(ctx context.Context, conn *net.UDPConn) {
	req := buildCCREQServerInfo()

	var ports []int

	// Prime quickly at startup.
	ports = fillRunningPorts(p.mgr, ports)
	for _, port := range ports {
		dst := serverUDPAddr(port)
		if _, err := conn.WriteToUDP(req, dst); err != nil && errors.Is(err, net.ErrClosed) {
			return
		}
	}

	pollOne := func(port int) bool {
		dst := serverUDPAddr(port)
		if _, err := conn.WriteToUDP(req, dst); err != nil {
			return !errors.Is(err, net.ErrClosed)
		}
		return true
	}

	ticker := time.NewTicker(serverInfoPollStep)
	defer ticker.Stop()

	infof("Server info poller: polling %d targets every %s (round-robin step %s)",
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

func (p *ServerInfoPoller) readLoop(ctx context.Context, conn *net.UDPConn) {
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
			// On shutdown the socket will close and return an error.
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

		if p.mgr != nil {
			p.mgr.UpdateObservedServerInfo(srcPort, hostname, mapName, players, maxPlayers)
		}
	}
}
