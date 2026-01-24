package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/gorilla/websocket"
)

// UDPRelay handles bidirectional relay between WebSocket client and UDP server
type UDPRelay struct {
	client         *ClientConnection
	serverAddr     *net.UDPAddr
	udpConn        *net.UDPConn
	ctx            context.Context
	cancel         context.CancelFunc
	serverToClient chan []byte
	clientToServer chan []byte
	dstMu          sync.RWMutex
	dstAddr        *net.UDPAddr

	seqMu                sync.Mutex
	lastClientUnreliable uint32
	haveClientUnreliable bool
}

// NewUDPRelay creates a new UDP relay for a client connection
func NewUDPRelay(client *ClientConnection, serverAddr *net.UDPAddr, listenPort int) (*UDPRelay, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Create UDP connection with unique port for this client
	listenAddrStr := ":0"
	if listenPort > 0 {
		listenAddrStr = fmt.Sprintf(":%d", listenPort)
	}

	listenAddr, err := net.ResolveUDPAddr("udp4", listenAddrStr) // Port 0 = auto-assign
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to resolve UDP address: %w", err)
	}

	udpConn, err := net.ListenUDP("udp4", listenAddr)
	if err != nil {
		// If a preferred port was requested but unavailable, fall back to an ephemeral port.
		if listenPort > 0 {
			listenAddr, err2 := net.ResolveUDPAddr("udp4", ":0")
			if err2 == nil {
				udpConn, err2 = net.ListenUDP("udp4", listenAddr)
				if err2 == nil {
					err = nil
				}
			}
		}
	}
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create UDP connection: %w", err)
	}

	relay := &UDPRelay{
		client:         client,
		serverAddr:     serverAddr,
		udpConn:        udpConn,
		ctx:            ctx,
		cancel:         cancel,
		serverToClient: make(chan []byte, 1024),
		clientToServer: make(chan []byte, 1024),
		dstAddr: &net.UDPAddr{
			IP:   append([]byte(nil), serverAddr.IP...),
			Port: serverAddr.Port,
			Zone: serverAddr.Zone,
		},
	}

	return relay, nil
}

// Start begins the bidirectional relay
func (r *UDPRelay) Start() {
	go r.udpReader()
	go r.udpWriter()
	go r.wsWriter()
}

// Stop stops the relay and cleans up resources
func (r *UDPRelay) Stop() {
	r.sendClientDisconnectToServer()
	r.cancel()
	_ = r.udpConn.Close()
}

func (r *UDPRelay) noteClientUnreliableSeq(seq uint32) {
	r.seqMu.Lock()
	r.lastClientUnreliable = seq
	r.haveClientUnreliable = true
	r.seqMu.Unlock()
}

func (r *UDPRelay) sendClientDisconnectToServer() {
	r.seqMu.Lock()
	have := r.haveClientUnreliable
	last := r.lastClientUnreliable
	r.seqMu.Unlock()
	if !have {
		return
	}

	// Craft a minimal NetQuake unreliable packet whose payload is a single
	// `clc_disconnect` byte. This lets the server drop the client immediately
	// when the WebSocket closes (when possible), rather than waiting for
	// net_messagetimeout.
	const (
		netFlagUnreliable = 0x00100000
		netHeaderSize     = 8
		clcDisconnect     = 2
	)

	packetLen := netHeaderSize + 1
	packet := make([]byte, packetLen)
	binary.BigEndian.PutUint32(packet[0:4], uint32(packetLen)|netFlagUnreliable)
	binary.BigEndian.PutUint32(packet[4:8], last+1)
	packet[8] = clcDisconnect

	r.dstMu.RLock()
	dst := &net.UDPAddr{
		IP:   append([]byte(nil), r.dstAddr.IP...),
		Port: r.dstAddr.Port,
		Zone: r.dstAddr.Zone,
	}
	r.dstMu.RUnlock()

	if _, err := r.udpConn.WriteToUDP(packet, dst); err == nil {
		debugf("Sent clc_disconnect to server at %s", dst.String())
	}
}

