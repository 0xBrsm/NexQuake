/**
 * shell_node_pre.js - Emscripten pre-JS for headless Node.js WASM client
 *
 * Sets up lazy VFS that fetches files from Nexus on demand,
 * mirroring the browser client's behavior for testing purposes.
 */

// These will be set by the runner script before Module initialization
if (typeof Module === 'undefined') Module = {};

Module.preRun = Module.preRun || [];
Module.preRun.push(function() {
  const nexusBaseUrl = Module.nexusBaseUrl || 'http://localhost:7071';
  const modName = Module.modName || 'id1';
  // Create the virtual folder for game data
  FS.mkdir('/' + modName);

  /**
   * Synchronous HTTP fetch for Node.js
   * Uses sync-fetch if available, falls back to child_process + curl
   */
  function fetchSync(url) {
    // Try sync-fetch first (preferred)
    if (Module.syncFetch) {
      const response = Module.syncFetch(url);
      if (!response.ok) {
        throw new Error(`HTTP ${response.status} fetching ${url}`);
      }
      return new Uint8Array(response.arrayBuffer());
    }

    // Fallback: use child_process with curl
    const { spawnSync } = require('child_process');
    try {
      const curlProc = spawnSync('curl', ['-sL', url], {
        encoding: 'buffer',
        maxBuffer: 50 * 1024 * 1024,  // 50MB max
        timeout: 30000
      });

      if (curlProc.error) {
        throw curlProc.error;
      }

      if (curlProc.status !== 0) {
        throw new Error(`curl failed with status ${curlProc.status} for ${url}`);
      }

      return new Uint8Array(curlProc.stdout);
    } catch (err) {
      throw new Error(`Failed to fetch ${url} via curl: ${err.message}`);
    }
  }

  /**
   * Create a lazy-loaded file node in the VFS
   * File contents are fetched on first read
   */
  function createRemoteFile(parent, name, url, size) {
    const node = FS.createFile(parent, name, null, true, false);
    node.url = url;
    node.usedBytes = Number.isFinite(size) && size > 0 ? size : 0;

    function ensureLoaded() {
      if (node.contents) return;

      if (Module.verbose) {
        console.log(`[VFS] Fetching: ${node.url}`);
      }

      const bytes = fetchSync(node.url);
      node.contents = bytes;
      node.usedBytes = bytes.length;

      if (Module.verbose) {
        console.log(`[VFS] Loaded: ${name} (${bytes.length} bytes)`);
      }
    }

    node.stream_ops = {
      open: function(stream) {
        ensureLoaded();
      },
      close: function(stream) {},
      read: function(stream, buffer, offset, length, position) {
        ensureLoaded();
        const contents = node.contents;
        if (!contents) return 0;
        const size = contents.length;
        if (position >= size) return 0;
        const end = Math.min(size, position + length);
        buffer.set(contents.subarray(position, end), offset);
        return end - position;
      },
      llseek: function(stream, offset, whence) {
        let position = offset;
        if (whence === 1) position += stream.position;
        else if (whence === 2) position += node.usedBytes;
        if (position < 0) throw new FS.ErrnoError(28); // EINVAL
        return position;
      }
    };

    return node;
  }

  /**
   * Ensure directory tree exists for a path
   */
  function ensureDirForPath(path) {
    const parts = String(path).split('/').filter(Boolean);
    if (parts.length <= 1) return;
    parts.pop(); // remove filename
    FS.mkdirTree('/' + parts.join('/'));
  }

  /**
   * Install a lazy file in the VFS
   */
  function installLazyFile(vfsRelPath, url, size) {
    vfsRelPath = String(vfsRelPath || '').replace(/^\/+/, '');
    url = String(url || '').trim();
    if (!vfsRelPath || !url) return;
    if (url.startsWith('/')) {
      url = nexusBaseUrl + url;
    }

    // Store as lowercase (Quake expects lowercase paths)
    const lowerRel = vfsRelPath.split('/').filter(Boolean)
      .map(p => p.toLowerCase()).join('/');
    const outPath = '/' + modName + '/' + lowerRel;
    const parent = outPath.substring(0, outPath.lastIndexOf('/')) || '/';
    const name = outPath.substring(outPath.lastIndexOf('/') + 1);

    ensureDirForPath(outPath);
    try { FS.unlink(outPath); } catch (e) {}
    createRemoteFile(parent, name, url, size);
  }

  /**
   * Fetch and install the manifest from Nexus
   */
  function fetchAndInstallManifest() {
    const manifestUrl = `${nexusBaseUrl}/data-manifest/${modName}`;

    if (Module.verbose) {
      console.log(`[VFS] Fetching manifest: ${manifestUrl}`);
    }

    let entries;
    try {
      const bytes = fetchSync(manifestUrl);
      const text = new TextDecoder().decode(bytes);
      entries = JSON.parse(text);
    } catch (err) {
      throw new Error(`Failed to fetch manifest from ${manifestUrl}: ${err.message}`);
    }

    if (!Array.isArray(entries) || entries.length === 0) {
      throw new Error('Manifest is empty or invalid');
    }

    if (Module.verbose) {
      console.log(`[VFS] Installing ${entries.length} files from manifest`);
    }

    // Ensure mount root exists
    try { FS.mkdir('/' + modName); } catch (e) {}

    // Handle both old format (string array) and new format (object array)
    if (typeof entries[0] === 'string') {
      // Old format: array of relative paths
      entries
        .filter(p => typeof p === 'string' && p.trim() !== '')
        .map(p => p.trim().replace(/^\/+/, ''))
        .filter(Boolean)
        .forEach(relPath => {
          const url = `${nexusBaseUrl}/data/${modName}/${relPath.split('/').map(encodeURIComponent).join('/')}`;
          installLazyFile(relPath, url);
        });
    } else {
      // New format: array of {path, url, size} objects
      entries.forEach(ent => {
        if (!ent || typeof ent !== 'object') return;
        if (typeof ent.path !== 'string' || typeof ent.url !== 'string') return;
        installLazyFile(ent.path, ent.url, Number(ent.size || 0));
      });
    }

    if (Module.verbose) {
      console.log('[VFS] Manifest installation complete');
    }
  }

  // Install the manifest synchronously before Quake starts
  try {
    fetchAndInstallManifest();
  } catch (err) {
    console.error('[VFS] Failed to install manifest:', err.message);
    throw err;
  }

  // Create writable directory for saves/configs
  try { FS.mkdir('/nqwasm'); } catch (e) {}

  // Signal that VFS is ready
  if (typeof Module._WebQuake_VFSReady === 'function') {
    // Will be called after Module is initialized
    Module.postRun = Module.postRun || [];
    Module.postRun.push(function() {
      Module._WebQuake_VFSReady();
    });
  }
});

// Console output capture for test assertions
Module.print = Module.print || function(text) {
  if (Module.onPrint) {
    Module.onPrint(text);
  }
  console.log(text);
};

Module.printErr = Module.printErr || function(text) {
  if (Module.onPrintErr) {
    Module.onPrintErr(text);
  }
  console.error(text);
};
