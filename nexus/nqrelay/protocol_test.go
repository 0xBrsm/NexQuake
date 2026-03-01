package nqrelay

import (
	"context"
	"testing"
)

func TestBuildWSFrame(t *testing.T) {
	frame := buildWSFrame(26000, []byte{1, 2, 3})
	if len(frame) != 5 {
		t.Fatalf("buildWSFrame() len = %d, want 5", len(frame))
	}
	if frame[0] != byte(26000>>8) || frame[1] != byte(26000&0xff) {
		t.Fatalf("unexpected port header: [%d %d]", frame[0], frame[1])
	}
	if frame[2] != 1 || frame[3] != 2 || frame[4] != 3 {
		t.Fatalf("unexpected payload bytes: %v", frame[2:])
	}

	if frame := buildWSFrame(-1, nil); frame != nil {
		t.Fatalf("buildWSFrame(-1) should be nil")
	}
	if frame := buildWSFrame(70000, nil); frame != nil {
		t.Fatalf("buildWSFrame(70000) should be nil")
	}
}

func TestDecodeWSFrame(t *testing.T) {
	if _, _, ok := decodeWSFrame([]byte{1}); ok {
		t.Fatalf("decodeWSFrame() should reject short packets")
	}

	frame := buildWSFrame(26000, []byte{9, 8, 7})
	port, payload, ok := decodeWSFrame(frame)
	if !ok {
		t.Fatalf("decodeWSFrame() returned ok=false for valid frame")
	}
	if port != 26000 {
		t.Fatalf("decoded port = %d, want 26000", port)
	}
	if string(payload) != string([]byte{9, 8, 7}) {
		t.Fatalf("decoded payload = %v, want [9 8 7]", payload)
	}
}

func TestBuildWSClientIdentityFrame(t *testing.T) {
	if frame := buildWSClientIdentityFrame([4]byte{}); frame != nil {
		t.Fatalf("expected nil identity frame for zero client ip")
	}

	frame := buildWSClientIdentityFrame([4]byte{127, 100, 10, 1})
	if len(frame) != 2+len(wsClientIdentityMagic)+4 {
		t.Fatalf("identity frame len = %d", len(frame))
	}
	if frame[0] != byte(controlPort>>8) || frame[1] != byte(controlPort) {
		t.Fatalf("identity frame port header = [%d %d], want control port", frame[0], frame[1])
	}
	if string(frame[2:2+len(wsClientIdentityMagic)]) != wsClientIdentityMagic {
		t.Fatalf("identity frame magic mismatch")
	}
}

func TestRelayHandleWSFrame_ControlDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	called := false
	r := &Relay{
		wsTx:   make(chan []byte, 1),
		ctx:    ctx,
		cancel: cancel,
		warnf:  noopLogf,
		debugf: noopLogf,
		dispatch: FrameDispatch{
			HandleControlFrame: func(relay *Relay, payload []byte) []byte {
				called = true
				if string(payload) != "ping" {
					t.Fatalf("control payload = %q, want %q", string(payload), "ping")
				}
				return []byte("pong")
			},
		},
	}

	r.handleWSFrame(buildWSFrame(controlPort, []byte("ping")))
	if !called {
		t.Fatalf("HandleControlFrame callback was not called")
	}

	select {
	case frame := <-r.wsTx:
		if frame[0] != byte(controlPort>>8) || frame[1] != byte(controlPort) {
			t.Fatalf("response frame routed to non-zero port: [%d %d]", frame[0], frame[1])
		}
		if string(frame[2:]) != "pong" {
			t.Fatalf("response payload = %q, want %q", string(frame[2:]), "pong")
		}
	default:
		t.Fatalf("expected control response frame")
	}
}

func TestRelayHandleWSFrame_DropsDisallowedPort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := &Relay{
		ctx:    ctx,
		cancel: cancel,
		warnf:  noopLogf,
		debugf: noopLogf,
		dispatch: FrameDispatch{
			IsAllowedPort: func(port int) bool { return false },
		},
	}

	r.handleWSFrame(buildWSFrame(26000, []byte{1}))
	if got := r.activeServerPort(); got != 0 {
		t.Fatalf("activeServerPort = %d, want 0 after disallowed frame", got)
	}
}

