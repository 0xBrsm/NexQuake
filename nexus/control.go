package main

import (
	"strings"

	"github.com/0xBrsm/NexQuake/nexus/internal/admin"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
	"github.com/0xBrsm/NexQuake/nexus/nqrelay"
)

// buildFrameDispatch constructs the FrameDispatch for a relay session.
// It routes slist requests to the server manager and all other port-0
// frames to the admin subsystem.
func (app *nexusApp) buildFrameDispatch(userIdentity string) nqrelay.FrameDispatch {
	return nqrelay.FrameDispatch{
		HandleControlFrame: func(relay *nqrelay.Relay, payload []byte) []byte {
			if orch.IsSlistRequest(payload) {
				return app.serverMgr.BuildSlistResponse()
			}
			admin.HandleAdminFrameWithIdentityAndPromotionHook(relay, payload, app.auth, app.adminEnv, userIdentity, func(r admin.Session) {
				source := strings.TrimSpace(r.SourceIP())
				if source == "" {
					source = "unknown"
				}
				infof("Admin promoted: source=%s key=%s nqip=%s", source, r.SourceKey(), r.VirtualClientIP())
			})
			return nil
		},
		// IsAllowedPort left nil — contained environment, no port gating needed.
	}
}

// buildAdminEnv constructs the admin.Env, wiring server manager and session
// registry capabilities via conversion closures that translate between
// orch/nqrelay types and admin-local types.
func buildAdminEnv(serverMgr *orch.ServerManager, sessionReg *nqrelay.SessionRegistry, ipAlloc *nqrelay.IPAllocator) *admin.Env {
	return &admin.Env{
		ServerSnapshots: func() []admin.ServerInfo {
			snaps := serverMgr.Snapshots()
			out := make([]admin.ServerInfo, len(snaps))
			for i, s := range snaps {
				out[i] = admin.ServerInfo{
					Hostname:   s.Hostname,
					ListenPort: s.ListenPort,
					GameDir:    s.GameDir,
					Players:    int(s.Players),
					MaxPlayers: int(s.MaxPlayers),
					State:      s.State,
				}
			}
			return out
		},
		StartServer:         serverMgr.StartServer,
		StartServersAll:     serverMgr.StartServersAll,
		StopServer:          serverMgr.StopServer,
		StopServersAll:      serverMgr.StopServersAll,
		RestartServer:       serverMgr.RestartServer,
		RestartServersAll:   serverMgr.RestartServersAll,
		RemoveServer:        serverMgr.RemoveServer,
		LaunchServer:        serverMgr.LaunchServer,
		ExecServerCmd:       serverMgr.ExecServerCmd,
		IsManagedListenPort: serverMgr.IsManagedListenPort,
		TailNexusLog:        tailNexusLogLines,
		Auditf:              auditf,
		SessionSnapshots: func() []admin.SessionInfo {
			snaps := sessionReg.SnapshotAll()
			out := make([]admin.SessionInfo, len(snaps))
			for i, s := range snaps {
				out[i] = admin.SessionInfo{
					VirtualIP:        s.VirtualIP,
					SourceIP:         s.SourceIP,
					UserID:           s.UserID,
					IsAdmin:          s.IsAdmin,
					ActiveServerPort: s.ActiveServerPort,
				}
			}
			return out
		},
		SnapshotByVIP: func(vip string) ([]admin.Session, []admin.BanTarget) {
			relays, targets := sessionReg.SnapshotByVirtualIP(vip)
			sessions := make([]admin.Session, len(relays))
			for i, r := range relays {
				sessions[i] = r
			}
			banTargets := make([]admin.BanTarget, len(targets))
			for i, t := range targets {
				banTargets[i] = admin.BanTarget{Port: t.Port, VirtualIP: t.VirtualIP}
			}
			return sessions, banTargets
		},
		ReserveAndBlock: ipAlloc.ReserveAndBlock,
	}
}
