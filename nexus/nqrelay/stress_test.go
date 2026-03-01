package nqrelay

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSessionRegistryStressSimultaneousClients(t *testing.T) {
	tiers := []struct {
		name    string
		clients int
	}{
		{name: "100_clients", clients: 100},
		{name: "1000_clients", clients: 1000},
		{name: "10000_clients", clients: 10000},
	}

	for _, tier := range tiers {
		tier := tier
		t.Run(tier.name, func(t *testing.T) {
			alloc := NewIPAllocator(net.ParseIP(DefaultNQServerIP).To4())
			sessions := NewSessionRegistry()

			relays, err := spawnStressRelays(tier.clients, alloc, sessions)
			if err != nil {
				t.Fatalf("spawnStressRelays(%d): %v", tier.clients, err)
			}
			t.Cleanup(func() {
				closeRelaysConcurrently(relays)
			})

			snapshots := sessions.SnapshotAll()
			if got := len(snapshots); got != tier.clients {
				t.Fatalf("SnapshotAll() count = %d, want %d", got, tier.clients)
			}

			seenVirtualIPs := make(map[string]struct{}, tier.clients)
			for _, snapshot := range snapshots {
				if snapshot.VirtualIP == "" {
					t.Fatalf("snapshot has empty virtual IP")
				}
				if _, dup := seenVirtualIPs[snapshot.VirtualIP]; dup {
					t.Fatalf("duplicate virtual IP: %s", snapshot.VirtualIP)
				}
				seenVirtualIPs[snapshot.VirtualIP] = struct{}{}
				if snapshot.ActiveServerPort < 26000 || snapshot.ActiveServerPort > 26007 {
					t.Fatalf("snapshot active server port = %d, want range [26000,26007]", snapshot.ActiveServerPort)
				}
			}

			sample := snapshots[len(snapshots)/2]
			relaysByVIP, targets := sessions.SnapshotByVirtualIP(sample.VirtualIP)
			if got := len(relaysByVIP); got != 1 {
				t.Fatalf("SnapshotByVirtualIP(%q) relays = %d, want 1", sample.VirtualIP, got)
			}
			if got := len(targets); got != 1 {
				t.Fatalf("SnapshotByVirtualIP(%q) targets = %d, want 1", sample.VirtualIP, got)
			}
			if targets[0].Port < 26000 || targets[0].Port > 26007 {
				t.Fatalf("target port = %d, want range [26000,26007]", targets[0].Port)
			}

			closeRelaysConcurrently(relays)
			if got := len(sessions.SnapshotAll()); got != 0 {
				t.Fatalf("SnapshotAll() after Close = %d, want 0", got)
			}
		})
	}
}

func TestSessionRegistryStressSimultaneousClientsFullStack(t *testing.T) {
	if os.Getenv("NQNET_STRESS_FULLSTACK") != "1" {
		t.Skip("set NQNET_STRESS_FULLSTACK=1 to run full-stack websocket/udp stress")
	}

	tiers, err := parseStressTierList(os.Getenv("NQNET_STRESS_FULLSTACK_TIERS"), []int{100, 1000, 10000})
	if err != nil {
		t.Fatalf("parse NQNET_STRESS_FULLSTACK_TIERS: %v", err)
	}
	if len(tiers) == 0 {
		t.Fatalf("NQNET_STRESS_FULLSTACK_TIERS resolved to no tiers")
	}

	for _, tier := range tiers {
		tier := tier
		t.Run(fmt.Sprintf("%d_clients", tier), func(t *testing.T) {
			runFullStackStressTier(t, tier)
		})
	}
}

type udpProbeClientMetrics struct {
	sent         int64
	success      int64
	failures     int64
	timeouts     int64
	latencyNanos []int64
}

