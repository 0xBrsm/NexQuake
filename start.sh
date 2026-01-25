#!/bin/bash
#
# Startup script for WebQuake Gateway
# - Spawns NetQuake servers for each mod in /data
# - Starts Go HTTP/WebSocket proxy

set -e

QUAKE_DATA_DIR="${QUAKE_DATA_DIR:-/data}"
LOGS_DIR="${LOGS_DIR:-/logs}"
SERVER_BINARY="${SERVER_BINARY:-/apps/nqserver}"
GATEWAY_BINARY="${GATEWAY_BINARY:-/apps/gateway}"
CLIENT_DIR="${CLIENT_DIR:-/apps/nqwasm}"

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

# Function to spawn a server for a mod
spawn_server() {
    local mod_name="$1"
    local bind_addr="$2"
    local server_id="$3"
    local log_dir="$LOGS_DIR/$mod_name"
    local log_file="$log_dir/server.log"
    local pid_file="$log_dir/server.pid"

    # Ensure each server reports a unique hostname so the in-game server browser
    # (hostcache) can disambiguate entries and allow `connect <hostname>`.
    local id_str="$server_id"
    local max_prefix=$((15 - 1 - ${#id_str}))
    if [ "$max_prefix" -lt 1 ]; then
        max_prefix=1
    fi
    local host_prefix="${mod_name:0:$max_prefix}"
    local server_hostname="${host_prefix}-${id_str}"

    echo "Starting NetQuake server for mod: $mod_name on ${bind_addr}:26000 (hostname: ${server_hostname})"

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
    # -ip: Bind to specific IP (127.255.255.x for multi-server, .255 is broadcast)
    cd "$QUAKE_DATA_DIR"

    # When stdout is redirected to a file, libc can fully-buffer output; this
    # makes server.log appear empty for a while even though the process is alive.
    # Use stdbuf when available to force line buffering.
    cmd_prefix=()
    if command -v stdbuf >/dev/null 2>&1; then
        cmd_prefix=(stdbuf -oL -eL)
    fi

    "${cmd_prefix[@]}" "$SERVER_BINARY" \
        -dedicated \
        -game "$mod_name" \
        -ip "$bind_addr" \
        +hostname "$server_hostname" \
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

# Enumerate mods and spawn a server for each
server_id=1
server_count=0

for mod_dir in "$QUAKE_DATA_DIR"/*; do
    if [ ! -d "$mod_dir" ]; then
        continue
    fi

    mod_name="$(basename "$mod_dir")"

    # Skip if no pak files or progs.dat found (not a valid mod)
    if [ ! -f "$mod_dir/pak0.pak" ] && [ ! -f "$mod_dir/progs.dat" ]; then
        echo "Skipping $mod_name (no pak files or progs.dat found)"
        continue
    fi

    # Bind to 127.255.255.x where x is the server_id (1-254)
    # 127.255.255.255 is the broadcast address for this subnet
    bind_addr="127.255.255.${server_id}"

    spawn_server "$mod_name" "$bind_addr" "$server_id"

    server_id=$((server_id + 1))
    server_count=$((server_count + 1))

    # Limit to 254 servers (127.255.255.1 through 127.255.255.254)
    if [ "$server_id" -gt 254 ]; then
        echo "Warning: Maximum of 254 servers reached, skipping remaining mods"
        break
    fi
done

if [ "$server_count" -eq 0 ]; then
    echo "ERROR: No valid mods found in $QUAKE_DATA_DIR" >&2
    echo "Expected directory structure: /data/modname/{pak0.pak or progs.dat}" >&2
    exit 1
fi

echo
echo "Started $server_count server(s)!"
echo

# Help the gateway server-info cache poll only active server IDs.
export WEBQUAKE_MAX_SERVER_ID="$server_count"

# Give servers a moment to initialize
sleep 2

# Start Go proxy in foreground
echo "Starting Gateway..."
echo
exec "$GATEWAY_BINARY"
