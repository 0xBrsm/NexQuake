package nqrelay

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsKeepalivePingInterval = 25 * time.Second
	wsKeepaliveWriteTimeout = 5 * time.Second
)

// Upgrader is the default WebSocket upgrader. It negotiates the "binary"
// subprotocol and disables per-message compression. CheckOrigin is set to
// allow all origins — callers should override it for production deployments.
var Upgrader = websocket.Upgrader{
	HandshakeTimeout:  10 * time.Second,
	ReadBufferSize:    4096,
	WriteBufferSize:   4096,
	Subprotocols:      []string{"binary"},
	CheckOrigin:       func(r *http.Request) bool { return true },
	EnableCompression: false,
}

// wsReadLoop reads WebSocket messages and dispatches them.
func (r *Relay) wsReadLoop() {
	for {
		messageType, data, err := r.ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				r.debugf("WebSocket read error: %v", err)
			}
			r.Close()
			return
		}

		if messageType != websocket.BinaryMessage {
			r.debugf("Unexpected message type: %d", messageType)
			continue
		}

		r.handleWSFrame(data)
	}
}

// wsWriteLoop writes queued frames to the WebSocket.
func (r *Relay) wsWriteLoop() {
	pingTicker := time.NewTicker(wsKeepalivePingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-pingTicker.C:
			if err := r.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsKeepaliveWriteTimeout)); err != nil {
				if r.ctx.Err() == nil {
					r.debugf("WebSocket ping error: %v", err)
				}
				r.Close()
				return
			}
		case packet := <-r.wsTx:
			if len(packet) == 0 || r.ctx.Err() != nil {
				continue
			}
			if err := r.ws.WriteMessage(websocket.BinaryMessage, packet); err != nil {
				if r.ctx.Err() == nil {
					r.debugf("WebSocket write error: %v", err)
				}
				r.Close()
				return
			}
		}
	}
}
