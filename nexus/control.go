package main

import (
	"github.com/0xBrsm/NexQuake/nexus/internal/admin"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
	"github.com/0xBrsm/NexQuake/nexus/internal/session"
	"github.com/0xBrsm/NexQuake/nexus/nqrelay"
)

// buildFrameDispatch wires the relay's port-0 control channel: slist
// requests only. Other port-0 payloads are silently dropped; admin rcon
// is served separately by POST /rcon.
func (app *nexusApp) buildFrameDispatch(_ *session.Session) nqrelay.FrameDispatch {
	return nqrelay.FrameDispatch{
		HandleControlFrame: func(_ *nqrelay.Relay, payload []byte) []byte {
			if orch.IsSlistRequest(payload) {
				return app.serverMgr.BuildSlistResponse()
			}
			return nil
		},
		// IsAllowedPort left nil — contained environment, no port gating needed.
	}
}

func convertServerSnapshot(s orch.ServerSnapshot) admin.ServerInfo {
	return admin.ServerInfo{
		Line:          s.Line,
		Hostname:      s.Hostname,
		MapName:       s.MapName,
		CandidatePort: s.CandidatePort,
		ListenPort:    s.ListenPort,
		GameDir:       s.GameDir,
		Players:       int(s.Players),
		MaxPlayers:    int(s.MaxPlayers),
		Instances:     int(s.Instances),
		State:         s.State,
	}
}

func convertServerSnapshots(snaps []orch.ServerSnapshot) []admin.ServerInfo {
	out := make([]admin.ServerInfo, len(snaps))
	for i, s := range snaps {
		out[i] = convertServerSnapshot(s)
	}
	return out
}

// buildAdminEnv constructs the admin.Env, wiring server manager and session
// registry capabilities without additional adapter types.
func buildAdminEnv(serverMgr *orch.ServerManager, sessionReg *session.Registry, ipAlloc *nqrelay.NQIPAllocator) *admin.Env {
	return &admin.Env{
		ServerSnapshots: func() []admin.ServerInfo {
			return convertServerSnapshots(serverMgr.Snapshots())
		},
		InstanceSnapshots: func(target int) ([]admin.ServerInfo, error) {
			snaps, err := serverMgr.InstanceSnapshots(target)
			if err != nil {
				return nil, err
			}
			return convertServerSnapshots(snaps), nil
		},
		StartServer:         serverMgr.StartServer,
		StartServersAll:     serverMgr.StartServersAll,
		StopServer:          serverMgr.StopServer,
		StopServersAll:      serverMgr.StopServersAll,
		RestartServer:       serverMgr.RestartServer,
		RestartServersAll:   serverMgr.RestartServersAll,
		RemoveServer:        serverMgr.RemoveServer,
		LaunchServer:        serverMgr.LaunchServer,
		DispatchInstanceCmd:   serverMgr.DispatchInstanceCmd,
		IsManagedListenPort: serverMgr.IsManagedListenPort,
		TailNexusLog:        tailNexusLogLines,
		Auditf:              auditf,
		SessionSnapshots:    sessionReg.SnapshotAll,
		SnapshotByNQIP:       sessionReg.SnapshotByNQIP,
		ReserveAndBlock:     ipAlloc.ReserveAndBlock,
	}
}
