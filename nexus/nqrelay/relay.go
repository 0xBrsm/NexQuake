package nqrelay

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// logf is a printf-style logging function.
type logf func(format string, args ...any)

func noopLogf(string, ...any) {}

// FrameDispatch holds application-defined callbacks that the Relay invokes for
// frames it cannot handle internally. All fields are optional (nil is safe).
type FrameDispatch struct {
	// HandleControlFrame is called for every incoming control-channel frame
	// (port 0). Return a non-nil slice to send a reply on the control channel;
	// return nil to send nothing. The payload slice is only valid for the
	// duration of the call — copy it if you need to retain it.
	HandleControlFrame func(relay *Relay, payload []byte) []byte

	// HandleClose is called once when the relay is closing, before the session
	// is untracked and sockets are torn down.
	HandleClose func(relay *Relay)

	// IsAllowedPort, if non-nil, is called for every incoming non-control frame
	// before the payload is forwarded over UDP. Return true to allow, false to
	// drop. When nil, all destination ports are allowed.
	IsAllowedPort func(port int) bool
}

// Relay relays WebSocket frames to/from UDP servers for a single client.
// Each Relay owns one WebSocket connection and one UDP socket bound to its
// virtual IP. Three goroutines run concurrently: wsReadLoop, wsWriteLoop,
// and udpReadLoop; all are started by Run and exit when the relay closes.
type Relay struct {
	ws      *websocket.Conn
	udpConn *net.UDPConn
	wsTx    chan []byte // outbound WS frames; bounded to 1024 entries

	clientIP  [4]byte
	sourceKey string
	sourceIP  string
	userID    string
	isAdmin   atomic.Bool

	alloc    *IPAllocator
	sessions *SessionRegistry
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

// NewRelay creates a WebSocket↔UDP relay for a single client connection.
//
// sourceKey is the stable identity token for this client (e.g. a user ID or
// auth token fingerprint). It determines the client's virtual IP: the same
// sourceKey always hashes to the same 127.x.x.x candidate, subject to
// collision avoidance. sourceIP is a best-effort human-readable address for
// logging. userID is an optional application-layer identifier stored on the
// relay and surfaced in session snapshots.
//
// alloc and sessions must be non-nil and shared across all active relays.
// warnf and debugf may be nil (logging is silently suppressed).
//
// On success, the relay is tracked in sessions and owns the WebSocket
// connection. Call [Relay.Run] to start the relay loops.
func NewRelay(
	ws *websocket.Conn,
	sourceKey string,
	sourceIP string,
	userID string,
	isAdmin bool,
	alloc *IPAllocator,
	sessions *SessionRegistry,
	dispatch FrameDispatch,
	warnf logf,
	debugf logf,
) (*Relay, error) {
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

	if warnf == nil {
		warnf = noopLogf
	}
	if debugf == nil {
		debugf = noopLogf
	}

	relay := &Relay{
		ws:         ws,
		udpConn:    udpConn,
		wsTx:       make(chan []byte, 1024),
		clientIP:   clientIP,
		sourceKey:  strings.TrimSpace(sourceKey),
		sourceIP:   strings.TrimSpace(sourceIP),
		userID:     strings.TrimSpace(userID),
		alloc:      alloc,
		sessions:   sessions,
		dispatch:   dispatch,
		warnf:      warnf,
		debugf:     debugf,
		debugRelay: os.Getenv("DEBUG_RELAY") == "1",
		ctx:        ctx,
		cancel:     cancel,
	}
	relay.isAdmin.Store(isAdmin)
	sessions.track(relay)
	return relay, nil
}

// Close tears down the relay: invokes HandleClose, unregisters the session,
// cancels the context, closes the UDP and WebSocket connections, and releases
// the virtual IP. Safe to call more than once; all work happens at most once.
func (r *Relay) Close() {
	r.closeOnce.Do(func() {
		if r.dispatch.HandleClose != nil {
			r.dispatch.HandleClose(r)
		}
		r.sessions.untrack(r)
		r.cancel()
		if r.udpConn != nil {
			_ = r.udpConn.Close()
		}
		if r.ws != nil {
			_ = r.ws.Close()
		}
		r.alloc.release(r.clientIP)
		r.clientIP = [4]byte{}
	})
}

// VirtualClientIP returns the relay's 127.x.x.x virtual IP as a dotted string.
// Returns an empty string after the relay has been closed.
func (r *Relay) VirtualClientIP() string {
	if r.clientIP[0] == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", r.clientIP[0], r.clientIP[1], r.clientIP[2], r.clientIP[3])
}

// ClientIP returns the raw 4-byte client IP.
func (r *Relay) ClientIP() [4]byte {
	return r.clientIP
}

// SourceKey returns the stable identity token passed to NewRelay.
func (r *Relay) SourceKey() string {
	return r.sourceKey
}

// SourceIP returns the best-effort client address string passed to NewRelay
// (typically the HTTP remote address at upgrade time).
func (r *Relay) SourceIP() string {
	return r.sourceIP
}

// UserID returns the user identity string passed to NewRelay.
func (r *Relay) UserID() string {
	return r.userID
}

// IsAdmin reports whether this relay has admin privileges.
func (r *Relay) IsAdmin() bool {
	return r.isAdmin.Load()
}

// PromoteAdmin marks this relay as an admin session.
func (r *Relay) PromoteAdmin() {
	r.isAdmin.Store(true)
}

// noteServerRoutePort records the last server port this client sent to.
func (r *Relay) noteServerRoutePort(port int) {
	r.routeMu.Lock()
	r.lastServerPort = port
	r.routeMu.Unlock()
}

// activeServerPort returns the last-routed server port.
func (r *Relay) activeServerPort() int {
	r.routeMu.Lock()
	defer r.routeMu.Unlock()
	return r.lastServerPort
}

// Run sends the identity frame, starts the UDP and WebSocket I/O goroutines,
// and blocks until the connection closes. It calls Close before returning.
func (r *Relay) Run() {
	if frame := buildWSClientIdentityFrame(r.clientIP); len(frame) > 0 {
		r.sendWS(frame, true)
	}

	go r.udpReadLoop()
	go r.wsWriteLoop()
	r.wsReadLoop()
	r.Close()
}

// sendWS enqueues a frame for the WebSocket write loop.
// If drop is true, the frame is silently dropped when the channel is full.
func (r *Relay) sendWS(frame []byte, drop bool) {
	if len(frame) == 0 || r.ctx.Err() != nil {
		return
	}

	if !drop {
		select {
		case <-r.ctx.Done():
		case r.wsTx <- frame:
		}
		return
	}

	select {
	case <-r.ctx.Done():
	case r.wsTx <- frame:
	default:
		r.warnf("ws tx channel full, dropping packet")
	}
}

// sendControlReply sends a control-channel payload (port 0) to the client.
func (r *Relay) sendControlReply(payload []byte) {
	if len(payload) == 0 {
		return
	}
	// Admin/control replies must not be silently dropped behind game traffic.
	r.sendWS(buildWSFrame(controlPort, payload), false)
}

// maxAdminReplyChunk is the maximum payload size for a single admin-reply
// control frame. The client enforces MAX_WS_MESSAGE_SIZE = NET_DATAGRAMSIZE+2
// (1034 bytes) and silently drops larger WebSocket messages. We chunk at 1000
// to leave comfortable headroom for the 2-byte port header.
const maxAdminReplyChunk = 1000

// SendAdminReply sends msg as one or more control-channel (port 0) payloads to
// the client. Messages longer than [maxAdminReplyChunk] are split into multiple
// frames so the client does not silently drop them. Empty strings are ignored.
func (r *Relay) SendAdminReply(msg string) {
	if msg == "" {
		return
	}
	for len(msg) > maxAdminReplyChunk {
		r.sendControlReply([]byte(msg[:maxAdminReplyChunk]))
		msg = msg[maxAdminReplyChunk:]
	}
	r.sendControlReply([]byte(msg))
}

func (r *Relay) handleWSFrame(packet []byte) {
	dstPort, payload, ok := decodeWSFrame(packet)
	if !ok {
		return
	}

	if dstPort == controlPort {
		// Control-channel payload ownership stays outside nqrelay.
		if r.dispatch.HandleControlFrame != nil {
			if resp := r.dispatch.HandleControlFrame(r, payload); len(resp) > 0 {
				r.sendWS(buildWSFrame(controlPort, resp), false)
			}
		}
		return
	}

	if !isValidServerPort(dstPort) {
		return
	}
	if r.dispatch.IsAllowedPort != nil && !r.dispatch.IsAllowedPort(dstPort) {
		r.debugf("dropping frame to unmanaged port %d", dstPort)
		return
	}

	r.noteServerRoutePort(dstPort)
	r.udpWrite(dstPort, payload)
}
