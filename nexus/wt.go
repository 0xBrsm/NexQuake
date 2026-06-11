package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/trunk"
	"github.com/0xBrsm/NexQuake/nexus/trunk/webtransport"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	qwt "github.com/quic-go/webtransport-go"
)

// WebTransport. Nexus auto-generates a self-signed ECDSA P-256 cert at
// WT_CERT_DIR on startup and rotates every WT_CERT_ROTATE_DAYS (browsers
// cap serverCertificateHashes-validated certs at 14 days). The WT listener
// binds to UDP/HTTP_PORT — TCP and UDP socket spaces don't collide, so the
// WS and WT listeners coexist on the same port.
//
// WT_HOST is the externally-reachable WT URL host, optionally with a port —
// e.g. "pi.local", "10.0.0.5", or "pi.local:1337". It is used verbatim as the
// advertised URL authority and pushed to the browser via /start so the client
// targets it. The port (if any) is stripped before the host is added to the
// cert's SAN list, since a port is not valid in a SAN. Empty disables WT
// entirely — no listener, no cert, no manifest fields.

// setupWebTransport configures the WebTransport listener when WT_HOST is set.
// Returns the configured *qwt.Server (caller drives ListenAndServe and Close)
// or nil when WT is disabled. A cert-rotation goroutine runs against runCtx;
// the cert hash is registered with the bootstrap manifest so /start carries
// it for the WASM client.
func setupWebTransport(app *nexusApp, runCtx context.Context) (*qwt.Server, error) {
	wtHost := strings.TrimSpace(os.Getenv("WT_HOST"))
	if wtHost == "" {
		return nil, nil
	}
	certDir := getEnv("WT_CERT_DIR", "/app/tls")
	// Rotation must land at least the safety buffer before the 14-day browser
	// cap, so whole days above the max would fail newWTCertManager's check.
	// Out-of-range values fall back to the default with a warning, matching
	// getEnvIntMin's handling of values below the minimum.
	rotateDays := getEnvIntMin("WT_CERT_ROTATE_DAYS", 9, 1)
	if maxDays := int((wtCertValidity-wtCertRotateBuffer)/(24*time.Hour)) - 1; rotateDays > maxDays {
		slog.Warn(fmt.Sprintf("Invalid WT_CERT_ROTATE_DAYS=%d (expected integer <= %d); using 9", rotateDays, maxDays))
		rotateDays = 9
	}
	certRotateEvery := time.Duration(rotateDays) * 24 * time.Hour

	// WT_HOST may carry a port for the advertised URL; the cert SAN must be the
	// bare host (a port is not valid in a SAN), so strip it when present. IPv6
	// brackets are URL syntax, not part of the address, so they go too —
	// SplitHostPort only removes them when a port follows.
	certHost := wtHost
	if host, _, err := net.SplitHostPort(wtHost); err == nil {
		certHost = host
	}
	certHost = strings.Trim(certHost, "[]")

	cert, err := newWTCertManager(certDir, certRotateEvery, []string{certHost})
	if err != nil {
		return nil, err
	}
	go cert.Run(runCtx)

	// Register WT-specific manifest fields. The provider is invoked per /start
	// request so the live cert hash propagates within one fetch (rotations
	// take effect on the next manifest issue). WT_HOST is used verbatim as the
	// URL authority, so it carries the externally-reachable port when set.
	app.AddBootstrapClientFields(func() map[string]any {
		return map[string]any{
			"transports": map[string]any{
				"webtransport": map[string]any{
					"url":           "https://" + wtHost + "/connect",
					"certSha256Hex": cert.Hash(),
				},
			},
		}
	})

	// HTTP/3 needs its own listener (UDP/QUIC); it cannot share Nexus's TCP
	// HTTP server. Both listeners bind to the same port number — TCP and UDP
	// socket spaces don't collide. The cert is delivered via GetCertificate
	// so rotation hot-swaps the active cert at handshake time.
	wt := &qwt.Server{
		H3: &http3.Server{
			Addr: ":" + app.cfg.httpPort,
			TLSConfig: &tls.Config{
				MinVersion:     tls.VersionTLS13,
				GetCertificate: cert.GetCertificate,
			},
			QUICConfig: &quic.Config{
				MaxIdleTimeout:  120 * time.Second,
				KeepAlivePeriod: 15 * time.Second,
			},
		},
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	wt.H3.TLSConfig = http3.ConfigureTLSConfig(wt.H3.TLSConfig)
	qwt.ConfigureHTTP3Server(wt.H3)

	h3mux := http.NewServeMux()
	h3mux.HandleFunc("/connect", app.handleWebTransport(wt))
	wt.H3.Handler = app.access.HTTPGate(h3mux)
	return wt, nil
}

func (app *nexusApp) handleWebTransport(wt *qwt.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app.trunkSession(w, r, trunk.TransportWebTransport, func() (trunk.Transport, error) {
			sess, err := wt.Upgrade(w, r)
			if err != nil {
				return nil, err
			}
			return webtransport.New(sess), nil
		})
	}
}
