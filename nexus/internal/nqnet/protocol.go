// Package nqnet implements the network/routing layer for the NexQuake nexus.
//
// It provides the WebSocket-to-UDP relay (Router), the Quake wire-protocol
// codec (CCREQ/CCREP), virtual-IP allocation, and the client session registry.
package nqnet

import (
	"bytes"
	"encoding/binary"
	"strconv"
)

// Quake network constants (see quakedef.h / net.h).
const (
	netFlagLengthMask uint32 = 0x0000ffff
	netFlagCtl        uint32 = 0x80000000

	netProtocolVersion byte = 3

	ccreqServerInfo byte = 0x02
	ccrepServerInfo byte = 0x83
)

// Quake constants: MAX_DATAGRAM=1024 and NET_HEADERSIZE=8 => NET_DATAGRAMSIZE=1032.
const maxNetDatagramSize = 1024 + 8

// WSPortHeaderSize is the two-byte port prefix on every nexus WS frame.
const WSPortHeaderSize = 2

// wsClientIdentityMagic is the 4-byte magic in a client-identity announcement frame.
const wsClientIdentityMagic = "NQIP"

// ServerListEntry describes a single server for the aggregated slist response.
type ServerListEntry struct {
	ListenPort int
	Hostname   string
	MapName    string
	GameDir    string
	Players    byte
	MaxPlayers byte
}

// BuildCCREQServerInfo constructs a CCREQ_SERVER_INFO datagram.
func BuildCCREQServerInfo() []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, 0, 0, 0, 0) // placeholder header
	buf = append(buf, ccreqServerInfo)
	buf = appendCString(buf, "QUAKE")
	buf = append(buf, netProtocolVersion)

	control := netFlagCtl | uint32(len(buf))
	binary.BigEndian.PutUint32(buf[0:4], control)
	return buf
}

// parseCCREQServerInfo extracts the protocol version from a CCREQ_SERVER_INFO.
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

// ParseCCREPServerInfo extracts server info from a CCREP_SERVER_INFO response.
func ParseCCREPServerInfo(payload []byte) (hostname, mapName string, players, maxPlayers, protocol byte, ok bool) {
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

// truncateQuakeString clamps s to maxBytes for Quake UI field limits.
func truncateQuakeString(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes]
}

// BuildCCREPServerList builds an aggregated CCREP_SERVER_INFO response
// containing multiple server entries. Returns the datagram and entry count.
func BuildCCREPServerList(entries []ServerListEntry) ([]byte, int) {
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
		hostname = truncateQuakeString(hostname, 15)
		mapName = truncateQuakeString(mapName, 15)
		gameDir = truncateQuakeString(gameDir, 15)

		entrySize := len(serverPortText) + 1 + len(hostname) + 1 + len(mapName) + 1 + len(gameDir) + 1 + 3
		if len(buf)+entrySize > maxNetDatagramSize {
			break
		}

		buf = appendCString(buf, serverPortText)
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

// buildWSFrame builds a nexus WS frame: 2-byte port header + payload.
func buildWSFrame(port int, payload []byte) []byte {
	if port < 0 || port > 65535 {
		return nil
	}
	frame := make([]byte, WSPortHeaderSize+len(payload))
	frame[0] = byte((port >> 8) & 0xff)
	frame[1] = byte(port & 0xff)
	copy(frame[WSPortHeaderSize:], payload)
	return frame
}

// buildWSClientIdentityFrame builds the identity announcement frame sent to
// a newly connected client.
func buildWSClientIdentityFrame(clientIP [4]byte) []byte {
	if clientIP[0] == 0 {
		return nil
	}
	frame := make([]byte, WSPortHeaderSize+len(wsClientIdentityMagic)+len(clientIP))
	frame[0] = 0
	frame[1] = 0
	copy(frame[WSPortHeaderSize:], wsClientIdentityMagic)
	copy(frame[WSPortHeaderSize+len(wsClientIdentityMagic):], clientIP[:])
	return frame
}

// appendCString appends a NUL-terminated C string to buf.
func appendCString(buf []byte, s string) []byte {
	buf = append(buf, []byte(s)...)
	buf = append(buf, 0)
	return buf
}

// readCString reads a NUL-terminated string from buf starting at offset.
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
