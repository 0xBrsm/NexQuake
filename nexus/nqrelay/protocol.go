// Package nqrelay implements a WebSocket-to-UDP relay for NQ (NetQuake)
// dedicated game servers.
//
// Each connected WebSocket client gets one [Relay]. The relay binds a UDP
// socket to a unique loopback virtual IP (allocated by [IPAllocator]) and
// forwards binary frames in both directions between the browser and the server.
//
// # Wire format
//
// Every WebSocket message is a binary frame with a two-byte big-endian port
// header followed by the payload:
//
//	byte 0    byte 1    byte 2 …
//	+---------+---------+----------+
//	| port (uint16, BE) | payload  |
//	+---------+---------+----------+
//
// Port 0 is the control channel; non-zero values are UDP game-server ports.
// Control frames are handed to [FrameDispatch.HandleControlFrame] instead of
// being forwarded over UDP. On connect, the relay sends an identity frame on
// the control channel containing the magic "NQIP" followed by the 4-byte
// virtual IPv4 address assigned to the client.
//
// # Usage
//
//	alloc := nqrelay.NewIPAllocator(net.ParseIP(nqrelay.DefaultNQServerIP))
//	sessions := nqrelay.NewSessionRegistry()
//
//	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
//		ws, err := nqrelay.Upgrader.Upgrade(w, r, nil)
//		if err != nil {
//			return
//		}
//		relay, err := nqrelay.NewRelay(ws, sourceKey, sourceIP, userID, false,
//			alloc, sessions, nqrelay.FrameDispatch{
//				IsAllowedPort: func(port int) bool { return port == 26000 },
//			}, nil, nil)
//		if err != nil {
//			_ = ws.Close()
//			return
//		}
//		relay.Run() // blocks until the connection closes
//	})
package nqrelay

import "encoding/binary"

// controlPort is the reserved WS tunnel port for control-channel frames.
const controlPort = 0

// wsClientIdentityMagic is the 4-byte magic in a client-identity announcement frame.
const wsClientIdentityMagic = "NQIP"

// buildWSFrame builds a Nexus WS frame: 2-byte port header + payload.
func buildWSFrame(port int, payload []byte) []byte {
	if port < controlPort || port > 65535 {
		return nil
	}
	frame := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(frame[:2], uint16(port))
	copy(frame[2:], payload)
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
	return buildWSFrame(controlPort, payload)
}

func decodeWSFrame(packet []byte) (dstPort int, payload []byte, ok bool) {
	if len(packet) < 2 {
		return 0, nil, false
	}
	dstPort = int(binary.BigEndian.Uint16(packet[:2]))
	return dstPort, packet[2:], true
}

func isValidServerPort(port int) bool {
	return port >= 1 && port <= 65535
}
