package admin

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

// Identity resolves a stable source IP and identity key for any incoming HTTP
// request. It is concerned with *who / where* a client is, independent of
// whether that client has privileged credentials.
//
// When Nexus sits behind a reverse proxy, the proxy is trusted to supply the
// real client IP via the AUTH_CLIENT_IP_HEADER header (e.g. "CF-Connecting-IP",
// "X-Forwarded-For"). When unset or unparseable, resolution falls back to the
// direct TCP remote address.
type Identity struct {
	clientIPHeader string
}

// NewIdentity constructs an [Identity] from the AUTH_CLIENT_IP_HEADER
// environment variable. Safe to call exactly once at startup.
func NewIdentity() *Identity {
	return &Identity{
		clientIPHeader: strings.TrimSpace(os.Getenv("AUTH_CLIENT_IP_HEADER")),
	}
}

// ClientSourceIP returns the external client IP for a request, preferring the
// configured trusted header over the direct connection address. Returns "" if
// neither yields a parseable IP.
func (i *Identity) ClientSourceIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if i != nil && i.clientIPHeader != "" {
		if ip, ok := parseClientIP(r.Header.Get(i.clientIPHeader)); ok {
			return ip.String()
		}
	}
	if ip, ok := parseClientIP(r.RemoteAddr); ok {
		return ip.String()
	}
	return ""
}

// ClientSourceKey derives a stable identity key from an HTTP request, used for
// ban tracking and per-client bookkeeping.
func (i *Identity) ClientSourceKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	if sourceIP := i.ClientSourceIP(r); sourceIP != "" {
		return "ip:" + sourceIP
	}
	return strings.TrimSpace(r.RemoteAddr)
}

// parseClientIP extracts an IP address from a raw header value or remote addr
// string, handling Forwarded/X-Forwarded-For formats and port stripping.
func parseClientIP(raw string) (netip.Addr, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return netip.Addr{}, false
	}

	if comma := strings.IndexByte(value, ','); comma >= 0 {
		value = strings.TrimSpace(value[:comma])
	}
	if k, v, ok := strings.Cut(value, "="); ok && strings.EqualFold(strings.TrimSpace(k), "for") {
		value = strings.TrimSpace(v)
	}
	value = strings.Trim(strings.TrimSpace(value), "\"")
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	ip, err := netip.ParseAddr(strings.Trim(value, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}