func spawnStressRelays(clientCount int, alloc *IPAllocator, sessions *SessionRegistry) ([]*Relay, error) {
	relays := make([]*Relay, clientCount)
	if clientCount == 0 {
		return relays, nil
	}

	jobs := make(chan int, clientCount)
	errCh := make(chan error, clientCount)

	workers := clientCount
	if workers > 256 {
		workers = 256
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wg.Done()
			for idx := range jobs {
				sourceIP := fmt.Sprintf("198.51.100.%d", 1+(idx%250))
				sourceKey := fmt.Sprintf("stress-client:%d", idx)

				clientIP, err := alloc.alloc(sourceKey)
				if err != nil {
					errCh <- fmt.Errorf("alloc %d: %w", idx, err)
					continue
				}

				ctx, cancel := context.WithCancel(context.Background())
				relay := &Relay{
					clientIP:  clientIP,
					sourceKey: sourceKey,
					sourceIP:  sourceIP,
					alloc:     alloc,
					sessions:  sessions,
					warnf:     noopLogf,
					debugf:    noopLogf,
					ctx:       ctx,
					cancel:    cancel,
				}
				relay.noteServerRoutePort(26000 + (idx % 8))
				sessions.track(relay)
				relays[idx] = relay
			}
		}()
	}

	for idx := 0; idx < clientCount; idx++ {
		jobs <- idx
	}
	close(jobs)

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			closeRelaysConcurrently(relays)
			return nil, err
		}
	}
	return relays, nil
}

func closeRelaysConcurrently(relays []*Relay) {
	if len(relays) == 0 {
		return
	}

	jobs := make(chan *Relay, len(relays))
	workers := len(relays)
	if workers > 256 {
		workers = 256
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wg.Done()
			for relay := range jobs {
				if relay != nil {
					relay.Close()
				}
			}
		}()
	}

	for _, relay := range relays {
		jobs <- relay
	}
	close(jobs)
	wg.Wait()
}

func parseStressTierList(raw string, defaults []int) ([]int, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		out := make([]int, len(defaults))
		copy(out, defaults)
		return out, nil
	}

	parts := strings.Split(text, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		n, err := strconv.Atoi(token)
		if err != nil {
			return nil, fmt.Errorf("invalid tier %q: %w", token, err)
		}
		if n <= 0 {
			return nil, fmt.Errorf("tier must be > 0, got %d", n)
		}
		out = append(out, n)
	}
	return out, nil
}

