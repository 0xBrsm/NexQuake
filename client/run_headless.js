#!/usr/bin/env node
/**
 * run_headless.js - Node.js runner for headless WebQuake client
 *
 * Usage:
 *   node run_headless.js [options] [-- quake_args...]
 *
 * Options:
 *   --nexus-url URL     Nexus server URL (default: http://localhost:7071)
 *   --mod NAME          Mod/game directory (default: id1)
 *   --verbose           Enable verbose logging
 *   --exec CMD          Execute Quake command after startup
 *   --timeout MS        Exit after MS milliseconds (for scripted tests)
 *   --help              Show this help
 *
 * Examples:
 *   node run_headless.js --exec "connect 127.255.255.1"
 *   node run_headless.js --nexus-url http://localhost:7071 --timeout 10000
 *   node run_headless.js -- -nosound -window
 */

const path = require('path');
const fs = require('fs');

// Parse command line arguments
function parseArgs() {
  const args = {
    nexusUrl: process.env.NEXUS_URL || 'http://localhost:7071',
    mod: 'id1',
    verbose: false,
    exec: [],
    timeout: 0,
    quakeArgs: []
  };

  const argv = process.argv.slice(2);
  let i = 0;
  let seenDoubleDash = false;

  while (i < argv.length) {
    const arg = argv[i];

    if (seenDoubleDash) {
      args.quakeArgs.push(arg);
      i++;
      continue;
    }

    if (arg === '--') {
      seenDoubleDash = true;
      i++;
      continue;
    }

    if (arg === '--nexus-url' && i + 1 < argv.length) {
      args.nexusUrl = argv[++i];
    } else if (arg === '--mod' && i + 1 < argv.length) {
      args.mod = argv[++i];
    } else if (arg === '--verbose' || arg === '-v') {
      args.verbose = true;
    } else if (arg === '--exec' && i + 1 < argv.length) {
      args.exec.push(argv[++i]);
    } else if (arg === '--timeout' && i + 1 < argv.length) {
      args.timeout = parseInt(argv[++i], 10);
    } else if (arg === '--help' || arg === '-h') {
      console.log(`
WebQuake Headless Client - Node.js runner for scripted testing

Usage:
  node run_headless.js [options] [-- quake_args...]

Options:
  --nexus-url URL     Nexus server URL (default: http://localhost:7071)
  --mod NAME          Mod/game directory (default: id1)
  --verbose, -v       Enable verbose logging
  --exec CMD          Execute Quake command after startup (can be repeated)
  --timeout MS        Exit after MS milliseconds (for scripted tests)
  --help, -h          Show this help

Environment:
  NEXUS_URL           Default Nexus server URL

Examples:
  # Connect to a server
  node run_headless.js --exec "connect 127.255.255.1"

  # Run with timeout for CI
  node run_headless.js --timeout 10000 --exec "echo test complete"

  # Pass arguments to Quake
  node run_headless.js -- -nosound -game mymod
`);
      process.exit(0);
    } else {
      args.quakeArgs.push(arg);
    }
    i++;
  }

  return args;
}

