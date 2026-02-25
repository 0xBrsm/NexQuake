package nqnet

import (
	"context"
	"net"
)

// NewTestRouter creates a minimal Router for testing admin-frame handling.
// It allocates a virtual client IP so the router can be tracked in a SessionRegistry.
// The returned channel receives WS frames sent by sendWS/SendAdminReply.
func NewTestRouter(isAdmin bool) (*Router, chan []byte) {
	return NewTestRouterWith(isAdmin, nil, nil)
}

// NewTestRouterWith creates a test Router using the provided allocator and
// session registry. Pass nil for either to use fresh defaults.
func NewTestRouterWith(isAdmin bool, alloc *IPAllocator, sessions *SessionRegistry) (*Router, chan []byte) {
	if alloc == nil {
		alloc = NewIPAllocator(net.ParseIP(DefaultNQServerIP).To4())
	}
	if sessions == nil {
		sessions = NewSessionRegistry()
	}

	ch := make(chan []byte, 16)
	ctx, cancel := context.WithCancel(context.Background())
	sourceKey := "ip:198.51.100.10"
	sourceIP := "198.51.100.10"
	if isAdmin {
		sourceKey = "ip:198.51.100.11"
		sourceIP = "198.51.100.11"
	}
	clientIP, _ := alloc.alloc(sourceKey)
	r := &Router{
		wsTx:      ch,
		sessionID: nextRouterSessionID.Add(1),
		clientIP:  clientIP,
		sourceKey: sourceKey,
		sourceIP:  sourceIP,
		userID:    "",
		ctx:       ctx,
		cancel:    cancel,
		alloc:     alloc,
		sessions:  sessions,
		warnf:     noopLogf,
		debugf:    noopLogf,
	}
	r.isAdmin.Store(isAdmin)
	sessions.track(r)
	return r, ch
}
