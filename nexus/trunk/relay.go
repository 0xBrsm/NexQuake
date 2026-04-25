package trunk

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

func noopLogf(string, ...any) {}

// Transport is a bidirectional binary-frame tunnel between nexus and a single
// browser client. Adapters wrap the concrete connection type ([WebSocket]
// today, [WebTransport] in the future, or a custom implementation); the
// [Conn]'s read/write loops are transport-agnostic.
type Transport interface {
	// ReadFrame blocks until a binary frame is received. On connection close
	// or error it returns a non-nil error; the Conn tears down. Non-binary
	// messages (e.g. WS control frames) are skipped internally.
	ReadFrame() ([]byte, error)
	// WriteFrame sends a binary frame with a short write deadline.
	WriteFrame([]byte) error
	// Ping sends an application-level keepalive. Transports with native
	// connection-level keepalive (e.g. QUIC) may return nil.
	Ping() error
	// Close tears down the underlying connection.
	Close() error
}

// FrameDispatch holds application-defined callbacks that [Conn] invokes for
// frames it cannot handle internally. All fields are optional (nil is safe).
type FrameDispatch struct {
	// HandleControlFrame is called for every incoming control-channel frame
	// (port 0). Return a non-nil slice to send a reply on the control channel;
	// return nil to send nothing. The payload slice is only valid for the
	// duration of the call — copy it if you need to retain it.
	HandleControlFrame func(conn *Conn, payload []byte) []byte

	// HandleClose is called once when the connection is closing, before the
	// tunnel and UDP sockets are torn down.
	HandleClose func(conn *Conn)

	// IsAllowedPort, if non-nil, is called for every incoming non-control
	// frame before the payload is forwarded over UDP. Return true to allow,
	// false to drop. When nil, all destination ports are allowed.
	IsAllowedPort func(port int) bool
}

// Conn is a single client's tunnel↔UDP bridge. It owns one [Transport] and
// one UDP socket bound to a unique virtual loopback IP. Three goroutines run
// concurrently while [Conn.Run] is active: readLoop, writeLoop, and
// udpReadLoop; all exit when the connection closes.
type Conn struct {
	xport   Transport
	udpConn *net.UDPConn
	tx      chan []byte // outbound tunnel frames; bounded to 1024 entries

	clientIP  [4]byte
	sourceKey string

	alloc    *VirtualIPAllocator
	dispatch FrameDispatch

	warnf      logf
	debugf     logf
	debugRelay bool // mirrors DEBUG_RELAY=1 env, read once at construction

	routeMu        sync.Mutex
	lastServerPort int

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
}

// NewConn builds a tunnel↔UDP Conn for a single client.
//
// sourceKey is the stable identity token that determines the client's
// VirtualIP — the same sourceKey always hashes to the same 127.x.x.x
// candidate, subject to collision avoidance. alloc must be non-nil and is
// shared across all active Conns in the process.
//
// Dispatch callbacks and loggers flow in through functional options. Zero
// options is fine: the defaults are a no-op logger and a zero-value
// FrameDispatch. The identity-frame magic is always the 4-byte string
// [defaultIdentityMagic] ("NQIP") — the token NexQuake's WASM client
// recognizes.
//
// Application-layer identity (source IP for logging, user ID, admin flag)
// is the caller's concern; trunk does not track it.
//
// Call [Conn.Run] to start the I/O loops.
func NewConn(t Transport, alloc *VirtualIPAllocator, sourceKey string, opts ...Option) (*Conn, error) {
	var o connOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.warnf == nil {
		o.warnf = noopLogf
	}
	if o.debugf == nil {
		o.debugf = noopLogf
	}

	ctx, cancel := context.WithCancel(context.Background())

	clientIP, err := alloc.alloc(sourceKey)
	if err != nil {
		cancel()
		return nil, err
	}

	udpConn, err := net.ListenUDP("udp4", listenAddrForClient(clientIP))
	if err != nil {
		alloc.release(clientIP)
		cancel()
		return nil, fmt.Errorf("listen udp: %w", err)
	}

	return &Conn{
		xport:      t,
		udpConn:    udpConn,
		tx:         make(chan []byte, 1024),
		clientIP:   clientIP,
		sourceKey:  strings.TrimSpace(sourceKey),
		alloc:      alloc,
		dispatch:   o.dispatch,
		warnf:      o.warnf,
		debugf:     o.debugf,
		debugRelay: os.Getenv("DEBUG_RELAY") == "1",
		ctx:        ctx,
		cancel:     cancel,
	}, nil
}

