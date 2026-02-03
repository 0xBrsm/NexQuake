package main

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	HandshakeTimeout:  10 * time.Second,
	ReadBufferSize:    4096,
	WriteBufferSize:   4096,
	Subprotocols:      []string{"binary"},
	CheckOrigin:       func(r *http.Request) bool { return true },
	EnableCompression: false,
}

// ClientConnection represents a browser WebSocket connection.
type ClientConnection struct {
	conn     *websocket.Conn
	udpRelay *UDPRelay
	mu       sync.Mutex
	done     chan struct{}
}

// handleWebSocket upgrades HTTP to WebSocket and manages the connection
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Check admin status before upgrading (auth headers are available here)
	isAdmin := IsAdmin(r)

	// Upgrade connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		warnf("WebSocket upgrade failed: %v", err)
		return
	}

	client := &ClientConnection{
		conn: conn,
		done: make(chan struct{}),
	}

	if isAdmin {
		infof("Admin connected: %s (subprotocol=%q)", conn.RemoteAddr(), conn.Subprotocol())
	} else {
		debugf("Client connected: %s (subprotocol=%q)", conn.RemoteAddr(), conn.Subprotocol())
	}

	// Create a UDP relay immediately. Nexus intentionally does not parse
	// NetQuake datagrams. It reads a small routing header from each WebSocket
	// frame and forwards the datagram to 127.255.255.<server_id>:<udp_port>.
	// Admin connections use 127.13.37.x subnet for server-side privilege.
	relay, err := NewUDPRelay(client, isAdmin)
	if err != nil {
		errorf("Failed to create UDP relay: %v", err)
		conn.Close()
		return
	}

	client.mu.Lock()
	client.udpRelay = relay
	client.mu.Unlock()

	relay.Start()
	if isAdmin {
		infof("UDP relay started for admin (subnet 127.13.37.x)")
	} else {
		debugf("UDP relay started for client")
	}

	// Handle client messages (client -> server)
	go client.readLoop()

	// Wait for connection to close
	<-client.done
	if isAdmin {
		infof("Admin disconnected: %s", conn.RemoteAddr())
	} else {
		debugf("Client disconnected: %s", conn.RemoteAddr())
	}
}

// readLoop reads messages from the WebSocket client
func (c *ClientConnection) readLoop() {
	defer func() {
		// Clean up UDP relay if active
		c.mu.Lock()
		if c.udpRelay != nil {
			c.udpRelay.Stop()
			c.udpRelay = nil
		}
		c.mu.Unlock()

		c.conn.Close()
		close(c.done)
	}()

	for {
		messageType, data, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				debugf("WebSocket read error: %v", err)
			}
			return
		}

		if messageType != websocket.BinaryMessage {
			debugf("Unexpected message type: %d", messageType)
			continue
		}

		c.handleGamePacket(data)
	}
}

// handleGamePacket forwards game packets to the selected NetQuake server via UDP relay
func (c *ClientConnection) handleGamePacket(data []byte) {
	c.mu.Lock()
	relay := c.udpRelay
	if relay == nil {
		c.mu.Unlock()
		debugf("Game packet received but no UDP relay active (dropping)")
		return
	}
	c.mu.Unlock()

	// Forward to UDP relay
	// Copy: websocket.ReadMessage buffers may be reused after the next read.
	relay.SendToServer(append([]byte(nil), data...))
}
