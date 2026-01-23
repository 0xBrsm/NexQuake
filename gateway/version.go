package main

import (
	"encoding/json"
	"net/http"
	"runtime"
	"time"
)

// These are set at build time via -ldflags.
// Example:
//
//	-X main.gitSHA=$GITHUB_SHA -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)
var (
	gitSHA    = "dev"
	buildTime = ""
)

type versionInfo struct {
	GitSHA    string `json:"git_sha"`
	BuildTime string `json:"build_time,omitempty"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

func currentVersionInfo() versionInfo {
	bt := buildTime
	if bt == "" {
		bt = time.Now().UTC().Format(time.RFC3339)
	}
	return versionInfo{
		GitSHA:    gitSHA,
		BuildTime: bt,
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
	}
}

func writeVersionJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(currentVersionInfo())
}