// Close tears down the Conn: invokes HandleClose, cancels the context, closes
// the UDP and tunnel connections, and releases the VirtualIP. Safe to call
// more than once; all work happens at most once. Callers that track the Conn
// externally are responsible for dropping their reference.
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		if c.dispatch.HandleClose != nil {
			c.dispatch.HandleClose(c)
		}
		c.cancel()
		if c.udpConn != nil {
			_ = c.udpConn.Close()
		}
		if c.xport != nil {
			_ = c.xport.Close()
		}
		c.alloc.release(c.clientIP)
		c.clientIP = [4]byte{}
	})
}

// VirtualIP returns the Conn's 127.x.x.x VirtualIP as a dotted string.
// Returns an empty string after the Conn has been closed.
func (c *Conn) VirtualIP() string {
	if c.clientIP[0] == 0 {
		return ""
	}
	return net.IP(c.clientIP[:]).String()
}

// VirtualIPBytes returns the raw 4-byte VirtualIP.
func (c *Conn) VirtualIPBytes() [4]byte { return c.clientIP }

// SourceKey returns the stable identity token passed to NewConn.
func (c *Conn) SourceKey() string { return c.sourceKey }

// noteServerRoutePort records the last server port this client sent to.
func (c *Conn) noteServerRoutePort(port int) {
	c.routeMu.Lock()
	c.lastServerPort = port
	c.routeMu.Unlock()
}

// ActiveServerPort returns the last-routed server port.
func (c *Conn) ActiveServerPort() int {
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	return c.lastServerPort
}

// Run sends the identity frame, starts the UDP and tunnel I/O goroutines,
// and blocks until the connection closes. It calls Close before returning.
func (c *Conn) Run() {
	if frame := buildIdentityFrame(c.clientIP); len(frame) > 0 {
		c.sendFrame(frame, true)
	}

	go c.udpReadLoop()
	go c.writeLoop()
	c.readLoop()
	c.Close()
}

// readLoop pulls binary frames off the transport and dispatches them until
// the transport closes.
func (c *Conn) readLoop() {
	for {
		data, err := c.xport.ReadFrame()
		if err != nil {
			if c.ctx.Err() == nil {
				c.debugf("tunnel read error: %v", err)
			}
			c.Close()
			return
		}
		c.handleFrame(data)
	}
}

// writeLoop flushes queued frames to the transport and fires periodic
// keepalive pings.
func (c *Conn) writeLoop() {
	ping := time.NewTicker(keepalivePingInterval)
	defer ping.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ping.C:
			if err := c.xport.Ping(); err != nil {
				if c.ctx.Err() == nil {
					c.debugf("tunnel ping error: %v", err)
				}
				c.Close()
				return
			}
		case packet := <-c.tx:
			if len(packet) == 0 || c.ctx.Err() != nil {
				continue
			}
			if err := c.xport.WriteFrame(packet); err != nil {
				if c.ctx.Err() == nil {
					c.debugf("tunnel write error: %v", err)
				}
				c.Close()
				return
			}
		}
	}
}

// sendFrame enqueues a frame for the tunnel write loop.
// If drop is true, the frame is silently dropped when the channel is full.
func (c *Conn) sendFrame(frame []byte, drop bool) {
	if len(frame) == 0 || c.ctx.Err() != nil {
		return
	}

	if !drop {
		select {
		case <-c.ctx.Done():
		case c.tx <- frame:
		}
		return
	}

	select {
	case <-c.ctx.Done():
	case c.tx <- frame:
	default:
		c.warnf("trunk tx channel full, dropping packet")
	}
}

func (c *Conn) handleFrame(packet []byte) {
	dstPort, payload, ok := decodeFrame(packet)
	if !ok {
		return
	}

	if dstPort == controlPort {
		// Control-channel payload ownership stays outside trunk.
		if c.dispatch.HandleControlFrame != nil {
			if resp := c.dispatch.HandleControlFrame(c, payload); len(resp) > 0 {
				c.sendFrame(buildFrame(controlPort, resp), false)
			}
		}
		return
	}

	if !isValidServerPort(dstPort) {
		return
	}
	if c.dispatch.IsAllowedPort != nil && !c.dispatch.IsAllowedPort(dstPort) {
		c.debugf("dropping frame to unmanaged port %d", dstPort)
		return
	}

	c.noteServerRoutePort(dstPort)
	c.udpWrite(dstPort, payload)
}
