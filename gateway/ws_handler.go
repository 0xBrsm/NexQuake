package main

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	HandshakeTimeout:  10 * time.Second,
	ReadBufferSize:    4096,
	WriteBufferSize:   4096,
	Subprotocols:      []string{"binary"},
	CheckOrigin:       checkOrigin,
	EnableCompression: false,
}

var allowedOrigins []string

// initWebSocketUpgrader initializes the WebSocket upgrader with allowed origins
func initWebSocketUpgrader(origins string) {
	if origins == "*" {
		// Allow all origins (development mode)
		allowedOrigins = []string{"*"}
		debugf("WebSocket: allowing all origins (WS_ALLOWED_ORIGIN=*)")
	} else {
		// Parse comma-separated list of allowed origins
		allowedOrigins = strings.Split(origins, ",")
		for i := range allowedOrigins {
			allowedOrigins[i] = strings.TrimSpace(allowedOrigins[i])
		}
		infof("WebSocket: allowed origins: %v", allowedOrigins)
	}
}

// checkOrigin validates the WebSocket connection origin
func checkOrigin(r *http.Request) bool {
	// Allow all origins if configured with "*"
	if len(allowedOrigins) == 1 && allowedOrigins[0] == "*" {
		return true
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		// No origin header - might be a non-browser client
		debugf("WebSocket connection with no Origin header from %s", r.RemoteAddr)
		return true
	}

	// Check if origin is in the allowed list
	for _, allowed := range allowedOrigins {
		if origin == allowed {
			return true
		}
	}

	warnf("WebSocket connection rejected: origin %s not in allowed list", origin)
	return false
}

// ClientConnection represents a browser client WebSocket connection
type ClientConnection struct {
	conn     *websocket.Conn
	udpRelay *UDPRelay
	mu       sync.Mutex
	done     chan struct{}
}

// handleWebSocket upgrades HTTP to WebSocket and manages the connection
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
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

	debugf("Client connected: %s (subprotocol=%q)", conn.RemoteAddr(), conn.Subprotocol())

	// Create a UDP relay immediately. This gateway intentionally does not parse
	// NetQuake datagrams. It reads a small routing header from each WebSocket
	// frame and forwards the datagram to 127.255.255.<server_id>:<udp_port>.
	relay, err := NewUDPRelay(client)
	if err != nil {
		errorf("Failed to create UDP relay: %v", err)
		conn.Close()
		return
	}

	client.mu.Lock()
	client.udpRelay = relay
	client.mu.Unlock()

	relay.Start()
	debugf("UDP relay started for client")

	// Handle client messages (client -> server)
	go client.readLoop()

	// Wait for connection to close
	<-client.done
	debugf("Client disconnected: %s", conn.RemoteAddr())
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
