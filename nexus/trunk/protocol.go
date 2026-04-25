// Package trunk implements a per-client browser↔UDP tunnel: it bridges a
// binary-frame transport (WebSocket today, WebTransport in the future, or
// any user-provided [Transport]) to UDP datagrams aimed at a localhost
// backend. One [Conn] per connected client, each owning a UDP socket bound
// to a unique virtual loopback IP allocated by [VirtualIPAllocator].
//
// # Wire format
//
// Every tunnel message is a binary frame with a 2-byte big-endian port
// header followed by the payload:
//
//	byte 0    byte 1    byte 2 …
//	+---------+---------+----------+
//	| port (uint16, BE) | payload  |
//	+---------+---------+----------+
//
// Port 0 is the control channel; non-zero values are UDP destination ports
// on the backend. Control frames are handed to
// [FrameDispatch.HandleControlFrame] instead of being forwarded over UDP.
// On connect, the Conn sends an identity frame on the control channel
// containing the 4-byte magic "NQIP" followed by the 4-byte virtual IPv4
// address assigned to the client.
//
// # Usage
//
//	alloc := trunk.NewVirtualIPAllocator(net.ParseIP(trunk.DefaultServerIP))
//
//	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
//		ws, err := trunk.Upgrader.Upgrade(w, r, nil)
//		if err != nil {
//			return
//		}
//		conn, err := trunk.NewConn(trunk.WebSocket(ws), alloc, sourceKey,
//			trunk.WithDispatch(trunk.FrameDispatch{
//				IsAllowedPort: func(port int) bool { return port == 26000 },
//			}),
//		)
//		if err != nil {
//			_ = ws.Close()
//			return
//		}
//		conn.Run() // blocks until the connection closes
//	})
package trunk

import "encoding/binary"

// controlPort is the reserved tunnel port for control-channel frames.
const controlPort = 0

// defaultIdentityMagic is the 4-byte prefix on the client-identity
// announcement frame. Must match NQ_IDENTITY_MAGIC in
// src/client/net_nqchan.c — NexQuake's WASM client looks for this exact
// string when it parses the identity frame on port 0.
const defaultIdentityMagic = "NQIP"

// buildFrame builds a trunk tunnel frame: 2-byte port header + payload.
func buildFrame(port int, payload []byte) []byte {
	if port < controlPort || port > 65535 {
		return nil
	}
	frame := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(frame[:2], uint16(port))
	copy(frame[2:], payload)
	return frame
}

// buildIdentityFrame builds the identity announcement frame sent to a newly
// connected client: control-channel frame carrying defaultIdentityMagic ||
// clientIP.
func buildIdentityFrame(clientIP [4]byte) []byte {
	if clientIP[0] == 0 {
		return nil
	}
	magic := []byte(defaultIdentityMagic)
	payload := make([]byte, len(magic)+len(clientIP))
	copy(payload, magic)
	copy(payload[len(magic):], clientIP[:])
	return buildFrame(controlPort, payload)
}

// decodeFrame extracts the port header and payload from a tunnel frame.
// Frames shorter than the 2-byte header are rejected.
func decodeFrame(packet []byte) (dstPort int, payload []byte, ok bool) {
	if len(packet) < 2 {
		return 0, nil, false
	}
	dstPort = int(binary.BigEndian.Uint16(packet[:2]))
	return dstPort, packet[2:], true
}

func isValidServerPort(port int) bool {
	return port >= 1 && port <= 65535
}
