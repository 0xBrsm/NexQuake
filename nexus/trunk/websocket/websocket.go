// Package websocket adapts a [github.com/gorilla/websocket] *Conn to the
// [trunk.Transport] interface. Non-binary messages on the underlying conn
// are skipped silently. [Upgrader] is a default Upgrader configured for
// binary framing with compression disabled — callers can build their own
// upgrader and pass the resulting conn to [New].
package websocket

import (
	"net/http"
	"time"

	gws "github.com/gorilla/websocket"

	"github.com/0xBrsm/NexQuake/nexus/trunk"
)

const wsWriteTimeout = 5 * time.Second

// Upgrader is a default WebSocket upgrader wired for binary framing with
// compression disabled.
var Upgrader = gws.Upgrader{
	HandshakeTimeout:  10 * time.Second,
	ReadBufferSize:    4096,
	WriteBufferSize:   4096,
	Subprotocols:      []string{"binary"},
	CheckOrigin:       func(r *http.Request) bool { return true },
	EnableCompression: false,
}

// New wraps an already-upgraded *websocket.Conn as a [trunk.Transport].
func New(c *gws.Conn) trunk.Transport { return &adapter{conn: c} }

type adapter struct {
	conn *gws.Conn
}

func (a *adapter) Name() string { return "WebSocket" }

// ReadFrame blocks until a binary WebSocket message arrives, skipping any
// non-binary frames silently.
func (a *adapter) ReadFrame() ([]byte, error) {
	for {
		mt, data, err := a.conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		if mt != gws.BinaryMessage {
			continue
		}
		return data, nil
	}
}

func (a *adapter) WriteFrame(data []byte) error {
	_ = a.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	err := a.conn.WriteMessage(gws.BinaryMessage, data)
	_ = a.conn.SetWriteDeadline(time.Time{})
	return err
}

func (a *adapter) Ping() error {
	return a.conn.WriteControl(
		gws.PingMessage,
		nil,
		time.Now().Add(wsWriteTimeout),
	)
}

func (a *adapter) Close() error {
	return a.conn.Close()
}
