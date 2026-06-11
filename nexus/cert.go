package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/trunk"
)

const (
	// wtCertValidity is the maximum WebTransport cert lifetime browsers will
	// accept via serverCertificateHashes (W3C-mandated 14 days). Setting
	// NotAfter at exactly the ceiling gives the longest possible reuse
	// before the rotation timer must fire.
	wtCertValidity = 14 * 24 * time.Hour

	// wtCertRotateBuffer is the safety margin before NotAfter we never
	// schedule rotation past, so a cert is always replaced before it could
	// expire mid-handshake.
	wtCertRotateBuffer = 24 * time.Hour
)

// wtCertSnapshot pairs a cert with its hex SHA-256 so a single atomic load
// returns a self-consistent (cert, hash) — readers can never observe a hash
// from a different generation than the cert.
type wtCertSnapshot struct {
	cert    *tls.Certificate
	hashHex string
}

// wtCertManager owns the WebTransport TLS certificate lifecycle. The cert
// is auto-generated (ECDSA P-256, 14-day validity) on startup and rotated
// every rotateEvery to stay below the serverCertificateHashes ceiling.
// Hot rotation is via tls.Config.GetCertificate, so in-flight sessions
// stay on whichever cert they handshook with.
type wtCertManager struct {
	certPath    string
	keyPath     string
	rotateEvery time.Duration
	hosts       []string

	snap atomic.Pointer[wtCertSnapshot]
}

// newWTCertManager prepares a manager and ensures a usable cert exists on
// disk. If the on-disk cert has too little life left for the rotation
// schedule, a fresh one is generated. Returns the manager ready to plug
// into tls.Config and to drive on Run.
func newWTCertManager(dir string, rotateEvery time.Duration, hosts []string) (*wtCertManager, error) {
	if rotateEvery <= 0 || rotateEvery >= wtCertValidity-wtCertRotateBuffer {
		return nil, errors.New("rotateEvery must be between 0 and 13 days")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	m := &wtCertManager{
		certPath:    filepath.Join(dir, "cert.pem"),
		keyPath:     filepath.Join(dir, "key.pem"),
		rotateEvery: rotateEvery,
		hosts:       hosts,
	}
	if err := m.ensure(); err != nil {
		return nil, err
	}
	return m, nil
}

// GetCertificate is plumbed into tls.Config; called per QUIC handshake.
func (m *wtCertManager) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	snap := m.snap.Load()
	if snap == nil {
		return nil, errors.New(trunk.TransportWebTransport + " cert not initialized")
	}
	return snap.cert, nil
}

// Hash returns the current cert's SHA-256 (hex) for serverCertificateHashes.
// Returns empty string if not initialized.
func (m *wtCertManager) Hash() string {
	if snap := m.snap.Load(); snap != nil {
		return snap.hashHex
	}
	return ""
}

// Run drives the rotation schedule until ctx is canceled. Wakeup interval
// is recomputed after each rotation so a freshly-loaded cert is replaced at
// roughly the right age regardless of when the process started.
func (m *wtCertManager) Run(ctx context.Context) {
	for {
		delay := m.nextRotationDelay()
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
			if err := m.rotate(); err != nil {
				slog.Error(trunk.TransportWebTransport+" cert rotate failed", "err", err)
				// Retry after one rotation interval; don't tight-loop.
				select {
				case <-ctx.Done():
					return
				case <-time.After(m.rotateEvery):
				}
			} else {
				slog.Info(trunk.TransportWebTransport+" cert rotated", "sha256", m.Hash())
			}
		}
	}
}

// ensure loads cert from disk if it has enough life left, otherwise
// generates and persists a fresh cert.
func (m *wtCertManager) ensure() error {
	if cert, err := loadCertFromDisk(m.certPath, m.keyPath); err == nil {
		if cert.Leaf != nil && time.Until(cert.Leaf.NotAfter) > wtCertRotateBuffer {
			m.swap(cert)
			return nil
		}
	}
	return m.rotate()
}

// rotate generates a fresh cert, persists it, and atomically swaps it in.
func (m *wtCertManager) rotate() error {
	cert, err := generateWTCert(m.hosts)
	if err != nil {
		return err
	}
	if err := persistCert(m.certPath, m.keyPath, cert); err != nil {
		return err
	}
	m.swap(cert)
	return nil
}

func (m *wtCertManager) swap(cert *tls.Certificate) {
	m.snap.Store(&wtCertSnapshot{
		cert:    cert,
		hashHex: certSHA256Hex(cert),
	})
}

// nextRotationDelay computes when the next rotation should fire. Targets
// (estimated cert creation + rotateEvery) but is clamped to fire no later
// than rotateBuffer before NotAfter so a cert is always replaced before
// it could expire. Caller must have ensured the snapshot is populated
// (newWTCertManager.ensure → swap).
func (m *wtCertManager) nextRotationDelay() time.Duration {
	cert := m.snap.Load().cert
	now := time.Now()
	target := cert.Leaf.NotAfter.Add(-wtCertValidity).Add(m.rotateEvery)
	if target.Before(now) {
		target = now
	}
	safeMax := cert.Leaf.NotAfter.Add(-wtCertRotateBuffer)
	if target.After(safeMax) {
		target = safeMax
	}
	if delay := target.Sub(now); delay > 0 {
		return delay
	}
	return 0
}

// generateWTCert mints a fresh ECDSA P-256 self-signed cert with 14-day
// validity. localhost + 127.0.0.1 + ::1 are always covered; extra hosts
// are added as DNS or IP SANs depending on parseability.
func generateWTCert(hosts []string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"NexQuake"}, CommonName: "NexQuake WebTransport"},
		NotBefore:             now,
		NotAfter:              now.Add(wtCertValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else if h != "" {
			template.DNSNames = append(template.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// persistCert writes cert.pem and key.pem atomically. Mode 0o600 on the
// key, 0o644 on the cert (the cert is published anyway).
func persistCert(certPath, keyPath string, cert *tls.Certificate) error {
	if len(cert.Certificate) == 0 {
		return errors.New("cert has no DER bytes")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := writeFileAtomic(certPath, certPEM, 0o644); err != nil {
		return err
	}
	return writeFileAtomic(keyPath, keyPEM, 0o600)
}

// writeFileAtomic writes data to a temp file in the same dir, then renames
// to path so readers never see a partial file. Both the file and the parent
// dir are fsynced so the rename survives a crash.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// loadCertFromDisk parses the on-disk pair and populates Leaf so callers
// can read NotAfter without re-parsing.
func loadCertFromDisk(certPath, keyPath string) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	if len(cert.Certificate) == 0 {
		return nil, errors.New("loaded cert has no DER bytes")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	cert.Leaf = leaf
	return &cert, nil
}

// certSHA256Hex returns the hex-encoded SHA-256 of the leaf cert's DER
// bytes — the input WebTransport's serverCertificateHashes wants.
func certSHA256Hex(cert *tls.Certificate) string {
	if cert == nil || len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:])
}
