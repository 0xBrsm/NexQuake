#!/bin/bash
#
# Startup script for WebQuake Gateway
# - Spawns a single NetQuake server (id1)
# - Starts Go HTTP/WebSocket proxy

set -e

QUAKE_DATA_DIR="${QUAKE_DATA_DIR:-/data}"
LOGS_DIR="${LOGS_DIR:-/logs}"
SERVER_BINARY="${SERVER_BINARY:-/apps/nqserver}"
GATEWAY_BINARY="${GATEWAY_BINARY:-/apps/gateway}"
CLIENT_DIR="${CLIENT_DIR:-/apps/nqwasm}"
BASE_PORT=26000

# Ensure variables propagate to the gateway process even when running `start.sh`
# outside Docker (where these may not be exported already).
export QUAKE_DATA_DIR LOGS_DIR SERVER_BINARY GATEWAY_BINARY CLIENT_DIR
export HTTP_PORT="${HTTP_PORT:-7071}"

echo "==================================="
echo "Welcome to WebQuake!"
echo "==================================="
echo "Data Directory: $QUAKE_DATA_DIR"
echo "Logs Directory: $LOGS_DIR"
echo "Gateway Binary: $GATEWAY_BINARY"
echo "Server Binary: $SERVER_BINARY"
echo "Client Directory: $CLIENT_DIR"
echo

# Validate runtime artifacts are present
if [ ! -x "$GATEWAY_BINARY" ]; then
    echo "ERROR: Gateway binary not found or not executable: $GATEWAY_BINARY" >&2
    echo "Contents of /apps:" >&2
    ls -la /apps >&2 || true
    exit 1
fi

if [ ! -x "$SERVER_BINARY" ]; then
    echo "ERROR: Server binary not found or not executable: $SERVER_BINARY" >&2
    echo "Contents of /apps:" >&2
    ls -la /apps >&2 || true
    exit 1
fi

if [ "${DEBUG_STARTUP:-0}" = "1" ]; then
  echo "Artifact fingerprints:"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$GATEWAY_BINARY" "$SERVER_BINARY" 2>/dev/null || true
    if [ -f "$CLIENT_DIR/index.wasm" ]; then
      sha256sum "$CLIENT_DIR/index.wasm" 2>/dev/null || true
    fi
  fi
  if "$GATEWAY_BINARY" --version >/dev/null 2>&1; then
    "$GATEWAY_BINARY" --version || true
  fi
  echo
fi

if [ ! -f "$CLIENT_DIR/index.html" ] && [ ! -f "$CLIENT_DIR/nqwasm/index.html" ]; then
    echo "ERROR: WASM client not found under CLIENT_DIR: $CLIENT_DIR" >&2
    echo "Contents of $CLIENT_DIR:" >&2
    ls -la "$CLIENT_DIR" >&2 || true
    exit 1
fi

# If the release tarball was extracted to /apps (recommended), the client will be at /apps/nqwasm.
# If it was extracted into /apps/nqwasm without strip-components, files may be nested under /apps/nqwasm/nqwasm/.
if [ ! -f "$CLIENT_DIR/index.html" ] && [ -f "$CLIENT_DIR/nqwasm/index.html" ]; then
    echo "Detected nested nqwasm client at: $CLIENT_DIR/nqwasm"
    CLIENT_DIR="$CLIENT_DIR/nqwasm"
    export CLIENT_DIR
    echo "Using Client Directory: $CLIENT_DIR"
    echo
fi

# Create logs directory if it doesn't exist
mkdir -p "$LOGS_DIR"

pick_udp_port() {
    local start_port="$1"
    local max_tries="${2:-20}"

    if ! command -v python3 >/dev/null 2>&1; then
        echo "$start_port"
        return 0
    fi

    if [ "$max_tries" -lt 1 ]; then
        max_tries=1
    fi

    for i in $(seq 0 $((max_tries - 1))); do
        local port=$((start_port + i))
        if python3 - "$port" <<'PY' >/dev/null 2>&1; then
import socket, sys
port = int(sys.argv[1])
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
try:
    s.bind(("127.0.0.1", port))
finally:
    s.close()
PY
            echo "$port"
            return 0
        fi
    done

    echo "ERROR: Could not find an open UDP port after ${max_tries} attempts." >&2
    return 1
}

# Function to spawn a server for a mod
spawn_server() {
    local mod_name="$1"
    local port="$2"
    local log_dir="$LOGS_DIR/$mod_name"
    local log_file="$log_dir/server.log"
    local pid_file="$log_dir/server.pid"

    echo "Starting NetQuake server for mod: $mod_name on port $port"

    # Create mod-specific log directory
    mkdir -p "$log_dir"

    # Stop any previously-started server recorded in this log directory.
    if [ -f "$pid_file" ]; then
        old_pid="$(cat "$pid_file" 2>/dev/null || true)"
        if [ -n "$old_pid" ] && kill -0 "$old_pid" >/dev/null 2>&1; then
            echo "  Stopping old server PID: $old_pid"
            kill "$old_pid" >/dev/null 2>&1 || true
            sleep 0.05
        fi
    fi

    # Start server in background
    # -dedicated: Run as dedicated server
    # -game: Specify mod directory
    # -port: UDP port
    # +maxplayers: Max players (16)
    cd "$QUAKE_DATA_DIR"

    # When stdout is redirected to a file, libc can fully-buffer output; this
    # makes server.log appear empty for a while even though the process is alive.
    # Use stdbuf when available to force line buffering.
    prefix=()
    if command -v stdbuf >/dev/null 2>&1; then
        prefix=(stdbuf -oL -eL)
    fi

    "${prefix[@]}" "$SERVER_BINARY" \
        -dedicated \
        -game "$mod_name" \
        -port "$port" \
        >"$log_file" 2>&1 &

    local pid=$!
    echo "$pid" > "$pid_file"
    echo "  Started server PID: $pid"

    # Quick sanity check: if the process exited immediately, surface its log.
    sleep 0.05
    if ! kill -0 "$pid" >/dev/null 2>&1; then
        echo "  ERROR: server process exited immediately (pid=$pid)" >&2
        if [ -f "$log_file" ]; then
            echo "  Last 200 lines of $log_file:" >&2
            tail -n 200 "$log_file" >&2 || true
        fi
        return 1
    fi

    if [ ! -s "$log_file" ]; then
        echo "  Note: $log_file is currently empty (stdout may still be buffering)."
    fi
}

# Start id1 only (vanilla).
server_port="$(pick_udp_port "$BASE_PORT")"
export UDP_SERVER_ADDR="127.0.0.1:${server_port}"
spawn_server "id1" "$server_port"

echo
echo "All servers started!"
echo

# Give servers a moment to initialize
sleep 2

# Start Go proxy in foreground
echo "Starting Gateway..."
echo
exec "$GATEWAY_BINARY"
