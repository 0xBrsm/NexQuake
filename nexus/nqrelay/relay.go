package nqrelay

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// logf is a printf-style logging function.
type logf func(format string, args ...any)

func noopLogf(string, ...any) {}

// FrameDispatch provides callbacks for frames the Relay cannot handle itself.
type FrameDispatch struct {
	// HandleControlFrame is called for all ControlPort (control channel) frames.
	// It should return a response payload to send back, or nil to send nothing.
	HandleControlFrame func(relay *Relay, payload []byte) []byte

	// HandleClose is invoked once during relay close.
	HandleClose func(relay *Relay)

	// IsAllowedPort, if non-nil, gates which UDP destination ports a client
	// may target. Frames addressed to ports that return false are silently
	// dropped. When nil, all ports are allowed.
	IsAllowedPort func(port int) bool
}

// Relay relays WebSocket frames to/from UDP servers for a single client.
type Relay struct {
	ws        *websocket.Conn
	udpConn   *net.UDPConn
	wsTx      chan []byte
	sessionID uint64
	clientIP  [4]byte
	sourceKey string
	sourceIP  string
	userID    string
	isAdmin   atomic.Bool

	alloc    *IPAllocator
	sessions *SessionRegistry
	dispatch FrameDispatch

	warnf  logf
	debugf logf

	routeMu        sync.RWMutex
	lastServerPort int

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
}

var nextSessionID atomic.Uint64

// NewRelay creates a new WebSocket↔UDP relay.
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
		ws:        ws,
		udpConn:   udpConn,
		wsTx:      make(chan []byte, 1024),
		sessionID: nextSessionID.Add(1),
		clientIP:  clientIP,
		sourceKey: strings.TrimSpace(sourceKey),
		sourceIP:  strings.TrimSpace(sourceIP),
		userID:    strings.TrimSpace(userID),
		alloc:     alloc,
		sessions:  sessions,
		dispatch:  dispatch,
		warnf:     warnf,
		debugf:    debugf,
		ctx:       ctx,
		cancel:    cancel,
	}
	relay.isAdmin.Store(isAdmin)
	sessions.track(relay)
	return relay, nil
}

// Close tears down the relay, releasing all resources.
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

// VirtualClientIP returns the relay's virtual relay IP as a dotted string.
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

// SourceKey returns the relay's identity source key.
func (r *Relay) SourceKey() string {
	return r.sourceKey
}

// SourceIP returns the relay's best-effort source client IP.
func (r *Relay) SourceIP() string {
	return r.sourceIP
}

// UserID returns the best-effort authenticated user identity for this session.
func (r *Relay) UserID() string {
	return r.userID
}

// SessionID returns the relay's unique per-websocket session identifier.
func (r *Relay) SessionID() uint64 {
	return r.sessionID
}

// IsAdmin reports whether this relay has admin privileges.
func (r *Relay) IsAdmin() bool {
	return r.isAdmin.Load()
}

// PromoteAdmin marks this relay as an admin session.
func (r *Relay) PromoteAdmin() {
	r.isAdmin.Store(true)
}

// NoteServerRoutePort records the last server port this client sent to.
func (r *Relay) NoteServerRoutePort(port int) {
	if !isValidServerPort(port) {
		return
	}
	r.routeMu.Lock()
	r.lastServerPort = port
	r.routeMu.Unlock()
}

// activeServerPort returns the last-routed server port.
func (r *Relay) activeServerPort() int {
	r.routeMu.RLock()
	defer r.routeMu.RUnlock()
	return r.lastServerPort
}

// Run starts the relay loops; blocks until the connection closes.
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

// SendControlReply sends a control-channel payload (port 0) to the client.
func (r *Relay) SendControlReply(payload []byte) {
	if len(payload) == 0 {
		return
	}
	r.sendWS(buildWSFrame(ControlPort, payload), true)
}

// SendAdminReply is a compatibility wrapper for admin text replies.
func (r *Relay) SendAdminReply(msg string) {
	if msg == "" {
		return
	}
	r.SendControlReply([]byte(msg))
}

func (r *Relay) handleWSFrame(packet []byte) {
	dstPort, payload, ok := decodeWSFrame(packet)
	if !ok {
		return
	}

	if dstPort == ControlPort {
		// Control-channel payload ownership stays outside nqrelay.
		if r.dispatch.HandleControlFrame != nil {
			if resp := r.dispatch.HandleControlFrame(r, payload); len(resp) > 0 {
				r.sendWS(buildWSFrame(ControlPort, resp), false)
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

	r.NoteServerRoutePort(dstPort)
	r.udpWrite(dstPort, payload)
}
