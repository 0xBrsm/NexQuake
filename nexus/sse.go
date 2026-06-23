package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
)

// stateHub is the general "something changed" channel (GET /events). It polls a
// snapshot builder, diffs the result as opaque bytes, and signals subscribers on
// any change; each subscriber re-reads the current snapshot on signal, so
// coalescing is automatic (a slow reader converges on the latest — snapshots are
// full and idempotent, not deltas). It is source-agnostic: today the snapshot
// carries the server list and the manifest generation; new change-sources are
// added as fields in buildStateSnapshot, not as new streams.
type stateHub struct {
	build func() []byte

	mu   sync.Mutex
	last []byte
	subs map[chan struct{}]struct{}
}

func newStateHub(build func() []byte) *stateHub {
	return &stateHub{build: build, subs: make(map[chan struct{}]struct{})}
}

// subscribe registers a change-signal channel and returns it with an unsubscribe
// func. The channel has capacity 1 and carries a coalesced "snapshot changed"
// signal, not the payload itself.
func (h *stateHub) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// snapshot returns the most recent snapshot, falling back to a fresh build when
// the hub loop hasn't produced one yet (e.g. a subscriber connecting at boot).
func (h *stateHub) snapshot() []byte {
	h.mu.Lock()
	last := h.last
	h.mu.Unlock()
	if last == nil {
		return h.build()
	}
	return last
}

// run polls the builder on interval and signals subscribers when the snapshot
// changes. It skips building entirely when no one is listening, so an idle
// browser-less stack does no work. Returns when ctx is cancelled.
func (h *stateHub) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.mu.Lock()
			hasSubs := len(h.subs) > 0
			h.mu.Unlock()
			if !hasSubs {
				continue
			}
			snap := h.build()
			h.mu.Lock()
			changed := !bytes.Equal(snap, h.last)
			h.last = snap
			if changed {
				for ch := range h.subs {
					select {
					case ch <- struct{}{}:
					default: // already signalled; coalesce
					}
				}
			}
			h.mu.Unlock()
		}
	}
}

// buildStateSnapshot is the state channel's payload: the live server list plus
// the client-asset manifest generation. The hub diffs it as opaque bytes, so a
// server-list change OR a manifest change wakes subscribers on the one
// connection. Clients update their hostcache from `servers` and, when
// `manifestGen` changes, refetch /gamedir. See DEC-021.
func (app *nexusApp) buildStateSnapshot() []byte {
	data, _ := json.Marshal(struct {
		Servers     []orch.SlistEntry `json:"servers"`
		ManifestGen string            `json:"manifestGen"`
	}{
		Servers:     app.serverMgr.SlistEntries(),
		ManifestGen: app.assetServer.ManifestGeneration(),
	})
	return data
}

// handleEvents streams the state channel over SSE (GET /events). The route is
// already behind HTTPGate (blocklist drop), the same gate every endpoint uses;
// the same-origin EventSource carries the session cookie.
func (app *nexusApp) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // defeat proxy buffering of the stream

	signal, unsubscribe := app.stateHub.subscribe()
	defer unsubscribe()

	// Initial snapshot so a fresh subscriber paints immediately (and a reconnect
	// re-delivers the current state, which is how a missed change self-heals).
	writeStateEvent(w, app.stateHub.snapshot())
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-signal:
			writeStateEvent(w, app.stateHub.snapshot())
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// writeStateEvent emits one `state` SSE event carrying a JSON snapshot
// (server list + manifest generation).
func writeStateEvent(w io.Writer, data []byte) {
	_, _ = io.WriteString(w, "event: state\ndata: ")
	_, _ = w.Write(data)
	_, _ = io.WriteString(w, "\n\n")
}
