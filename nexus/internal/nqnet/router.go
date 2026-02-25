package nqnet

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

// FrameDispatch provides callbacks for frames the Router cannot handle itself.
type FrameDispatch struct {
	// HandleSlistRequest is called for CCREQ_SERVER_INFO frames (port 0).
	// It should return the CCREP response payload (or nil to skip).
	HandleSlistRequest func(payload []byte) []byte

	// HandleAdminFrame is called for non-slist port-0 frames (rcon).
	HandleAdminFrame func(router *Router, payload []byte)

	// ResolveDestinationPort rewrites client frame destination ports.
	// Return ok=false to drop the frame.
	ResolveDestinationPort func(router *Router, dstPort int) (resolvedPort int, ok bool)

	// RewriteSourcePort rewrites server reply source ports before WS send.
	RewriteSourcePort func(router *Router, srcPort int) (rewrittenPort int)

	// HandleRouterClose is invoked once during router close.
	HandleRouterClose func(router *Router)
}

// Router relays WebSocket frames to/from UDP servers for a single client.
type Router struct {
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

var nextRouterSessionID atomic.Uint64

// NewRouter creates a new WebSocket↔UDP relay router.
func NewRouter(
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
) (*Router, error) {
	ctx, cancel := context.WithCancel(context.Background())

	clientIP, err := alloc.alloc(sourceKey)
	if err != nil {
		cancel()
		return nil, err
	}

	udpConn, err := net.ListenUDP("udp4", relayListenAddrForClient(clientIP))
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

	router := &Router{
		ws:        ws,
		udpConn:   udpConn,
		wsTx:      make(chan []byte, 1024),
		sessionID: nextRouterSessionID.Add(1),
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
	router.isAdmin.Store(isAdmin)
	sessions.track(router)
	return router, nil
}

// Close tears down the router, releasing all resources.
func (r *Router) Close() {
	r.closeOnce.Do(func() {
		if r.dispatch.HandleRouterClose != nil {
			r.dispatch.HandleRouterClose(r)
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

// VirtualClientIP returns the router's virtual relay IP as a dotted string.
func (r *Router) VirtualClientIP() string {
	if r.clientIP[0] == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", r.clientIP[0], r.clientIP[1], r.clientIP[2], r.clientIP[3])
}

// ClientIP returns the raw 4-byte client IP.
func (r *Router) ClientIP() [4]byte {
	return r.clientIP
}

// SourceKey returns the router's identity source key.
func (r *Router) SourceKey() string {
	return r.sourceKey
}

// SourceIP returns the router's best-effort source client IP.
func (r *Router) SourceIP() string {
	return r.sourceIP
}

// UserID returns the best-effort authenticated user identity for this session.
func (r *Router) UserID() string {
	return r.userID
}

// SessionID returns the router's unique per-websocket session identifier.
func (r *Router) SessionID() uint64 {
	return r.sessionID
}

// IsAdmin reports whether this router has admin privileges.
func (r *Router) IsAdmin() bool {
	return r.isAdmin.Load()
}

// PromoteAdmin marks this router as an admin session.
func (r *Router) PromoteAdmin() {
	r.isAdmin.Store(true)
}

// NoteServerRoutePort records the last server port this client sent to.
func (r *Router) NoteServerRoutePort(port int) {
	if port < 1 || port > 65535 {
		return
	}
	r.routeMu.Lock()
	r.lastServerPort = port
	r.routeMu.Unlock()
}

// activeServerPort returns the last-routed server port.
func (r *Router) activeServerPort() int {
	r.routeMu.RLock()
	port := r.lastServerPort
	r.routeMu.RUnlock()
	return port
}

// Run starts the relay loops; blocks until the connection closes.
func (r *Router) Run() {
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
func (r *Router) sendWS(frame []byte, drop bool) {
	if len(frame) == 0 || r.ctx.Err() != nil {
		return
	}

	if drop {
		select {
		case <-r.ctx.Done():
		case r.wsTx <- frame:
		default:
			r.warnf("ws tx channel full, dropping packet")
		}
		return
	}

	select {
	case <-r.ctx.Done():
	case r.wsTx <- frame:
	}
}

// SendAdminReply sends a text message on port 0 (admin channel) to the client.
func (r *Router) SendAdminReply(msg string) {
	if msg == "" {
		return
	}

	frame := make([]byte, WSPortHeaderSize+len(msg))
	frame[0] = 0
	frame[1] = 0
	copy(frame[WSPortHeaderSize:], msg)
	r.sendWS(frame, true)
}

func (r *Router) handleWSFrame(packet []byte) {
	if len(packet) < WSPortHeaderSize {
		return
	}

	dstPort := int(packet[0])<<8 | int(packet[1])
	payload := packet[WSPortHeaderSize:]

	if dstPort == 0 {
		if _, ok := parseCCREQServerInfo(payload); r.dispatch.HandleSlistRequest != nil && ok {
			if resp := r.dispatch.HandleSlistRequest(payload); len(resp) > 0 {
				r.sendWS(buildWSFrame(0, resp), false)
			}
		} else if r.dispatch.HandleAdminFrame != nil {
			r.dispatch.HandleAdminFrame(r, payload)
		}
		return
	}

	resolvedPort := dstPort
	if r.dispatch.ResolveDestinationPort != nil {
		var ok bool
		resolvedPort, ok = r.dispatch.ResolveDestinationPort(r, dstPort)
		if !ok {
			return
		}
	}
	if resolvedPort < 1 || resolvedPort > 65535 {
		return
	}

	r.NoteServerRoutePort(resolvedPort)
	r.udpWrite(resolvedPort, payload)
}
