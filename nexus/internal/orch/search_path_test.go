package orch

import (
	"slices"
	"testing"
)

func TestResolveManifestGameDirs_UsesServerSearchPath(t *testing.T) {
	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(serverLaunch{Slot: 0})
	mgr.UpdatePort(rec, 26000)
	mgr.UpdateSearchPath(rec, []string{"ctf", "id1"})

	got := mgr.ResolveManifestGameDirs("ctf")
	want := []string{"ctf", "id1"}
	if !slices.Equal(got, want) {
		t.Fatalf("expected game dirs %v, got %v", want, got)
	}
}

func TestResolveManifestGameDirs_UsesMatchingSuffixWhenRequestedDirIsNotActive(t *testing.T) {
	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec := mgr.RegisterServerLaunch(serverLaunch{Slot: 0})
	mgr.UpdatePort(rec, 26000)
	mgr.UpdateSearchPath(rec, []string{"arena", "rogue", "id1"})

	got := mgr.ResolveManifestGameDirs("rogue")
	want := []string{"rogue", "id1"}
	if !slices.Equal(got, want) {
		t.Fatalf("expected game dirs %v, got %v", want, got)
	}
}

func TestResolveManifestGameDirs_IncludesAllFallbackDirsFromMatchingSearchPaths(t *testing.T) {
	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	rec0 := mgr.RegisterServerLaunch(serverLaunch{Slot: 0})
	mgr.UpdatePort(rec0, 26000)
	mgr.UpdateSearchPath(rec0, []string{"arena", "id1"})

	rec1 := mgr.RegisterServerLaunch(serverLaunch{Slot: 1})
	mgr.UpdatePort(rec1, 26001)
	mgr.UpdateSearchPath(rec1, []string{"arena", "rogue", "id1"})

	got := mgr.ResolveManifestGameDirs("arena")
	if len(got) != 3 {
		t.Fatalf("expected 3 game dirs, got %v", got)
	}
	if got[0] != "arena" {
		t.Fatalf("expected requested game dir first, got %v", got)
	}
	if !slices.Contains(got, "id1") || !slices.Contains(got, "rogue") {
		t.Fatalf("expected merged fallback dirs [id1 rogue], got %v", got)
	}
}

func TestResolveManifestGameDirs_FallsBackToRequestedGameDirWithoutServerPath(t *testing.T) {
	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)

	got := mgr.ResolveManifestGameDirs("id1")
	want := []string{"id1"}
	if !slices.Equal(got, want) {
		t.Fatalf("expected game dirs %v, got %v", want, got)
	}
}
