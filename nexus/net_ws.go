package main

import (
	"net/http"
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

// handleWebSocket upgrades HTTP to WebSocket and manages the connection
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Check admin status before upgrading (auth headers are available here)
	isAdmin := IsAdmin(r)
	sourceKey := resolveClientSourceKey(r)
	if isBlockedRelaySource(sourceKey) {
		warnf("Rejected blocked client source=%q remote=%s", sourceKey, r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Upgrade connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		warnf("WebSocket upgrade failed: %v", err)
		return
	}

	if isAdmin {
		infof("Admin connected: %s (subprotocol=%q source=%q)", conn.RemoteAddr(), conn.Subprotocol(), sourceKey)
	} else {
		debugf("Client connected: %s (subprotocol=%q source=%q)", conn.RemoteAddr(), conn.Subprotocol(), sourceKey)
	}

	router, err := NewRouter(conn, sourceKey, isAdmin)
	if err != nil {
		errorf("Failed to create router: %v", err)
		_ = conn.Close()
		return
	}

	router.Run()

	if isAdmin {
		infof("Admin disconnected: %s", conn.RemoteAddr())
	} else {
		debugf("Client disconnected: %s", conn.RemoteAddr())
	}
}

func (r *Router) wsReadLoop() {
	for {
		messageType, data, err := r.ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				debugf("WebSocket read error: %v", err)
			}
			r.Close()
			return
		}

		if messageType != websocket.BinaryMessage {
			debugf("Unexpected message type: %d", messageType)
			continue
		}

		r.handleWSFrame(data)
	}
}

func (r *Router) wsWriteLoop() {
	for {
		select {
		case <-r.ctx.Done():
			return
		case packet := <-r.wsTx:
			if len(packet) == 0 || r.ctx.Err() != nil {
				continue
			}
			if err := r.ws.WriteMessage(websocket.BinaryMessage, packet); err != nil {
				if r.ctx.Err() == nil {
					debugf("WebSocket write error: %v", err)
				}
				r.Close()
				return
			}
		}
	}
}