func runFullStackStressTier(t *testing.T, clientCount int) {
	t.Helper()

	if softLimit, ok := currentFileDescriptorLimit(); ok {
		// Rough estimate:
		// - one client WS fd
		// - one server-side accepted WS fd
		// - one Relay UDP fd
		// Plus overhead room for Go runtime/http listener and test process itself.
		const safetyOverhead = 1024
		required := uint64(clientCount*3 + safetyOverhead)
		if softLimit < required {
			t.Skipf("tier %d needs ~%d open fds, soft limit is %d", clientCount, required, softLimit)
		}
	}

	dialConcurrency, err := parseStressInt("NQNET_STRESS_FULLSTACK_DIAL_CONCURRENCY", min(clientCount, 1024))
	if err != nil {
		t.Fatalf("parse NQNET_STRESS_FULLSTACK_DIAL_CONCURRENCY: %v", err)
	}
	if dialConcurrency <= 0 {
		t.Fatalf("NQNET_STRESS_FULLSTACK_DIAL_CONCURRENCY must be > 0")
	}

	probeClients, err := parseStressInt("NQNET_STRESS_FULLSTACK_UDP_PROBES", min(clientCount, 128))
	if err != nil {
		t.Fatalf("parse NQNET_STRESS_FULLSTACK_UDP_PROBES: %v", err)
	}
	if probeClients <= 0 {
		t.Fatalf("NQNET_STRESS_FULLSTACK_UDP_PROBES must be > 0")
	}
	if probeClients > clientCount {
		probeClients = clientCount
	}

	trafficConcurrency, err := parseStressInt("NQNET_STRESS_FULLSTACK_TRAFFIC_CONCURRENCY", probeClients)
	if err != nil {
		t.Fatalf("parse NQNET_STRESS_FULLSTACK_TRAFFIC_CONCURRENCY: %v", err)
	}
	if trafficConcurrency <= 0 {
		t.Fatalf("NQNET_STRESS_FULLSTACK_TRAFFIC_CONCURRENCY must be > 0")
	}
	if trafficConcurrency > probeClients {
		trafficConcurrency = probeClients
	}

	trafficDuration, err := parseStressDuration("NQNET_STRESS_FULLSTACK_TRAFFIC_DURATION", 5*time.Second)
	if err != nil {
		t.Fatalf("parse NQNET_STRESS_FULLSTACK_TRAFFIC_DURATION: %v", err)
	}

	udpProbeInterval, err := parseStressDuration("NQNET_STRESS_FULLSTACK_UDP_INTERVAL", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("parse NQNET_STRESS_FULLSTACK_UDP_INTERVAL: %v", err)
	}

	udpProbeTimeout, err := parseStressDuration("NQNET_STRESS_FULLSTACK_UDP_TIMEOUT", 2*time.Second)
	if err != nil {
		t.Fatalf("parse NQNET_STRESS_FULLSTACK_UDP_TIMEOUT: %v", err)
	}

	serverIP := net.ParseIP(DefaultNQServerIP).To4()
	if serverIP == nil {
		t.Fatalf("parse server ip: %q", DefaultNQServerIP)
	}

	alloc := NewIPAllocator(serverIP)
	sessions := NewSessionRegistry()

	echoConn, err := net.ListenUDP("udp4", ServerUDPAddr(serverIP, 0))
	if err != nil {
		t.Fatalf("listen udp echo: %v", err)
	}
	echoAddr, ok := echoConn.LocalAddr().(*net.UDPAddr)
	if !ok || echoAddr == nil || echoAddr.Port <= 0 {
		_ = echoConn.Close()
		t.Fatalf("unexpected echo listen addr: %v", echoConn.LocalAddr())
	}
	echoPort := echoAddr.Port

	echoStop := make(chan struct{})
	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
		runUDPEchoLoop(echoConn, echoStop)
	}()
	t.Cleanup(func() {
		close(echoStop)
		_ = echoConn.Close()
		<-echoDone
	})

	var currentSessions atomic.Int64
	var maxConcurrentSessions atomic.Int64
	var serverErrMu sync.Mutex
	serverErrs := make([]error, 0, 4)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrMu.Lock()
			serverErrs = append(serverErrs, fmt.Errorf("upgrade: %w", err))
			serverErrMu.Unlock()
			return
		}

		clientID := strings.TrimSpace(r.Header.Get("X-Test-Client-ID"))
		if clientID == "" {
			clientID = strings.TrimSpace(r.RemoteAddr)
		}
		sourceKey := "stress-fullstack:" + clientID
		sourceIP := fmt.Sprintf("198.51.100.%d", 1+(len(clientID)%250))

		relay, err := NewRelay(conn, sourceKey, sourceIP, "", false, alloc, sessions, FrameDispatch{}, noopLogf, noopLogf)
		if err != nil {
			serverErrMu.Lock()
			serverErrs = append(serverErrs, fmt.Errorf("new relay: %w", err))
			serverErrMu.Unlock()
			_ = conn.Close()
			return
		}

		count := currentSessions.Add(1)
		for {
			prev := maxConcurrentSessions.Load()
			if count <= prev || maxConcurrentSessions.CompareAndSwap(prev, count) {
				break
			}
		}
		defer currentSessions.Add(-1)

		relay.Run()
	})

	wsServer := httptest.NewServer(mux)
	t.Cleanup(wsServer.Close)
	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http") + "/ws"

	clientErrs := make(chan error, clientCount)
	ready := make(chan struct{}, clientCount)
	runTraffic := make(chan struct{})
	start := make(chan struct{})
	var wg sync.WaitGroup
	connectLatencyNanos := make([]int64, clientCount)
	expectedProbesPerClient := estimateProbeSamplesPerClient(trafficDuration, udpProbeInterval)
	probeResults := make(chan udpProbeClientMetrics, probeClients)
	dialSlots := make(chan struct{}, dialConcurrency)
	trafficSlots := make(chan struct{}, trafficConcurrency)

	dialer := websocket.Dialer{
		HandshakeTimeout:  10 * time.Second,
		ReadBufferSize:    4096,
		WriteBufferSize:   4096,
		Subprotocols:      []string{"binary"},
		EnableCompression: false,
	}

	for idx := 0; idx < clientCount; idx++ {
		idx := idx
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			connectStart := time.Now()
			header := http.Header{}
			header.Set("X-Test-Client-ID", strconv.Itoa(idx))

			var conn *websocket.Conn
			var connectErr error
			for attempt := 1; attempt <= 3; attempt++ {
				dialSlots <- struct{}{}
				c, _, err := dialer.Dial(wsURL, header)
				<-dialSlots
				if err != nil {
					connectErr = fmt.Errorf("dial[%d] attempt %d: %w", idx, attempt, err)
					time.Sleep(time.Duration(attempt*25) * time.Millisecond)
					continue
				}

				if err := c.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
					_ = c.Close()
					connectErr = fmt.Errorf("set read deadline[%d] attempt %d: %w", idx, attempt, err)
					time.Sleep(time.Duration(attempt*25) * time.Millisecond)
					continue
				}
				if err := expectWSIdentityFrame(c, idx); err != nil {
					_ = c.Close()
					connectErr = fmt.Errorf("identity[%d] attempt %d: %w", idx, attempt, err)
					time.Sleep(time.Duration(attempt*25) * time.Millisecond)
					continue
				}
				if err := c.SetReadDeadline(time.Time{}); err != nil {
					_ = c.Close()
					connectErr = fmt.Errorf("clear read deadline[%d] attempt %d: %w", idx, attempt, err)
					time.Sleep(time.Duration(attempt*25) * time.Millisecond)
					continue
				}

				conn = c
				connectErr = nil
				break
			}
			if connectErr != nil {
				clientErrs <- connectErr
				return
			}
			if conn == nil {
				clientErrs <- fmt.Errorf("connect[%d]: unknown failure", idx)
				return
			}
			connectLatency := time.Since(connectStart).Nanoseconds()
			if connectLatency < 1 {
				connectLatency = 1
			}
			connectLatencyNanos[idx] = connectLatency
			defer func() {
				_ = conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
					time.Now().Add(time.Second),
				)
				_ = conn.Close()
			}()

			ready <- struct{}{}

			select {
			case <-runTraffic:
			case <-time.After(30 * time.Second):
				clientErrs <- fmt.Errorf("run traffic gate timeout[%d]", idx)
				return
			}

			if idx >= probeClients {
				return
			}

			udpResponses, udpReadErrs := startUDPEchoResponseReader(conn, idx, echoPort)

			trafficSlots <- struct{}{}
			defer func() {
				<-trafficSlots
			}()

			metrics := udpProbeClientMetrics{
				latencyNanos: make([]int64, 0, expectedProbesPerClient),
			}
			trafficDeadline := time.Now().Add(trafficDuration)
			nextProbeAt := time.Now().Add(initialProbeJitter(idx, udpProbeInterval))
			for probeSeq := 0; ; probeSeq++ {
				if !time.Now().Before(trafficDeadline) {
					break
				}
				if wait := time.Until(nextProbeAt); wait > 0 {
					time.Sleep(wait)
				}
				if !time.Now().Before(trafficDeadline) {
					break
				}

				metrics.sent++
				udpRoundTripLatency, timedOut, err := probeUDPEchoRoundTrip(
					conn,
					idx,
					echoPort,
					probeSeq,
					udpProbeTimeout,
					udpResponses,
					udpReadErrs,
				)
				if err != nil {
					clientErrs <- err
					return
				}
				if timedOut {
					metrics.failures++
					metrics.timeouts++
				} else {
					metrics.success++
					udpRoundTripNanos := udpRoundTripLatency.Nanoseconds()
					if udpRoundTripNanos < 1 {
						udpRoundTripNanos = 1
					}
					metrics.latencyNanos = append(metrics.latencyNanos, udpRoundTripNanos)
				}
				nextProbeAt = nextProbeAt.Add(udpProbeInterval)
			}

			probeResults <- metrics
		}()
	}

	close(start)
	readyDeadline := time.NewTimer(60 * time.Second)
	defer readyDeadline.Stop()
	readyCount := 0
	for readyCount < clientCount {
		select {
		case <-ready:
			readyCount++
		case err := <-clientErrs:
			if err != nil {
				close(runTraffic)
				wg.Wait()
				t.Fatalf("full-stack client setup error: %v", err)
			}
		case <-readyDeadline.C:
			close(runTraffic)
			wg.Wait()
			t.Fatalf("timeout waiting for clients to become ready: ready=%d want=%d", readyCount, clientCount)
		}
	}

	if got := len(sessions.SnapshotAll()); got != clientCount {
		close(runTraffic)
		wg.Wait()
		t.Fatalf("session snapshot at barrier = %d, want %d", got, clientCount)
	}
	// One in-band snapshot verification under full load is enough here.
	for {
		prev := maxConcurrentSessions.Load()
		count := int64(clientCount)
		if count <= prev || maxConcurrentSessions.CompareAndSwap(prev, count) {
			break
		}
	}

	close(runTraffic)
	wg.Wait()
	close(clientErrs)
	close(probeResults)

	for err := range clientErrs {
		if err != nil {
			t.Fatalf("full-stack client error: %v", err)
		}
	}

	serverErrMu.Lock()
	defer serverErrMu.Unlock()
	if len(serverErrs) > 0 {
		t.Fatalf("full-stack server errors: %v", serverErrs[0])
	}

	waitForSessionsToDrain(t, sessions, 10*time.Second)
	connectAvg, connectMax, missingConnectLatencies := summarizeLatencyNanos(connectLatencyNanos)
	if missingConnectLatencies > 0 {
		t.Fatalf("missing connect latency samples: %d", missingConnectLatencies)
	}

	var probeClientResults int
	var udpSent int64
	var udpSuccess int64
	var udpFailures int64
	var udpTimeouts int64
	udpLatencyNanos := make([]int64, 0, probeClients*expectedProbesPerClient)
	for metrics := range probeResults {
		probeClientResults++
		udpSent += metrics.sent
		udpSuccess += metrics.success
		udpFailures += metrics.failures
		udpTimeouts += metrics.timeouts
		udpLatencyNanos = append(udpLatencyNanos, metrics.latencyNanos...)
	}
	if probeClientResults != probeClients {
		t.Fatalf("missing UDP probe client results: got %d, want %d", probeClientResults, probeClients)
	}
	udpLossPercent := 0.0
	if udpSent > 0 {
		udpLossPercent = float64(udpFailures) * 100 / float64(udpSent)
	}
	udpRoundTripAvg, udpRoundTripMax, udpRoundTripP95, udpRoundTripP99 := summarizeLatencyPercentiles(udpLatencyNanos)

	t.Logf(
		"peak concurrent sessions observed: %d (probe_clients=%d, dial_concurrency=%d, traffic_concurrency=%d)",
		maxConcurrentSessions.Load(),
		probeClients,
		dialConcurrency,
		trafficConcurrency,
	)
	t.Logf(
		"connect-to-server latency: avg=%s max=%s samples=%d",
		connectAvg,
		connectMax,
		len(connectLatencyNanos),
	)
	t.Logf(
		"udp playability: duration=%s interval=%s timeout=%s sent=%d success=%d failures=%d loss=%.2f%% timeouts=%d avg=%s max=%s p95=%s p99=%s samples=%d",
		trafficDuration,
		udpProbeInterval,
		udpProbeTimeout,
		udpSent,
		udpSuccess,
		udpFailures,
		udpLossPercent,
		udpTimeouts,
		udpRoundTripAvg,
		udpRoundTripMax,
		udpRoundTripP95,
		udpRoundTripP99,
		len(udpLatencyNanos),
	)
}

