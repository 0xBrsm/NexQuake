package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// globalServerInfoCache is used by the WS->UDP relay to answer Quake's `slist`
// broadcast from a stable polled snapshot (rather than relying on UDP broadcast
// delivery semantics on loopback).
var globalServerInfoCache *ServerInfoCache

const serverInfoPollStep = 500 * time.Millisecond

type serverInfoEntry struct {
	hostname   string
	mapName    string
	players    byte
	maxPlayers byte
	lastSeen   time.Time
}

type ServerInfoCache struct {
	mu          sync.RWMutex
	maxServerID int
	entries     []serverInfoEntry // indexed by server id (0 unused)

	pollConn *net.UDPConn
	stopOnce sync.Once
}

func NewServerInfoCache(maxServerID int) *ServerInfoCache {
	if maxServerID < 1 {
		maxServerID = 1
	}
	if maxServerID > 254 {
		maxServerID = 254
	}
	return &ServerInfoCache{
		maxServerID: maxServerID,
		entries:     make([]serverInfoEntry, maxServerID+1),
	}
}

func (c *ServerInfoCache) Start(ctx context.Context) error {
	if c.maxServerID < 1 {
		return fmt.Errorf("invalid max server id")
	}

	// Use a single UDP socket for polling. Bind it to a stable "nexus" loopback
	// address so it can't collide with per-client loopback allocations.
	listenAddr := &net.UDPAddr{
		IP:   net.IPv4(subnetNexusA, subnetNexusB, subnetNexusC, nexusPollerHostOct).To4(),
		Port: 0,
	}

	udpConn, err := net.ListenUDP("udp4", listenAddr)
	if err != nil {
		// Fall back to any available address.
		udpConn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
		if err != nil {
			return fmt.Errorf("server info poller: listen udp: %w", err)
		}
	}

	c.pollConn = udpConn

	go c.readLoop(ctx, udpConn)
	go c.pollLoop(ctx, udpConn)

	infof("Server info cache: polling 127.%d.%d.1..%d:%d every %s (round-robin step %s)",
		subnetServersB, subnetServersC, c.maxServerID, defaultServerPort,
		time.Duration(c.maxServerID)*serverInfoPollStep, serverInfoPollStep,
	)

	return nil
}

func (c *ServerInfoCache) Stop() {
	c.stopOnce.Do(func() {
		if c.pollConn != nil {
			_ = c.pollConn.Close()
		}
	})
}

func (c *ServerInfoCache) pollLoop(ctx context.Context, conn *net.UDPConn) {
	req := buildCCREQServerInfo()

	// Prime the cache quickly at startup (bounded by maxServerID).
	for id := 1; id <= c.maxServerID; id++ {
		dst := &net.UDPAddr{
			IP:   net.IPv4(subnetServersA, subnetServersB, subnetServersC, byte(id)).To4(),
			Port: defaultServerPort,
		}
		if _, err := conn.WriteToUDP(req, dst); err != nil && errors.Is(err, net.ErrClosed) {
			return
		}
	}

	nextID := 1
	pollOne := func(serverID int) bool {
		dst := &net.UDPAddr{
			IP:   net.IPv4(subnetServersA, subnetServersB, subnetServersC, byte(serverID)).To4(),
			Port: defaultServerPort,
		}
		if _, err := conn.WriteToUDP(req, dst); err != nil {
			return !errors.Is(err, net.ErrClosed)
		}
		return true
	}

	ticker := time.NewTicker(serverInfoPollStep)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if nextID > c.maxServerID {
				nextID = 1
			}
			if !pollOne(nextID) {
				return
			}
			nextID++
		}
	}
}

func (c *ServerInfoCache) readLoop(ctx context.Context, conn *net.UDPConn) {
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

		if src == nil || src.IP == nil {
			continue
		}
		ip4 := src.IP.To4()
		if ip4 == nil || ip4[0] != subnetServersA || ip4[1] != subnetServersB || ip4[2] != subnetServersC {
			continue
		}
		serverID := int(ip4[3])
		if serverID < 1 || serverID > c.maxServerID {
			continue
		}

		hostname, mapName, players, maxPlayers, _, ok := parseCCREPServerInfo(buf[:n])
		if !ok {
			continue
		}

		c.mu.Lock()
		c.entries[serverID] = serverInfoEntry{
			hostname:   hostname,
			mapName:    mapName,
			players:    players,
			maxPlayers: maxPlayers,
			lastSeen:   time.Now(),
		}
		c.mu.Unlock()
	}
}

func (c *ServerInfoCache) SnapshotForSlist() []struct {
	ServerID   byte
	Hostname   string
	MapName    string
	Players    byte
	MaxPlayers byte
} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	pollPeriod := time.Duration(c.maxServerID) * serverInfoPollStep
	staleAfter := pollPeriod * 10
	if staleAfter < 30*time.Second {
		staleAfter = 30 * time.Second
	}

	var out []struct {
		ServerID   byte
		Hostname   string
		MapName    string
		Players    byte
		MaxPlayers byte
	}
	for id := 1; id <= c.maxServerID; id++ {
		e := c.entries[id]
		if e.lastSeen.IsZero() {
			continue
		}
		if now.Sub(e.lastSeen) > staleAfter {
			continue
		}
		out = append(out, struct {
			ServerID   byte
			Hostname   string
			MapName    string
			Players    byte
			MaxPlayers byte
		}{
			ServerID:   byte(id),
			Hostname:   e.hostname,
			MapName:    e.mapName,
			Players:    e.players,
			MaxPlayers: e.maxPlayers,
		})
	}
	return out
}
