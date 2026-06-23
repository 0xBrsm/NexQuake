package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakeStateSource struct{ payload atomic.Value }

func (f *fakeStateSource) set(s string) { f.payload.Store([]byte(s)) }
func (f *fakeStateSource) build() []byte {
	if v, ok := f.payload.Load().([]byte); ok {
		return v
	}
	return []byte(`{"servers":[]}`)
}

func TestStateHubSignalsOnChangeOnly(t *testing.T) {
	src := &fakeStateSource{}
	src.set(`{"servers":[{"port":26000}]}`)
	h := newStateHub(src.build)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.run(ctx, 10*time.Millisecond)

	sig, unsub := h.subscribe()
	defer unsub()

	// First tick after subscribe: snapshot differs from nil last → one signal.
	select {
	case <-sig:
	case <-time.After(time.Second):
		t.Fatal("expected initial change signal")
	}

	// Steady state (no change): no further signal.
	select {
	case <-sig:
		t.Fatal("unexpected signal with no change")
	case <-time.After(60 * time.Millisecond):
	}

	// Change the snapshot → exactly one (coalesced) signal.
	src.set(`{"servers":[{"port":26001}]}`)
	select {
	case <-sig:
	case <-time.After(time.Second):
		t.Fatal("expected signal after change")
	}

	if got := string(h.snapshot()); got != `{"servers":[{"port":26001}]}` {
		t.Fatalf("snapshot not updated: %s", got)
	}
}

func TestStateHubIdleWhenNoSubscribers(t *testing.T) {
	src := &fakeStateSource{}
	src.set(`{"servers":[]}`)
	h := newStateHub(src.build)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.run(ctx, 5*time.Millisecond)

	// With no subscribers, run() must not populate last (it skips building).
	time.Sleep(40 * time.Millisecond)
	h.mu.Lock()
	last := h.last
	h.mu.Unlock()
	if last != nil {
		t.Fatal("hub built a snapshot with no subscribers")
	}
}