func runUDPEchoLoop(conn *net.UDPConn, stop <-chan struct{}) {
	buffer := make([]byte, 2048)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, remote, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				select {
				case <-stop:
					return
				default:
					continue
				}
			}
			return
		}
		if n == 0 || remote == nil {
			continue
		}
		_, _ = conn.WriteToUDP(buffer[:n], remote)
	}
}

func expectWSIdentityFrame(conn *websocket.Conn, idx int) error {
	messageType, frame, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read identity[%d]: %w", idx, err)
	}
	if messageType != websocket.BinaryMessage {
		return fmt.Errorf("identity message type[%d] = %d, want binary", idx, messageType)
	}

	minLen := 2 + len(wsClientIdentityMagic) + 4
	if len(frame) < minLen {
		return fmt.Errorf("identity frame too short[%d]: %d < %d", idx, len(frame), minLen)
	}
	if frame[0] != 0 || frame[1] != 0 {
		return fmt.Errorf("identity frame port[%d] = [%d %d], want [0 0]", idx, frame[0], frame[1])
	}
	if !bytes.Equal(frame[2:2+len(wsClientIdentityMagic)], []byte(wsClientIdentityMagic)) {
		return fmt.Errorf("identity frame magic mismatch[%d]", idx)
	}
	return nil
}

