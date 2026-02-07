package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	defaultServerPort = 26000

	// Reserved infra subnet:
	// - nexus uses .0
	// - dedicated servers use .1..N
	// - admin relays are allocated from .255 downward
	subnetAdminsA = 127
	subnetAdminsB = 13
	subnetAdminsC = 37

	// Dedicated servers share the same infra subnet.
	subnetServersA = 127
	subnetServersB = 13
	subnetServersC = 37

	// Nexus/orchestration entities live in the same infra subnet.
	subnetNexusA       = 127
	subnetNexusB       = 13
	subnetNexusC       = 37
	nexusPollerHostOct = 0

	wsRoutingHeaderSize  = 3
	wsRoutingBroadcastID = 0xFF
)

const (
	netFlagLengthMask uint32 = 0x0000ffff
	netFlagCtl        uint32 = 0x80000000

	netProtocolVersion byte = 3

	ccreqServerInfo byte = 0x02
	ccrepServerInfo byte = 0x83
)

// Quake constants (see quakedef.h/net.h):
// MAX_DATAGRAM=1024 and NET_HEADERSIZE=8 => NET_DATAGRAMSIZE=1032.
// NexQuake's WS transport enforces this limit for a single tunneled "UDP datagram".
const maxNetDatagramSize = 1024 + 8

func buildCCREQServerInfo() []byte {
	// Matches WinQuake net_dgrm.c:
	//   long header (NETFLAG_CTL | length)
	//   byte CCREQ_SERVER_INFO
	//   string "QUAKE"
	//   byte NET_PROTOCOL_VERSION
	buf := make([]byte, 0, 64)
	buf = append(buf, 0, 0, 0, 0) // placeholder for the header
	buf = append(buf, ccreqServerInfo)
	buf = appendCString(buf, "QUAKE")
	buf = append(buf, netProtocolVersion)

	control := netFlagCtl | uint32(len(buf))
	binary.BigEndian.PutUint32(buf[0:4], control)
	return buf
}

func truncateQuakeString(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if s == "" {
		return s
	}
	// Quake UI fields are short and treated as byte strings; clamp by bytes.
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes]
}

func buildCCREPServerList(entries []struct {
	ServerID   byte
	Hostname   string
	MapName    string
	GameDir    string
	Players    byte
	MaxPlayers byte
}) ([]byte, int) {
	// Format:
	//   u32 control header (NETFLAG_CTL | length)
	//   u8  CCREP_SERVER_INFO (0x83) - overloaded for nexus aggregated list mode
	//   u8  count
	//   repeated entries:
	//     cstring server_address (e.g. "127.13.37.1:26000")
	//     cstring host_name (<= 15 chars recommended)
	//     cstring level_name (<= 15 chars recommended)
	//     cstring game_dir (<= 15 chars recommended)
	//     u8 players, u8 maxPlayers, u8 protocol_version
	buf := make([]byte, 0, 512)
	buf = append(buf, 0, 0, 0, 0) // placeholder header
	buf = append(buf, ccrepServerInfo)
	countIndex := len(buf)
	buf = append(buf, 0) // count placeholder

	count := 0
	for _, e := range entries {
		// The in-engine hostcache is only 8 entries; sending more is wasted and risks oversize.
		if count >= 8 {
			break
		}

		if e.ServerID == 0 || e.ServerID == 0xFF {
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

		serverAddr := fmt.Sprintf("%d.%d.%d.%d:%d", subnetServersA, subnetServersB, subnetServersC, e.ServerID, defaultServerPort)

		hostname = truncateQuakeString(hostname, 15)
		mapName = truncateQuakeString(mapName, 15)
		gameDir = truncateQuakeString(gameDir, 15)

		// Compute the entry size before appending to stay within Quake's datagram limit.
		entrySize := len(serverAddr) + 1 + len(hostname) + 1 + len(mapName) + 1 + len(gameDir) + 1 + 3
		if len(buf)+entrySize > maxNetDatagramSize {
			break
		}

		buf = appendCString(buf, serverAddr)
		buf = appendCString(buf, hostname)
		buf = appendCString(buf, mapName)
		buf = appendCString(buf, gameDir)
		buf = append(buf, e.Players, e.MaxPlayers, netProtocolVersion)
		count++
	}

	buf[countIndex] = byte(count)

	control := netFlagCtl | uint32(len(buf))
	binary.BigEndian.PutUint32(buf[0:4], control)
	return buf, count
}

func isCCREQServerInfo(payload []byte) bool {
	_, ok := parseCCREQServerInfo(payload)
	return ok
}

func parseCCREQServerInfo(payload []byte) (protocol byte, ok bool) {
	if len(payload) < 4+1 {
		return 0, false
	}
	control := binary.BigEndian.Uint32(payload[0:4])
	if (control &^ netFlagLengthMask) != netFlagCtl {
		return 0, false
	}
	if int(control&netFlagLengthMask) != len(payload) {
		return 0, false
	}
	if payload[4] != ccreqServerInfo {
		return 0, false
	}
	i := 5
	game, next, ok := readCString(payload, i)
	if !ok || game != "QUAKE" {
		return 0, false
	}
	i = next
	if i >= len(payload) {
		return 0, false
	}
	protocol = payload[i]
	return protocol, protocol == netProtocolVersion
}

func parseCCREPServerInfo(payload []byte) (hostname, mapName string, players, maxPlayers, protocol byte, ok bool) {
	if len(payload) < 4+1 {
		return "", "", 0, 0, 0, false
	}
	control := binary.BigEndian.Uint32(payload[0:4])
	if (control &^ netFlagLengthMask) != netFlagCtl {
		return "", "", 0, 0, 0, false
	}
	if int(control&netFlagLengthMask) != len(payload) {
		return "", "", 0, 0, 0, false
	}
	if payload[4] != ccrepServerInfo {
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
