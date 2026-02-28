// Package nqrelay implements the network/routing layer for NexQuake Nexus.
//
// It provides the WebSocket-to-UDP relay (Relay), the Quake wire-protocol
// codec (CCREQ/CCREP), virtual-IP allocation, and the client session registry.
package nqrelay

import (
	"bytes"
	"encoding/binary"
)

// Quake network constants (see quakedef.h / net.h).
const (
	netFlagLengthMask uint32 = 0x0000ffff
	netFlagCtl        uint32 = 0x80000000

	netProtocolVersion byte = 3

	ccreqServerInfo byte = 0x02
	ccrepServerInfo byte = 0x83
)

const (
	// ControlPort is the reserved WS tunnel port for control-channel frames.
	ControlPort = 0

	// MinServerPort/MaxServerPort bound valid UDP server ports.
	MinServerPort = 1
	MaxServerPort = 65535

	// WSPortHeaderSize is the two-byte port prefix on every Nexus WS frame.
	WSPortHeaderSize = 2
)

// wsClientIdentityMagic is the 4-byte magic in a client-identity announcement frame.
const wsClientIdentityMagic = "NQIP"

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

// ParseCCREPServerInfo extracts server info from a CCREP_SERVER_INFO response.
func ParseCCREPServerInfo(payload []byte) (hostname, mapName string, players, maxPlayers, protocol byte, ok bool) {
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

// buildWSFrame builds a Nexus WS frame: 2-byte port header + payload.
func buildWSFrame(port int, payload []byte) []byte {
	if port < ControlPort || port > MaxServerPort {
		return nil
	}
	frame := make([]byte, WSPortHeaderSize+len(payload))
	binary.BigEndian.PutUint16(frame[:WSPortHeaderSize], uint16(port))
	copy(frame[WSPortHeaderSize:], payload)
	return frame
}

// buildWSClientIdentityFrame builds the identity announcement frame sent to
// a newly connected client.
func buildWSClientIdentityFrame(clientIP [4]byte) []byte {
	if clientIP[0] == 0 {
		return nil
	}
	payload := make([]byte, len(wsClientIdentityMagic)+len(clientIP))
	copy(payload, wsClientIdentityMagic)
	copy(payload[len(wsClientIdentityMagic):], clientIP[:])
	return buildWSFrame(ControlPort, payload)
}

func decodeWSFrame(packet []byte) (dstPort int, payload []byte, ok bool) {
	if len(packet) < WSPortHeaderSize {
		return 0, nil, false
	}
	dstPort = int(binary.BigEndian.Uint16(packet[:WSPortHeaderSize]))
	return dstPort, packet[WSPortHeaderSize:], true
}

func isValidServerPort(port int) bool {
	return port >= MinServerPort && port <= MaxServerPort
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