async function main() {
  const args = parseArgs();

  const wsOverride = process.env.WEBSOCKET_URL || process.env.WS_URL || '';
  const toWsUrl = (baseUrl) => {
    if (!baseUrl) return '';
    if (baseUrl.startsWith('ws://') || baseUrl.startsWith('wss://')) {
      return baseUrl;
    }
    if (baseUrl.startsWith('https://')) {
      return 'wss://' + baseUrl.slice('https://'.length).replace(/\/+$/, '') + '/ws';
    }
    if (baseUrl.startsWith('http://')) {
      return 'ws://' + baseUrl.slice('http://'.length).replace(/\/+$/, '') + '/ws';
    }
    return baseUrl;
  };
  const websocketUrl = wsOverride ? toWsUrl(wsOverride) : toWsUrl(args.nexusUrl);

  if (typeof global.WebSocket === 'undefined') {
    try {
      // Optional dependency for Node WebSocket support.
      // Emscripten's websocket shim looks for a global WebSocket.
      global.WebSocket = require('ws');
    } catch (e) {
      if (args.verbose) {
        console.log('[Runner] ws not available; WebSocket support may be missing');
      }
    }
  }

  if (args.verbose) {
    console.log('[Runner] Configuration:', {
      nexusUrl: args.nexusUrl,
      mod: args.mod,
      timeout: args.timeout,
      exec: args.exec,
      quakeArgs: args.quakeArgs
    });
  }

  // Try to load sync-fetch for better performance
  let syncFetch = null;
  try {
    syncFetch = require('sync-fetch');
    if (args.verbose) {
      console.log('[Runner] Using sync-fetch for HTTP requests');
    }
  } catch (e) {
    if (args.verbose) {
      console.log('[Runner] sync-fetch not available, falling back to curl');
    }
  }

  // Locate the WASM module
  const scriptDir = __dirname;
  const wasmPath = path.join(scriptDir, 'quake_headless.js');

  if (!fs.existsSync(wasmPath)) {
    console.error(`[Runner] WASM module not found at: ${wasmPath}`);
    console.error('[Runner] Run the build first: ./testing/build-scripts/build_headless.sh');
    process.exit(1);
  }

  // Console output buffer for test assertions
  const consoleBuffer = [];

  // Set up Module configuration before loading
  global.Module = {
    nexusBaseUrl: args.nexusUrl,
    modName: args.mod,
    verbose: args.verbose,
    syncFetch: syncFetch,
    websocketUrl: websocketUrl,
    WEBSOCKET_URL: websocketUrl,

    // Capture console output
    onPrint: (text) => {
      consoleBuffer.push(text);
    },

    // Quake command line arguments
    arguments: args.quakeArgs,

    // Called when the module is ready
    onRuntimeInitialized: async () => {
      if (args.verbose) {
        console.log('[Runner] WASM runtime initialized');
      }

      // Execute startup commands after a short delay
      if (args.exec.length > 0) {
        setTimeout(() => {
          for (const cmd of args.exec) {
            if (args.verbose) {
              console.log(`[Runner] Executing: ${cmd}`);
            }
            Module.ccall('WebQuake_ExecCommand', 'void', ['string'], [cmd]);
          }
        }, 100);
      }
    }
  };

  // Set up timeout if specified
  if (args.timeout > 0) {
    setTimeout(() => {
      if (args.verbose) {
        console.log(`[Runner] Timeout reached (${args.timeout}ms), exiting`);
      }
      process.exit(0);
    }, args.timeout);
  }

  // Handle graceful shutdown
  process.on('SIGINT', () => {
    console.log('\n[Runner] Received SIGINT, disconnecting...');
    try {
      Module.ccall('WebQuake_OnPageHide', 'void', [], []);
    } catch (e) {}
    setTimeout(() => process.exit(0), 500);
  });

  process.on('SIGTERM', () => {
    console.log('[Runner] Received SIGTERM, disconnecting...');
    try {
      Module.ccall('WebQuake_OnPageHide', 'void', [], []);
    } catch (e) {}
    setTimeout(() => process.exit(0), 500);
  });

  // Load and run the WASM module
  if (args.verbose) {
    console.log(`[Runner] Loading WASM module from: ${wasmPath}`);
  }

  try {
    const createQuakeModule = require(wasmPath);
    await createQuakeModule(Module);
  } catch (err) {
    console.error('[Runner] Failed to load WASM module:', err.message);
    if (args.verbose) {
      console.error(err.stack);
    }
    process.exit(1);
  }
}

// Export for programmatic use
module.exports = {
  run: main,
  parseArgs
};

// Run if executed directly
if (require.main === module) {
  main().catch(err => {
    console.error('[Runner] Fatal error:', err.message);
    process.exit(1);
  });
}