func startUDPEchoResponseReader(conn *websocket.Conn, idx int, echoPort int) (<-chan []byte, <-chan error) {
	responses := make(chan []byte, 8)
	readErrs := make(chan error, 1)
	go func() {
		defer close(responses)
		defer close(readErrs)

		for {
			messageType, frame, err := conn.ReadMessage()
			if err != nil {
				readErrs <- fmt.Errorf("read echo[%d]: %w", idx, err)
				return
			}
			if messageType != websocket.BinaryMessage || len(frame) < 2 {
				continue
			}

			port := int(frame[0])<<8 | int(frame[1])
			if port != echoPort {
				continue
			}

			payload := append([]byte(nil), frame[2:]...)
			responses <- payload
		}
	}()
	return responses, readErrs
}

func probeUDPEchoRoundTrip(
	conn *websocket.Conn,
	idx int,
	echoPort int,
	probeSeq int,
	timeout time.Duration,
	responses <-chan []byte,
	readErrs <-chan error,
) (latency time.Duration, timedOut bool, err error) {
	started := time.Now()
	payload := []byte(fmt.Sprintf("ping-%d-%d", idx, probeSeq))
	if err := conn.WriteMessage(websocket.BinaryMessage, buildWSFrame(echoPort, payload)); err != nil {
		return 0, false, fmt.Errorf("write payload[%d] probe %d: %w", idx, probeSeq, err)
	}

	deadline := started.Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, true, nil
		}

		timer := time.NewTimer(remaining)
		select {
		case readErr, ok := <-readErrs:
			stopTimer(timer)
			if !ok {
				return 0, false, fmt.Errorf("echo reader closed[%d]", idx)
			}
			return 0, false, readErr
		case echoPayload, ok := <-responses:
			stopTimer(timer)
			if !ok {
				return 0, false, fmt.Errorf("echo response channel closed[%d]", idx)
			}
			if !bytes.Equal(echoPayload, payload) {
				continue
			}
			return time.Since(started), false, nil
		case <-timer.C:
			return 0, true, nil
		}
	}
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func initialProbeJitter(clientIndex int, interval time.Duration) time.Duration {
	if interval <= time.Nanosecond {
		return 0
	}
	rng := rand.New(rand.NewSource(int64(clientIndex) + 1))
	return time.Duration(rng.Int63n(interval.Nanoseconds()))
}