// SendToServer queues a packet to be sent to the UDP server
func (r *UDPRelay) SendToServer(data []byte) {
	if r.ctx.Err() != nil {
		return
	}
	select {
	case <-r.ctx.Done():
		return
	case r.clientToServer <- data:
	default:
		warnf("clientToServer channel full, dropping packet")
	}
}

// udpReader reads packets from the UDP server and forwards to WebSocket
func (r *UDPRelay) udpReader() {
	defer r.cancel()

	debugRelay := os.Getenv("DEBUG_RELAY") == "1"
	debugSeen := 0

	r.dstMu.RLock()
	debugf("UDP reader started: CTRL -> %s:%d", r.serverAddr.IP, r.serverAddr.Port)
	r.dstMu.RUnlock()

	for {
		select {
		case <-r.ctx.Done():
			return
		default:
		}

		buffer := make([]byte, 65536)
		n, remoteSrcAddr, err := r.udpConn.ReadFrom(buffer)
		if err != nil {
			if r.ctx.Err() == nil {
				warnf("UDP read error: %v", err)
			}
			return
		}

		packet := buffer[:n]
		_ = remoteSrcAddr // Quake server might send from different port

		// Validate basic NetQuake packet framing: the first 4 bytes are a big-endian
		// length/flags word where the low 16 bits must match the datagram size.
		var wireHeader uint32
		var declaredLen int
		var flags uint32
		if len(packet) >= 4 {
			const lengthMask = 0x0000ffff
			wireHeader = binary.BigEndian.Uint32(packet[0:4])
			declaredLen = int(wireHeader & lengthMask)
			flags = wireHeader & ^uint32(lengthMask)
			if declaredLen != len(packet) {
				warnf("Dropping UDP packet with mismatched header length (declared %d, got %d)", declaredLen, len(packet))
				continue
			}
		}

		if debugRelay && debugSeen < 25 {
			cmd := byte(0)
			if flags == 0x80000000 && len(packet) >= 5 {
				cmd = packet[4]
			}
			debugf("DEBUG_RELAY\tudp<-server\tsrc=%s\tlen=%d\theader=0x%08x\tflags=0x%08x\tcmd=0x%02x\tbytes=% x",
				remoteSrcAddr.String(), len(packet), wireHeader, flags, cmd, packet[:min(len(packet), 24)])
			debugSeen++
		}

		// Check for control -> game port transition.
		// Control packets are framed as:
		//   u32  header_be = NETFLAG_CTL | length
		//   u8   cmd       (CCREP_ACCEPT=0x81, CCREP_REJECT=0x82, ...)
		//   ...  payload
		if flags == 0x80000000 && len(packet) >= 5 {
			cmd := packet[4]

			// Accept: cmd + i32 port (little-endian)
			if cmd == 0x81 && len(packet) >= 9 {
				newPort := int(binary.LittleEndian.Uint32(packet[5:9]))
				if newPort > 0 && newPort <= 65535 {
					r.dstMu.Lock()
					oldPort := r.dstAddr.Port
					r.dstAddr.Port = newPort
					r.dstMu.Unlock()
					debugf("Switched to GAME mode, port: %d -> %d", oldPort, newPort)
				} else {
					warnf("Ignoring CCREP_ACCEPT with invalid port %d", newPort)
				}
			} else if cmd == 0x82 {
				// Reject: cmd + NUL-terminated reason string
				reason := ""
				if len(packet) > 5 {
					rest := packet[5:]
					if i := bytes.IndexByte(rest, 0); i >= 0 {
						reason = string(rest[:i])
					} else {
						reason = string(rest)
					}
				}
				if reason != "" {
					warnf("Server rejected connect: %s", reason)
				} else {
					warnf("Server rejected connect")
				}
			}
		}

		// Forward to WebSocket client
		if r.ctx.Err() != nil {
			return
		}
		select {
		case <-r.ctx.Done():
			return
		case r.serverToClient <- packet:
		default:
			warnf("serverToClient channel full, dropping packet")
		}
	}
}

