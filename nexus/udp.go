package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"

	"github.com/gorilla/websocket"
)

type clientIPAllocator struct {
	mu   sync.Mutex
	next byte
	used [256]bool
}

func newClientIPAllocator() *clientIPAllocator {
	a := &clientIPAllocator{next: firstClientHostOct}
	// Reserve 0 (network).
	a.used[0] = true
	return a
}

func (a *clientIPAllocator) alloc() (byte, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := 0; i < int(lastClientHostOct-firstClientHostOct+1); i++ {
		if a.next < firstClientHostOct || a.next > lastClientHostOct {
			a.next = firstClientHostOct
		}
		oct := a.next
		a.next++
		if !a.used[oct] {
			a.used[oct] = true
			return oct, true
		}
	}
	return 0, false
}

func (a *clientIPAllocator) release(oct byte) {
	if oct == 0 {
		return
	}
	a.mu.Lock()
	a.used[oct] = false
	a.mu.Unlock()
}

var globalClientIPs = newClientIPAllocator()

// UDPRelay handles bidirectional relay between WebSocket client and UDP server
type UDPRelay struct {
	client         *ClientConnection
	udpConn        *net.UDPConn
	ctx            context.Context
	cancel         context.CancelFunc
	serverToClient chan []byte
	clientToServer chan []byte
	clientIPOctet  byte
}

// NewUDPRelay creates a new UDP relay for a client connection
func NewUDPRelay(client *ClientConnection) (*UDPRelay, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Create UDP connection with unique source IP for this client.
	oct, ok := globalClientIPs.alloc()
	if !ok {
		cancel()
		return nil, fmt.Errorf("failed to allocate unique client loopback IP")
	}
	listenAddr := &net.UDPAddr{
		IP:   net.IPv4(subnetClientsA, subnetClientsB, subnetClientsC, oct).To4(),
		Port: 0,
	}

	udpConn, err := net.ListenUDP("udp4", listenAddr)
	if err != nil {
		globalClientIPs.release(oct)
		cancel()
		return nil, fmt.Errorf("failed to create UDP connection: %w", err)
	}

	relay := &UDPRelay{
		client:         client,
		udpConn:        udpConn,
		ctx:            ctx,
		cancel:         cancel,
		serverToClient: make(chan []byte, 1024),
		clientToServer: make(chan []byte, 1024),
		clientIPOctet:  oct,
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
	r.cancel()
	_ = r.udpConn.Close()
	if r.clientIPOctet != 0 {
		globalClientIPs.release(r.clientIPOctet)
		r.clientIPOctet = 0
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

	debugf("UDP reader started")

	buffer := make([]byte, 65536)
	for {
		select {
		case <-r.ctx.Done():
			return
		default:
		}

		n, remoteSrcAddr, err := r.udpConn.ReadFrom(buffer)
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			// Linux may surface ICMP port-unreachable as ECONNREFUSED on UDP
			// sockets after writes (especially when clients probe addresses
			// with no server bound). Ignore and keep the relay alive.
			if errors.Is(err, syscall.ECONNREFUSED) {
				continue
			}
			warnf("UDP read error: %v", err)
			return
		}

		packet := buffer[:n]

		// Extract server ID from source IP (127.255.255.x).
		serverID := byte(0)
		srcPort := 0
		if udpAddr, ok := remoteSrcAddr.(*net.UDPAddr); ok && udpAddr.IP != nil {
			if ip4 := udpAddr.IP.To4(); ip4 != nil && ip4[0] == subnetServersA && ip4[1] == subnetServersB && ip4[2] == subnetServersC {
				serverID = ip4[3]
			}
			srcPort = udpAddr.Port
		}
		if serverID == 0 {
			continue
		}
		if srcPort <= 0 || srcPort > 65535 {
			continue
		}

		if debugRelay && debugSeen < 25 {
			debugf("DEBUG_RELAY\tudp<-server\tsrc=%s\tserver_id=%d\tport=%d\tlen=%d\tbytes=% x",
				remoteSrcAddr.String(), serverID, srcPort, len(packet), packet[:min(len(packet), 24)])
			debugSeen++
		}

		// Prepend routing header for client-side routing.
		frameWithRouting := make([]byte, len(packet)+wsRoutingHeaderSize)
		frameWithRouting[0] = serverID
		frameWithRouting[1] = byte((srcPort >> 8) & 0xff)
		frameWithRouting[2] = byte(srcPort & 0xff)
		copy(frameWithRouting[wsRoutingHeaderSize:], packet)

		// Forward to WebSocket client
		if r.ctx.Err() != nil {
			return
		}
		select {
		case <-r.ctx.Done():
			return
		case r.serverToClient <- frameWithRouting:
		default:
			warnf("serverToClient channel full, dropping packet")
		}
	}
}

// udpWriter writes packets from the WebSocket to the UDP server(s)
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
			if len(packet) < wsRoutingHeaderSize {
				continue
			}

			routingByte := packet[0]
			dstPort := int(packet[1])<<8 | int(packet[2])
			payload := packet[wsRoutingHeaderSize:]
			if routingByte == 0 {
				continue
			}
			if dstPort == 0 {
				dstPort = defaultServerPort
			}
			if dstPort <= 0 || dstPort > 65535 {
				continue
			}

			// Control-plane handling for Quake's LAN server discovery.
			// `slist` broadcasts a connectionless CCREQ_SERVER_INFO. Reply
			// immediately from the nexus polled cache, as a single aggregated
			// packet to avoid timing/polling flakiness in the client.
			if routingByte == 0xFF {
				if globalServerInfoCache != nil && isCCREQServerInfo(payload) {
					listPayload, _ := buildCCREPServerList(globalServerInfoCache.SnapshotForSlist())
					frame := make([]byte, wsRoutingHeaderSize+len(listPayload))
					// Any non-local address works here; Quake only uses this source
					// to filter its own queries. Use serverID=1, port=26000.
					frame[0] = 1
					frame[1] = byte((defaultServerPort >> 8) & 0xff)
					frame[2] = byte(defaultServerPort & 0xff)
					copy(frame[wsRoutingHeaderSize:], listPayload)

					select {
					case <-r.ctx.Done():
						return
					case r.serverToClient <- frame:
					}
				}
				continue
			}

			writeTo := func(serverID byte) {
				dst := &net.UDPAddr{
					IP:   net.IPv4(subnetServersA, subnetServersB, subnetServersC, serverID).To4(),
					Port: dstPort,
				}

				if debugRelay && debugSeen < 25 {
					debugf("DEBUG_RELAY\tudp->server\tdst=%s\tserver_id=%d\tlen=%d\tbytes=% x",
						dst.String(), serverID, len(payload), payload[:min(len(payload), 24)])
					debugSeen++
				}

				_, err := r.udpConn.WriteToUDP(payload, dst)
				if err != nil && errors.Is(err, syscall.ECONNREFUSED) {
					// Linux may report ICMP port-unreachable from a previous
					// send as ECONNREFUSED on the next socket op.
					// Retry once to clear the error and still deliver the
					// broadcast to real servers.
					_, err = r.udpConn.WriteToUDP(payload, dst)
				}
				if err != nil && r.ctx.Err() == nil {
					warnf("UDP write error: %v", err)
				}
			}

			writeTo(routingByte)
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