func summarizeLatencyNanos(samples []int64) (avg time.Duration, max time.Duration, missing int) {
	if len(samples) == 0 {
		return 0, 0, 0
	}

	var totalNanos int64
	var maxNanos int64
	for _, sample := range samples {
		if sample <= 0 {
			missing++
			continue
		}
		totalNanos += sample
		if sample > maxNanos {
			maxNanos = sample
		}
	}

	valid := len(samples) - missing
	if valid == 0 {
		return 0, 0, missing
	}
	return time.Duration(totalNanos / int64(valid)), time.Duration(maxNanos), missing
}

func summarizeLatencyPercentiles(samples []int64) (avg time.Duration, max time.Duration, p95 time.Duration, p99 time.Duration) {
	if len(samples) == 0 {
		return 0, 0, 0, 0
	}

	var totalNanos int64
	maxNanos := samples[0]
	sorted := append([]int64(nil), samples...)
	for _, sample := range sorted {
		totalNanos += sample
		if sample > maxNanos {
			maxNanos = sample
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})

	return time.Duration(totalNanos / int64(len(sorted))),
		time.Duration(maxNanos),
		time.Duration(percentileNanos(sorted, 95)),
		time.Duration(percentileNanos(sorted, 99))
}

func percentileNanos(sorted []int64, percentile float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if percentile <= 0 {
		return sorted[0]
	}
	if percentile >= 100 {
		return sorted[len(sorted)-1]
	}

	rank := int(math.Ceil((percentile / 100) * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func estimateProbeSamplesPerClient(duration time.Duration, interval time.Duration) int {
	if duration <= 0 || interval <= 0 {
		return 1
	}
	samples := int(duration/interval) + 1
	if samples < 1 {
		return 1
	}
	return samples
}

func waitForSessionsToDrain(t *testing.T, sessions *SessionRegistry, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if len(sessions.SnapshotAll()) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sessions did not drain within %s; remaining=%d", timeout, len(sessions.SnapshotAll()))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func currentFileDescriptorLimit() (uint64, bool) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return 0, false
	}
	return lim.Cur, true
}

func parseStressInt(env string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", env, raw, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s must be > 0, got %d", env, n)
	}
	return n, nil
}

func parseStressDuration(env string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", env, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be > 0, got %s", env, d)
	}
	return d, nil
}