// udpWriter writes packets from the WebSocket to the UDP server
func (r *UDPRelay) udpWriter() {
	defer r.cancel()

	debugf("UDP writer started")
	debugRelay := os.Getenv("DEBUG_RELAY") == "1"
	debugSeen := 0

	for {
		select {
		case <-r.ctx.Done():
			return
		case packet := <-r.clientToServer:
			if r.ctx.Err() != nil {
				return
			}
			if len(packet) == 0 {
				continue
			}
			// Validate client packet framing (same as udpReader): header low-16 length
			// should match the datagram size. This prevents forwarding corrupted or
			// truncated packets that can destabilize the server/client.
			if len(packet) >= 4 {
				const lengthMask = 0x0000ffff
				wireHeader := binary.BigEndian.Uint32(packet[0:4])
				declaredLen := int(wireHeader & lengthMask)
				if declaredLen != len(packet) {
					warnf("Dropping client UDP packet with mismatched header length (declared %d, got %d)", declaredLen, len(packet))
					continue
				}
			}

			wireHeader := uint32(0)
			flags := uint32(0)
			cmd := byte(0)
			if len(packet) >= 5 {
				const lengthMask = 0x0000ffff
				wireHeader = binary.BigEndian.Uint32(packet[0:4])
				flags = wireHeader & ^uint32(lengthMask)
				if flags == 0x80000000 {
					cmd = packet[4]
				}
			}

			// Track the last client unreliable sequence so we can send a best-effort
			// clc_disconnect when the WebSocket closes.
			if flags == 0x00100000 && len(packet) >= 8 {
				seq := binary.BigEndian.Uint32(packet[4:8])
				r.noteClientUnreliableSeq(seq)
			}

			// If the client starts a new out-of-band NetQuake handshake (connect/info),
			// force the destination back to the server's listen port. This makes
			// reconnects stable even if the relay was previously in GAME mode.
			if flags == 0x80000000 && (cmd == 0x01 || cmd == 0x02) {
				r.dstMu.Lock()
				oldPort := r.dstAddr.Port
				r.dstAddr.Port = r.serverAddr.Port
				r.dstMu.Unlock()
				if debugRelay {
					debugf("DEBUG_RELAY\treset->CTRL\tport:%d->%d\tcmd=0x%02x", oldPort, r.serverAddr.Port, cmd)
				}
			}

			if debugRelay && debugSeen < 25 {
				r.dstMu.RLock()
				dstNow := r.dstAddr.String()
				r.dstMu.RUnlock()
				debugf("DEBUG_RELAY\tudp->server\tdst=%s\tlen=%d\theader=0x%08x\tflags=0x%08x\tcmd=0x%02x\tbytes=% x",
					dstNow, len(packet), wireHeader, flags, cmd, packet[:min(len(packet), 24)])
				debugSeen++
			}

			r.dstMu.RLock()
			dst := &net.UDPAddr{
				IP:   append([]byte(nil), r.dstAddr.IP...),
				Port: r.dstAddr.Port,
				Zone: r.dstAddr.Zone,
			}
			r.dstMu.RUnlock()

			_, err := r.udpConn.WriteToUDP(packet, dst)
			if err != nil {
				if r.ctx.Err() == nil {
					warnf("UDP write error: %v", err)
				}
				return
			}
		}
	}
}

// wsWriter writes packets from the UDP server to the WebSocket client
func (r *UDPRelay) wsWriter() {
	defer r.cancel()

	debugf("WebSocket writer started")

	for {
		select {
		case <-r.ctx.Done():
			return
		case packet := <-r.serverToClient:
			if r.ctx.Err() != nil {
				return
			}
			if len(packet) == 0 {
				continue
			}
			r.client.mu.Lock()
			err := r.client.conn.WriteMessage(websocket.BinaryMessage, packet)
			r.client.mu.Unlock()

			if err != nil {
				if r.ctx.Err() == nil {
					debugf("WebSocket write error: %v", err)
				}
				return
			}
		}
	}
}
