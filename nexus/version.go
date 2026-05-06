package main

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"
)

// These are set at build time via -ldflags.
// Example:
//
//	-X github.com/0xBrsm/NexQuake/nexus.gitSHA=$GITHUB_SHA -X github.com/0xBrsm/NexQuake/nexus.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)
var (
	gitSHA    = "dev"
	buildTime = ""
)

// versionInfo carries build-time metadata returned by the /health endpoint
// and the --version CLI command.
type versionInfo struct {
	GitSHA    string `json:"git_sha"`
	BuildTime string `json:"build_time,omitempty"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

// currentVersionInfo returns build metadata. When buildTime is not set via
// -ldflags it defaults to the current UTC time (dev builds only).
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

// handleCLI processes CLI-only sub-commands (--version, --healthcheck).
// It returns (true, exitCode) when a sub-command was matched and the process
// should exit; (false, 0) means normal server startup should proceed.
func handleCLI(args []string) (handled bool, exitCode int) {
	if len(args) == 0 {
		return false, 0
	}

	switch args[0] {
	case "--version", "version":
		// Keep this simple so it's usable inside minimal runtime images.
		v := currentVersionInfo()
		fmt.Printf("nexquake-nexus git_sha=%s build_time=%s go=%s %s/%s\n",
			v.GitSHA,
			v.BuildTime,
			v.GoVersion,
			v.GOOS,
			v.GOARCH,
		)
		return true, 0
	case "--healthcheck", "healthcheck":
		// Used by Docker/compose healthchecks. Do not require curl/wget/bash in the image.
		httpPort := getEnv("HTTP_PORT", "1337")
		if err := runHealthcheck(httpPort); err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
			return true, 1
		}
		return true, 0
	default:
		return false, 0
	}
}

// runHealthcheck performs an HTTP GET against the local /health endpoint.
// Designed for Docker/compose healthchecks — avoids needing curl or bash.
func runHealthcheck(httpPort string) error {
	url := fmt.Sprintf("http://127.0.0.1:%s/health", httpPort)

	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}

	return nil
}
