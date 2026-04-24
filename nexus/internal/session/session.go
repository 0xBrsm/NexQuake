package session

import (
	"encoding/binary"
	"net/netip"
	"sort"
	"strings"
	"sync"
)

// Channel is the transport-specific attachment for a Session. WebSocket relays,
// future WebTransport streams, or other session-bearing transports can satisfy
// this interface without the registry depending on a concrete transport type.
type Channel interface {
	ClientNQIP() string
	ClientIP() [4]byte
	SourceKey() string
	ActiveServerPort() int
	SendAdminReply(string)
	Close()
}

// Session is the unified record of a user's presence in Nexus. One Session
// spans transports and channels; transports attach to and detach from a
// Session over its lifetime while the Session itself persists.
//
// Sessions are identified internally by their *Session handle and externally
// by (source IP) or (NQIP) for registry lookups. There is no opaque
// session token sent to the client — cross-channel correlation is done via
// the real source IP (see [Registry.LookupBySourceIP]).
type Session struct {
	mu       sync.RWMutex
	sourceIP string
	identity string
	isAdmin  bool
	channel  Channel
}

// SourceIP returns the best-effort real client IP associated with this session.
func (s *Session) SourceIP() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sourceIP
}

// Identity returns the application-layer identity (e.g. OIDC email/sub), or ""
// for anonymous sessions.
func (s *Session) Identity() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.identity
}

// IsAdmin reports whether this session holds admin privileges. Admin status
// may be set at creation (via OIDC JWT) or later via rcon_password promotion.
func (s *Session) IsAdmin() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isAdmin
}

// PromoteAdmin marks this session as admin. Idempotent.
func (s *Session) PromoteAdmin() {
	s.mu.Lock()
	s.isAdmin = true
	s.mu.Unlock()
}

// Channel returns the currently attached transport channel, or nil if none is
// attached.
func (s *Session) Channel() Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.channel
}

// ClientNQIP returns the NQ NQIP of the attached channel, or "".
func (s *Session) ClientNQIP() string {
	if ch := s.Channel(); ch != nil {
		return ch.ClientNQIP()
	}
	return ""
}

// ClientIP returns the raw 4-byte NQIP of the attached channel.
func (s *Session) ClientIP() [4]byte {
	if ch := s.Channel(); ch != nil {
		return ch.ClientIP()
	}
	return [4]byte{}
}

// SourceKey returns the attached channel's stable source identity key.
func (s *Session) SourceKey() string {
	if ch := s.Channel(); ch != nil {
		return ch.SourceKey()
	}
	return ""
}

// ActiveServerPort returns the last routed server port for the attached
// channel, or 0 if no game channel is attached.
func (s *Session) ActiveServerPort() int {
	if ch := s.Channel(); ch != nil {
		return ch.ActiveServerPort()
	}
	return 0
}

// SendAdminReply delivers text to the attached channel's admin path. No-op
// when no channel is attached.
func (s *Session) SendAdminReply(msg string) {
	if ch := s.Channel(); ch != nil {
		ch.SendAdminReply(msg)
	}
}

// Close terminates the attached channel, if any. Does not remove the session
// from the registry.
func (s *Session) Close() {
	if ch := s.Channel(); ch != nil {
		ch.Close()
	}
}

// Snapshot is a point-in-time view of a session, safe to display to admins.
type Snapshot struct {
	NQIP        string
	SourceIP         string
	UserID           string
	IsAdmin          bool
	ActiveServerPort int
}

// BanTarget pairs a NQIP with the server port the client was last routed
// to — the tuple a game-server ban command needs.
type BanTarget struct {
	Port      int
	NQIP string
}

// Registry tracks active Sessions. Indexed by source IP (for cross-channel
// correlation, e.g. matching an HTTP /rcon request to the WS session it came
// from) and by NQIP (for ban-by-NQIP resolution). Safe for concurrent use.
type Registry struct {
	mu         sync.RWMutex
	all        map[*Session]struct{}
	bySourceIP map[string]map[*Session]struct{}
	byNQIPKey   map[uint32]map[*Session]struct{}
}

func NewRegistry() *Registry {
	return &Registry{
		all:        make(map[*Session]struct{}),
		bySourceIP: make(map[string]map[*Session]struct{}),
		byNQIPKey:   make(map[uint32]map[*Session]struct{}),
	}
}

// Create mints a new Session and registers it. Indexed by source IP immediately
// when non-empty; NQIP indexing happens on [Registry.AttachChannel].
func (r *Registry) Create(sourceIP, identity string, isAdmin bool) *Session {
	s := &Session{
		sourceIP: sourceIP,
		identity: identity,
		isAdmin:  isAdmin,
	}
	r.mu.Lock()
	r.all[s] = struct{}{}
	if ip := strings.TrimSpace(sourceIP); ip != "" {
		r.addToSourceIPLocked(ip, s)
	}
	r.mu.Unlock()
	return s
}

// Remove deletes the Session, clearing all indexes. Safe to call more than once.
func (r *Registry) Remove(s *Session) {
	if s == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.all[s]; !ok {
		return
	}
	delete(r.all, s)
	if ip := strings.TrimSpace(s.SourceIP()); ip != "" {
		r.removeFromSourceIPLocked(ip, s)
	}
	r.removeFromNQIPLocked(s)
}

