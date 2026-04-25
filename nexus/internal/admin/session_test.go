package admin

import "testing"

func TestRegistry_AttachChannelSnapshotsAndBanTargets(t *testing.T) {
	reg := NewSessionRegistry()
	s := reg.Create("198.51.100.10", "player@example.com", false)
	reg.AttachChannel(s, &mockChannel{
		nqip:      "127.100.10.1",
		clientIP:  [4]byte{127, 100, 10, 1},
		sourceKey: "ip:198.51.100.10",
		port:      26000,
	})

	s.PromoteAdmin()

	snaps := reg.SnapshotAll()
	if len(snaps) != 1 {
		t.Fatalf("expected one snapshot, got %d", len(snaps))
	}
	if snaps[0].VirtualIP != "127.100.10.1" || snaps[0].ActiveServerPort != 26000 {
		t.Fatalf("unexpected snapshot route info: %+v", snaps[0])
	}
	if !snaps[0].IsAdmin {
		t.Fatalf("expected promoted admin snapshot, got %+v", snaps[0])
	}

	sessions, targets := reg.SnapshotByVirtualIP("127.100.10.1")
	if len(sessions) != 1 {
		t.Fatalf("expected one session lookup, got %d", len(sessions))
	}
	if sessions[0] != s {
		t.Fatalf("expected snapshot lookup to return original session")
	}
	if len(targets) != 1 || targets[0] != (BanTarget{Port: 26000, VirtualIP: "127.100.10.1"}) {
		t.Fatalf("unexpected ban targets: %+v", targets)
	}
}
