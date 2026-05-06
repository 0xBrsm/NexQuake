package trunk

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTransport records writes and signals when its xport-level close fires.
// Reads block on readCh until Close.
type fakeTransport struct {
	mu       sync.Mutex
	writes   [][]byte
	pings    atomic.Int32
	closed   atomic.Bool
	readCh   chan []byte
	closedAt time.Time

	// writeBlock optionally blocks WriteFrame until released; lets tests
	// inject latency on the drain path.
	writeBlock <-chan struct{}
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{readCh: make(chan []byte)}
}

func (f *fakeTransport) Name() string { return "fake" }

func (f *fakeTransport) ReadFrame() ([]byte, error) {
	b, ok := <-f.readCh
	if !ok {
		return nil, errors.New("transport closed")
	}
	return b, nil
}

func (f *fakeTransport) WriteFrame(b []byte) error {
	if f.writeBlock != nil {
		<-f.writeBlock
	}
	if f.closed.Load() {
		return errors.New("transport closed")
	}
	f.mu.Lock()
	f.writes = append(f.writes, append([]byte(nil), b...))
	f.mu.Unlock()
	return nil
}

func (f *fakeTransport) Ping() error {
	f.pings.Add(1)
	return nil
}

func (f *fakeTransport) Close() error {
	if f.closed.Swap(true) {
		return nil
	}
	f.closedAt = time.Now()
	close(f.readCh)
	return nil
}

func (f *fakeTransport) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

func (f *fakeTransport) writeAt(i int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.writes) {
		return nil
	}
	return f.writes[i]
}

// --- End() drains queued frames before closing transport -------------------

func TestEnd_DrainsQueuedFramesBeforeClose(t *testing.T) {
	tr := newFakeTransport()
	tk := New()
	sess, err := tk.NewSession(tr, "1.2.3.4")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	runDone := make(chan struct{})
	go func() {
		sess.Run()
		close(runDone)
	}()

	// Wait for Run() to mark the session running so writeLoop is live.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !sess.runStarted.Load() {
		time.Sleep(time.Millisecond)
	}
	if !sess.runStarted.Load() {
		t.Fatal("Run did not start")
	}

	// Queue an arbitrary control payload, then evict.
	payload := []byte("queued-during-eviction")
	if err := sess.SendControl(payload); err != nil {
		t.Fatalf("SendControl: %v", err)
	}
	tk.EndSession(sess.virtualIP)

	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after EndSession")
	}

	if !tr.closed.Load() {
		t.Fatal("transport not closed after End")
	}
	if tr.writeCount() == 0 {
		t.Fatal("expected drain to deliver the queued frame; got zero writes")
	}

	// The queued payload should have been flushed before Close.
	last := tr.writeAt(tr.writeCount() - 1)
	want := append([]byte{0, 0}, payload...) // 2-byte control-port header + payload
	if !bytes.Equal(last, want) {
		t.Fatalf("drained frame = %q, want %q", last, want)
	}
}

// --- Trunk.SessionByVirtualIP ----------------------------------------------

func TestSessionByVirtualIP(t *testing.T) {
	tk := New()
	tr := newFakeTransport()
	sess, err := tk.NewSession(tr, "203.0.113.5")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.End()

	got := tk.SessionByVirtualIP(sess.virtualIP)
	if got != sess {
		t.Fatalf("SessionByVirtualIP returned %p, want %p", got, sess)
	}

	if tk.SessionByVirtualIP([4]byte{}) != nil {
		t.Fatal("zero VIP should return nil")
	}
	if tk.SessionByVirtualIP([4]byte{127, 99, 99, 99}) != nil {
		t.Fatal("unknown VIP should return nil")
	}
}