// LookupBySourceIP returns all sessions whose source IP matches. Useful for
// matching an HTTP request back to the WS session(s) it originated from.
// Multiple sessions may share a source IP (e.g., users behind a common NAT
// or multiple tabs from the same browser).
func (r *Registry) LookupBySourceIP(sourceIP string) []*Session {
	sourceIP = strings.TrimSpace(sourceIP)
	if sourceIP == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := r.bySourceIP[sourceIP]
	if len(set) == 0 {
		return nil
	}
	out := make([]*Session, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}

// AttachChannel binds a transport channel to the session and indexes it by the
// channel's NQIP so ban-by-NQIP can find it.
func (r *Registry) AttachChannel(s *Session, ch Channel) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.removeFromNQIPLocked(s)

	s.mu.Lock()
	s.channel = ch
	s.mu.Unlock()

	nqipKey, ok := nqipKeyFromChannel(ch)
	if !ok {
		return
	}
	set := r.byNQIPKey[nqipKey]
	if set == nil {
		set = make(map[*Session]struct{})
		r.byNQIPKey[nqipKey] = set
	}
	set[s] = struct{}{}
}

// DetachChannel drops the channel attachment and removes the NQIP index entry.
func (r *Registry) DetachChannel(s *Session) {
	r.mu.Lock()
	r.removeFromNQIPLocked(s)
	r.mu.Unlock()

	s.mu.Lock()
	s.channel = nil
	s.mu.Unlock()
}

// SnapshotAll returns a point-in-time view of every tracked session. Returns
// nil when no sessions are tracked.
func (r *Registry) SnapshotAll() []Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.all) == 0 {
		return nil
	}
	out := make([]Snapshot, 0, len(r.all))
	for s := range r.all {
		out = append(out, snapshotOf(s))
	}
	return out
}

// SnapshotByNQIP returns all sessions attached to the given NQIP together
// with the deduplicated, sorted set of ban targets derived from their active
// server ports. Returns (nil, nil) for an unknown or invalid NQIP.
func (r *Registry) SnapshotByNQIP(nqip string) (sessions []*Session, targets []BanTarget) {
	nqipKey, ok := parseNQIPKey(nqip)
	if !ok {
		return nil, nil
	}
	nqip = nqipStringFromKey(nqipKey)

	r.mu.RLock()
	defer r.mu.RUnlock()
	set := r.byNQIPKey[nqipKey]
	if len(set) == 0 {
		return nil, nil
	}

	sessions = make([]*Session, 0, len(set))
	targets = make([]BanTarget, 0, len(set))
	for s := range set {
		sessions = append(sessions, s)
		port := s.ActiveServerPort()
		if isValidServerPort(port) {
			targets = append(targets, BanTarget{Port: port, NQIP: nqip})
		}
	}
	return sessions, sortedUniqueTargets(targets)
}

func (r *Registry) addToSourceIPLocked(sourceIP string, s *Session) {
	set := r.bySourceIP[sourceIP]
	if set == nil {
		set = make(map[*Session]struct{})
		r.bySourceIP[sourceIP] = set
	}
	set[s] = struct{}{}
}

func (r *Registry) removeFromSourceIPLocked(sourceIP string, s *Session) {
	set := r.bySourceIP[sourceIP]
	if set == nil {
		return
	}
	delete(set, s)
	if len(set) == 0 {
		delete(r.bySourceIP, sourceIP)
	}
}

func (r *Registry) removeFromNQIPLocked(s *Session) {
	nqipKey, ok := nqipKeyFromChannel(s.Channel())
	if !ok {
		return
	}
	set := r.byNQIPKey[nqipKey]
	if set == nil {
		return
	}
	delete(set, s)
	if len(set) == 0 {
		delete(r.byNQIPKey, nqipKey)
	}
}

func snapshotOf(s *Session) Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := Snapshot{
		SourceIP: s.sourceIP,
		UserID:   s.identity,
		IsAdmin:  s.isAdmin,
	}
	if s.channel != nil {
		snap.NQIP = s.channel.ClientNQIP()
		snap.ActiveServerPort = s.channel.ActiveServerPort()
	}
	return snap
}

func nqipKeyFromChannel(ch Channel) (uint32, bool) {
	if ch == nil {
		return 0, false
	}
	ip4 := ch.ClientIP()
	if ip4[0] == 0 {
		return 0, false
	}
	return binary.BigEndian.Uint32(ip4[:]), true
}

func parseNQIPKey(nqip string) (uint32, bool) {
	nqip = strings.TrimSpace(nqip)
	if nqip == "" {
		return 0, false
	}
	addr, err := netip.ParseAddr(nqip)
	if err != nil {
		return 0, false
	}
	addr = addr.Unmap()
	if !addr.Is4() {
		return 0, false
	}
	ip4 := addr.As4()
	if ip4[0] == 0 {
		return 0, false
	}
	return binary.BigEndian.Uint32(ip4[:]), true
}

func nqipStringFromKey(nqipKey uint32) string {
	var ip4 [4]byte
	binary.BigEndian.PutUint32(ip4[:], nqipKey)
	return netip.AddrFrom4(ip4).String()
}

func isValidServerPort(port int) bool {
	return port > 0 && port <= 65535
}

func sortedUniqueTargets(in []BanTarget) []BanTarget {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[BanTarget]struct{}, len(in))
	out := make([]BanTarget, 0, len(in))
	for _, t := range in {
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].NQIP < out[j].NQIP
	})
	return out
}
