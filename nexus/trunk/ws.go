package trunk

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	keepalivePingInterval = 25 * time.Second
	wsWriteTimeout        = 5 * time.Second
)

// Upgrader is a default WebSocket upgrader wired for binary framing with
// compression disabled. Exposed as a convenience — callers can build their
// own upgrader and still use [WebSocket] to wrap the resulting conn.
var Upgrader = websocket.Upgrader{
	HandshakeTimeout:  10 * time.Second,
	ReadBufferSize:    4096,
	WriteBufferSize:   4096,
	Subprotocols:      []string{"binary"},
	CheckOrigin:       func(r *http.Request) bool { return true },
	EnableCompression: false,
}

// WebSocket wraps an already-upgraded *websocket.Conn as a trunk [Transport].
// Non-binary messages received on the underlying conn are skipped silently.
func WebSocket(ws *websocket.Conn) Transport { return &wsTransport{conn: ws} }

// wsTransport adapts a *websocket.Conn to the Transport interface.
type wsTransport struct {
	conn *websocket.Conn
}

// ReadFrame blocks until a binary WebSocket message arrives, skipping any
// non-binary frames silently.
func (w *wsTransport) ReadFrame() ([]byte, error) {
	for {
		mt, data, err := w.conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		return data, nil
	}
}

func (w *wsTransport) WriteFrame(data []byte) error {
	_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	err := w.conn.WriteMessage(websocket.BinaryMessage, data)
	_ = w.conn.SetWriteDeadline(time.Time{})
	return err
}

func (w *wsTransport) Ping() error {
	return w.conn.WriteControl(
		websocket.PingMessage,
		nil,
		time.Now().Add(wsWriteTimeout),
	)
}

func (w *wsTransport) Close() error {
	return w.conn.Close()
}
