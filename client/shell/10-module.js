var nqStoredPerModConfig = nqLoadPerModConfig();
var nqBootstrapReady = false;
var nqMainLoopStarted = false;
var nqNeedsReload = false;
var NQ_AUTOSTART_RELOAD_STORAGE_KEY = 'nexquake.autostart_after_reload';
var NQ_BOOTSTRAP_PHASE_COUNT = 3;
var NQ_BOOTSTRAP_PROGRESS_MAX = 90;
var NQ_BOOTSTRAP_PROGRESS_STEP = NQ_BOOTSTRAP_PROGRESS_MAX / NQ_BOOTSTRAP_PHASE_COUNT;
var nqBootstrapPhase = 1;
var nqAutoStartAfterReload = false;

function nqConsumeAutoStartAfterReload() {
  try {
    if (sessionStorage.getItem(NQ_AUTOSTART_RELOAD_STORAGE_KEY) === '1') {
      sessionStorage.removeItem(NQ_AUTOSTART_RELOAD_STORAGE_KEY);
      return true;
    }
  } catch (e) {}
  return false;
}

function nqMarkAutoStartAfterReload() {
  try {
    sessionStorage.setItem(NQ_AUTOSTART_RELOAD_STORAGE_KEY, '1');
  } catch (e) {}
}

function nqLogBootstrapStage(text) {
  if (text)
    console.info('[nq-loader] ' + text);
}

nqAutoStartAfterReload = nqConsumeAutoStartAfterReload();
nqLogBootstrapStage('instantiating WASM... (0%)');

function nqSetBootstrapProgress(percent) {
  if (!loaderProgressBar)
    return;
  if (!Number.isFinite(percent))
    percent = 0;
  if (percent < 0) percent = 0;
  if (percent > NQ_BOOTSTRAP_PROGRESS_MAX) percent = NQ_BOOTSTRAP_PROGRESS_MAX;
  loaderProgressBar.style.width = Math.round(percent) + '%';
}

function nqSetBootstrapPhase(phase) {
  var phaseText = '';

  if (phase < nqBootstrapPhase)
    return;
  nqBootstrapPhase = phase;
  nqSetBootstrapProgress((phase - 1) * NQ_BOOTSTRAP_PROGRESS_STEP);
  if (phase === 1)
    phaseText = 'instantiating WASM...';
  else if (phase === 2)
    phaseText = 'building vfs...';
  else if (phase === 3)
    phaseText = 'syncing saved data...';
  if (!phaseText)
    return;
  if (loaderStatusElement)
    loaderStatusElement.textContent = phaseText;
  nqLogBootstrapStage(phaseText + ' (' + Math.round((phase - 1) * NQ_BOOTSTRAP_PROGRESS_STEP) + '%)');
}

function nqSetBootstrapRunning() {
  nqSetBootstrapProgress(NQ_BOOTSTRAP_PROGRESS_MAX);
  if (loaderStatusElement)
    loaderStatusElement.textContent = 'running...';
  nqLogBootstrapStage('running... (' + NQ_BOOTSTRAP_PROGRESS_MAX + '%)');
}

function nqIsRuntimeStatusText(text) {
  var phaseMatch;
  if (!text)
    return false;
  text = String(text).trim().toLowerCase();
  phaseMatch = text.match(/^([^(]+)\(\s*\d+(?:\.\d+)?\s*\/\s*\d+\s*\)$/);
  if (phaseMatch)
    text = phaseMatch[1].trim();
  if (text === 'preparing...')
    return true;
  if (text === 'loading...')
    return true;
  if (text === 'running...')
    return true;
  if (text === 'all downloads complete.')
    return true;
  if (text === 'downloading...')
    return true;
  return false;
}

function nqShowEnterButton() {
  nqBootstrapReady = true;
  nqSetOverlayToggleVisible(true);
  try {
    if (Module.nqOverlayCtx) {
      if (typeof Module.nqOverlayCtx.syncCdEnabledFromPreference === 'function')
        Module.nqOverlayCtx.syncCdEnabledFromPreference();
      else if (typeof Module.nqOverlayCtx.setCdEnabled === 'function')
        Module.nqOverlayCtx.setCdEnabled(false, false);
    }
  } catch (e) {}
  if (loaderElement)
    loaderElement.classList.add('enter-mode');
  if (loaderStatusElement)
    loaderStatusElement.textContent = '';
  if (loaderReloadButton) {
    loaderReloadButton.textContent = 'ENTER';
    loaderReloadButton.disabled = false;
    loaderReloadButton.classList.remove('hidden');
  }
  nqLogBootstrapStage('enter ready (100%)');
  if (nqAutoStartAfterReload) {
    nqAutoStartAfterReload = false;
    setTimeout(nqStartGameFromEnter, 0);
  }
}

function nqStartGameFromEnter() {
  if (nqNeedsReload) {
    nqMarkAutoStartAfterReload();
    window.location.reload();
    return;
  }
  if (nqGameStarted) {
    window.location.reload();
    return;
  }
  if (!nqBootstrapReady || !nqRuntimeReady || nqMainLoopStarted)
    return;
  nqMainLoopStarted = true;
  nqGameStarted = true;
  if (loaderReloadButton) {
    loaderReloadButton.textContent = 'STARTING...';
    loaderReloadButton.disabled = true;
  }
  try {
    if (Module.nqOverlayCtx) {
      if (typeof Module.nqOverlayCtx.syncCdEnabledFromPreference === 'function')
        Module.nqOverlayCtx.syncCdEnabledFromPreference();
      else if (typeof Module.nqOverlayCtx.setCdEnabled === 'function')
        Module.nqOverlayCtx.setCdEnabled(true, false);
    }
  } catch (e3) {}
  nqLogBootstrapStage('starting game client...');
  try {
    if (typeof Module.callMain !== 'function')
      throw new Error('Module.callMain is not available');
    Module.callMain([]);
    nqLogBootstrapStage('wasm main initialized');
    Module.ccall('NexQuake_StartMainLoop', 'void', [], []);
    nqLogBootstrapStage('main loop started');
    if (typeof Module.hideConsole === 'function')
      Module.hideConsole();
    try {
      if (Module.nqOverlayCtx && typeof Module.nqOverlayCtx.applyCdPreferenceToGame === 'function')
        Module.nqOverlayCtx.applyCdPreferenceToGame();
      if (Module.nqOverlayCtx && typeof Module.nqOverlayCtx.refresh === 'function')
        Module.nqOverlayCtx.refresh();
    } catch (overlayErr) {
      console.warn('Overlay refresh failed:', overlayErr);
    }
  } catch (err) {
    console.error('Failed to start main loop:', err);
    nqMainLoopStarted = false;
    nqGameStarted = false;
    if (loaderReloadButton) {
      loaderReloadButton.textContent = 'ENTER';
      loaderReloadButton.disabled = false;
    }
    try {
      if (Module.nqOverlayCtx) {
        if (typeof Module.nqOverlayCtx.syncCdEnabledFromPreference === 'function')
          Module.nqOverlayCtx.syncCdEnabledFromPreference();
        else if (typeof Module.nqOverlayCtx.setCdEnabled === 'function')
          Module.nqOverlayCtx.setCdEnabled(false, false);
      }
    } catch (e4) {}
  }
}

var Module = {
  nexquakeBaseGameName: NEXQUAKE_GAMENAME,
  noInitialRun: true,
  nqPerModConfig: nqStoredPerModConfig === null ? false : !!nqStoredPerModConfig,
  preRun: [function() {
    var normalizeGameName = nqNormalizeGameName;
    var getBaseGameName = nqGetBaseGameName;
    var safeMkdirTree = nqSafeMkdirTree;
    var REMOTE_ROOT = '/.nqremote';
    var USERFS_ROOT = '/NexQuake';

    function ensureGameDir(mod) {
      mod = normalizeGameName(mod);
      safeMkdirTree(REMOTE_ROOT + '/' + mod);
      return mod;
    }

    // Create the baseline remote mount root.
    ensureGameDir(getBaseGameName());

    function ensureDirForPath(path) {
      var parts = String(path).split('/').filter(Boolean);
      if (parts.length <= 1) return;
      parts.pop(); // filename
      safeMkdirTree('/' + parts.join('/'));
    }

    // Remote assets live under /.nqremote/<mod> as lazy VFS entries.
    // User-writable content lives in /NexQuake/<mod> (IDBFS), exposed at /<mod> via symlink.
    // Quake searchpaths are layered: remote first, user last (so user overrides win).
    if (!Module.nexquakeRemoteFiles) Module.nexquakeRemoteFiles = Object.create(null);
    if (!Module.nexquakeInstalledManifests) Module.nexquakeInstalledManifests = Object.create(null);
    if (!Array.isArray(Module.nexquakeCdRemoteManifest)) Module.nexquakeCdRemoteManifest = [];
    Module.nexquakeActiveGame = normalizeGameName(Module.nexquakeActiveGame || getBaseGameName());

    // Emscripten's built-in FS.createLazyFile only supports browser lazy-loading in Web Workers.
    // NexQuake runs on the main thread, so we implement a tiny "lazy remote file" wrapper:
    // - Create the file node up-front so Quake can open it.
    // - On first read/open, download the full file into MEMFS (blocking via ASYNCIFY).
    // This preserves Quake's synchronous filesystem model while avoiding upfront downloads.
    function createRemoteFile(parent, name, url, size) {
      var node = FS.createFile(parent, name, null, true, false);
      node.url = url;
      node.usedBytes = Number.isFinite(size) && size > 0 ? size : 0;
      node.nqErrorCount = 0;
      node.nqRetryAfterMs = 0;
      node.nqLastLoadError = '';
      // Export a handle so we can prefetch into this node later.

      function ensureLoaded() {
        if (node.contents) return;
        if (node.nqRetryAfterMs && Date.now() < node.nqRetryAfterMs) {
          throw new Error(node.nqLastLoadError || ('remote file fetch throttled for ' + node.url));
        }
        // Synchronous XHR is still permitted on the main thread, but Chrome disallows
        // setting responseType for sync requests. Use the classic "x-user-defined" path
        // and convert responseText -> Uint8Array.
        var xhr = new XMLHttpRequest();
        try {
          xhr.open('GET', node.url, false);
          try { xhr.overrideMimeType('text/plain; charset=x-user-defined'); } catch (e) {}
          xhr.send(null);
        } catch (e) {
          node.nqErrorCount++;
          node.nqRetryAfterMs = Date.now() + Math.min(15000, 500 * Math.pow(2, Math.min(node.nqErrorCount, 5)));
          node.nqLastLoadError = String(e && e.message ? e.message : e);
          throw e;
        }

        if (xhr.status !== 200 && xhr.status !== 0) {
          node.nqErrorCount++;
          node.nqRetryAfterMs = Date.now() + Math.min(15000, 500 * Math.pow(2, Math.min(node.nqErrorCount, 5)));
          node.nqLastLoadError = 'remote file fetch failed: ' + xhr.status + ' for ' + node.url;
          throw new Error(node.nqLastLoadError);
        }

        var text = xhr.responseText || '';
        var bytes = new Uint8Array(text.length);
        for (var i = 0; i < text.length; i++) bytes[i] = text.charCodeAt(i) & 0xFF;
        node.contents = bytes;
        node.usedBytes = bytes.length;
        node.nqErrorCount = 0;
        node.nqRetryAfterMs = 0;
        node.nqLastLoadError = '';
      }

      node.stream_ops = {
        open: function() {
          ensureLoaded();
        },
        close: function() {},
        read: function(stream, buffer, offset, length, position) {
          ensureLoaded();
          var contents = node.contents;
          if (!contents) return 0;
          var size = contents.length;
          if (position >= size) return 0;
          var end = Math.min(size, position + length);
          buffer.set(contents.subarray(position, end), offset);
          return end - position;
        },
        llseek: function(stream, offset, whence) {
          var position = offset;
          if (whence === 1) position += stream.position;
          else if (whence === 2) position += node.usedBytes;
          if (position < 0) throw new FS.ErrnoError(28); // EINVAL
          return position;
        }
      };

      return node;
    }

    function installLazyFile(mod, vfsRelPath, url, size) {
      mod = normalizeGameName(mod);
      vfsRelPath = String(vfsRelPath || '').replace(/^\/+/, '');
      url = String(url || '').trim();
      if (!vfsRelPath || !url) return;

      // Store everything as lowercase in the VFS (Quake expects lowercase paths).
      var lowerRel = vfsRelPath.split('/').filter(Boolean).map(function(p) { return p.toLowerCase(); }).join('/');
      var outPath = REMOTE_ROOT + '/' + mod + '/' + lowerRel;
      var parent = outPath.substring(0, outPath.lastIndexOf('/')) || '/';
      var name = outPath.substring(outPath.lastIndexOf('/') + 1);

      ensureDirForPath(outPath);

      // Do not replace existing local files. Loose files should override "pak-like"
      // remote assets, matching default Quake precedence.
      try {
        var existing = FS.lookupPath(outPath).node;
        if (existing) {
          if (existing.url) {
            existing.url = url;
            existing.usedBytes = Number.isFinite(size) && size > 0 ? size : existing.usedBytes;
            Module.nexquakeRemoteFiles[outPath] = existing;
          }
          return;
        }
      } catch (e) {}

      try {
        var node = createRemoteFile(parent, name, url, size);
        Module.nexquakeRemoteFiles[outPath] = node;
      } catch (e) {
        console.error('Failed to install remote file:', outPath, e);
      }
    }

    // Prefetch support (used by the client after parsing svc_serverinfo).
    // The C code enqueues paths, then waits (with emscripten_sleep) for
    // Module.nexquakePrefetchBusy to become 0.
    Module.nexquakePrefetchQueue = Module.nexquakePrefetchQueue || [];
    Module.nexquakePrefetchBusy = 0;
    Module.nexquakePrefetchConcurrency = Module.nexquakePrefetchConcurrency || 16;
    Module.nexquakePrefetchFailures = Module.nexquakePrefetchFailures || Object.create(null);

    function parsePositiveInt(value, fallback) {
      var n = Number(value);
      if (!Number.isFinite(n)) return fallback;
      n = Math.floor(n);
      return n >= 1 ? n : fallback;
    }

    Module.nexquakePrefetchReset = function() {
      Module.nexquakePrefetchQueue = [];
      Module.nexquakePrefetchFailures = Object.create(null);
    };

    Module.nexquakePrefetchEnqueue = function(relPath) {
      relPath = String(relPath || '').replace(/^\/+/, '').trim();
      if (!relPath) return;
      var lowerRel = relPath.split('/').filter(Boolean).map(function(p) { return p.toLowerCase(); }).join('/');
      Module.nexquakePrefetchQueue.push(lowerRel);
    };

    async function prefetchOne(lowerRel) {
      var baseGame = getBaseGameName();
      var activeGame = normalizeGameName(Module.nexquakeActiveGame || baseGame);
      var outPath = REMOTE_ROOT + '/' + activeGame + '/' + lowerRel;
      var node = Module.nexquakeRemoteFiles && Module.nexquakeRemoteFiles[outPath];
      if (!node && activeGame !== baseGame) {
        node = Module.nexquakeRemoteFiles && Module.nexquakeRemoteFiles[REMOTE_ROOT + '/' + baseGame + '/' + lowerRel];
      }
      if (!node || node.contents) return;
      var resp = await fetch(node.url, { cache: 'no-store' });
      if (!resp.ok) throw new Error('prefetch failed: ' + resp.status + ' for ' + node.url);
      var buf = await resp.arrayBuffer();
      node.contents = new Uint8Array(buf);
      node.usedBytes = node.contents.length;
    }

    async function prefetchMany(paths, concurrency) {
      concurrency = parsePositiveInt(concurrency, Module.nexquakePrefetchConcurrency);
      var uniq = Object.create(null);
      var queue = [];
      for (var i = 0; i < paths.length; i++) {
        var p = String(paths[i] || '').trim();
        if (!p || uniq[p]) continue;
        uniq[p] = true;
        queue.push(p);
      }

      var idx = 0;
      async function worker() {
        while (true) {
          var j = idx++;
          if (j >= queue.length) return;
          try {
            await prefetchOne(queue[j]);
          } catch (e) {
            Module.nexquakePrefetchFailures[queue[j]] = String(e && e.message ? e.message : e);
          }
        }
      }

      var workers = [];
      for (var w = 0; w < concurrency; w++) workers.push(worker());
      await Promise.all(workers);
    }

    Module.nexquakePrefetchStart = function() {
      if (Module.nexquakePrefetchBusy) return;
      var list = (Module.nexquakePrefetchQueue || []).slice();
      Module.nexquakePrefetchQueue = [];
      if (!list.length) return;
      Module.nexquakePrefetchBusy = 1;
      Module.nexquakePrefetchFailures = Object.create(null);
      prefetchMany(list, Module.nexquakePrefetchConcurrency)
        .finally(function() { Module.nexquakePrefetchBusy = 0; });
    };

    // Install virtualized manifests into /.nqremote/<mod> roots.
    // Files are installed as lazy-backed VFS entries (download on first read).
    var baseGame = getBaseGameName();
    var manifestDependencyId = 'manifest:' + baseGame;
    var syncDependencyId = 'sync:' + baseGame;
    nqSetBootstrapPhase(2);
    Module.addRunDependency(manifestDependencyId);

    function applyPrefetchConcurrency(value) {
      if (value !== null && value !== undefined && value !== '') {
        Module.nexquakePrefetchConcurrency = parsePositiveInt(value, Module.nexquakePrefetchConcurrency);
      }
    }

    function normalizeRemotePath(path) {
      path = String(path || '').replace(/\\/g, '/').replace(/^\/+/, '');
      return path.split('/').filter(Boolean).map(function(part) { return part.toLowerCase(); }).join('/');
    }

    function fnv1a64Hex(text) {
      var FNV_OFFSET = 0xcbf29ce484222325n;
      var FNV_PRIME = 0x100000001b3n;
      var MASK_64 = 0xffffffffffffffffn;
      var bytes;
      var hash = FNV_OFFSET;
      var i;
      var hex;

      if (typeof BigInt === 'undefined')
        throw new Error('BigInt is required for NexQuake asset hashing');
      if (typeof TextEncoder === 'undefined')
        throw new Error('TextEncoder is required for NexQuake asset hashing');

      bytes = new TextEncoder().encode(String(text || ''));

      for (i = 0; i < bytes.length; i++) {
        hash ^= BigInt(bytes[i]);
        hash = (hash * FNV_PRIME) & MASK_64;
      }

      hex = hash.toString(16);
      while (hex.length < 16) hex = '0' + hex;
      return hex;
    }

    function computeAssetURL(kind, mod, relPath) {
      var ref = String(Module.nexquakeAssetRef || '').trim();
      var normalizedPath = normalizeRemotePath(relPath);
      var normalizedMod;
      var key;
      if (!ref || !normalizedPath)
        return '';
      if (kind === 'cd') {
        key = 'cd:' + normalizedPath;
      } else {
        normalizedMod = normalizeGameName(mod);
        if (!normalizedMod)
          return '';
        key = 'mod:' + normalizedMod + ':' + normalizedPath;
      }
      return '/nq/' + fnv1a64Hex(ref + ':' + key);
    }

    function decodeBase64UTF8(encoded) {
      var text = String(encoded || '').trim();
      var binary;
      var bytes;
      var i;
      if (!text)
        throw new Error('start bundle payload is empty');
      if (typeof atob !== 'function')
        throw new Error('base64 decode not supported in this runtime');
      if (typeof TextDecoder === 'undefined')
        throw new Error('TextDecoder is required for start bundle decode');
      binary = atob(text);
      bytes = new Uint8Array(binary.length);
      for (i = 0; i < binary.length; i++)
        bytes[i] = binary.charCodeAt(i) & 255;
      return new TextDecoder().decode(bytes);
    }

    function fetchStartBundle() {
      return fetch('/start').then(function(response) {
        if (!response.ok) throw new Error('start bundle fetch failed: ' + response.status);
        applyPrefetchConcurrency(response.headers.get('X-NQ-VFS-Prefetch-Concurrency'));
        Module.nexquakeAssetRef = String(response.headers.get('X-NexQuake-Ref') || '');
        if (!Module.nexquakeAssetRef)
          throw new Error('start bundle missing X-NexQuake-Ref header');
        return response.text();
      }).then(function(encoded) {
        var decoded = decodeBase64UTF8(encoded);
        try {
          return JSON.parse(decoded);
        } catch (err) {
          throw new Error('start bundle decode failed: ' + err);
        }
      });
    }

    function syncSavedData() {
      safeMkdirTree(USERFS_ROOT);
      try { FS.mount(IDBFS, {}, USERFS_ROOT); } catch (e) {}
      return new Promise(function(resolve) {
        try {
          FS.syncfs(true, function(err) {
            if (err) console.warn('Failed to sync saved data:', err);
            try {
              safeMkdirTree(USERFS_ROOT + '/' + baseGame);
              safeMkdirTree(USERFS_ROOT + '/cd');
              try { FS.symlink(USERFS_ROOT + '/cd', '/cd'); } catch (e2) {}
              try { FS.symlink(USERFS_ROOT + '/' + baseGame, '/' + baseGame); } catch (e3) {}
              FS.readdir(USERFS_ROOT).forEach(function(name) {
                if (name === '.' || name === '..' || name === baseGame || name === 'cd') return;
                var st = null;
                try { st = FS.stat(USERFS_ROOT + '/' + name); } catch (e4) {}
                if (st && FS.isDir(st.mode))
                  try { FS.symlink(USERFS_ROOT + '/' + name, '/' + name); } catch (e5) {}
              });
            } catch (linkErr) {
              console.warn('Failed to link user dirs:', linkErr);
            }
            resolve();
          });
        } catch (e) {
          console.warn('Failed to sync saved data:', e);
          resolve();
        }
      });
    }

    function installManifest(mod, entries) {
      mod = normalizeGameName(mod);
      if (!Array.isArray(entries)) {
        throw new Error('manifest invalid for ' + mod);
      }

      // Ensure the mount root exists.
      ensureGameDir(mod);
      entries.forEach(function(ent) {
        var path = String(ent && ent.path || '').trim();
        if (!path) return;
        installLazyFile(mod, path, computeAssetURL('mod', mod, path), 0);
      });
      Module.nexquakeInstalledManifests[mod] = true;
      Module.nexquakeActiveGame = mod;
      try { if (typeof Module.nqOverlayUpdateDirs === 'function') Module.nqOverlayUpdateDirs(); } catch (e) {}
    }

    var manifestBundle = null;

    function normalizeStartBundle(rawBundle) {
      var out = Object.create(null);
      var rawGame = (rawBundle && rawBundle.game && typeof rawBundle.game === 'object') ? rawBundle.game : {};
      var rawCd = (rawBundle && Array.isArray(rawBundle.cd)) ? rawBundle.cd : [];
      Object.keys(rawGame).forEach(function(rawMod) {
        var mod = normalizeGameName(rawMod);
        if (!mod)
          return;
        out[mod] = Array.isArray(rawGame[rawMod]) ? rawGame[rawMod] : [];
      });
      if (!out[baseGame])
        throw new Error('manifest bundle missing base game: ' + baseGame);
      return { game: out, cd: rawCd };
    }

    function buildRemoteCdManifest(entries) {
      var out = [];
      if (!Array.isArray(entries))
        return out;
      entries.forEach(function(ent) {
        var path = String(ent && ent.path || '').trim();
        if (!path) return;
        var url = computeAssetURL('cd', '', path);
        if (url) out.push({ path: path, url: url });
      });
      return out;
    }

    function preloadAllManifestRoots() {
      installManifest(baseGame, manifestBundle[baseGame]);
      Object.keys(manifestBundle).forEach(function(mod) {
        if (mod === baseGame)
          return;
        try {
          installManifest(mod, manifestBundle[mod]);
        } catch (err) {
          console.warn('Skipping /' + mod + ' manifest preload:', err);
        }
      });
    }

    Module.nexquakeSwitchGameData = function(mod) {
      mod = normalizeGameName(mod);
      try {
        if (!Module.nexquakeInstalledManifests || !Module.nexquakeInstalledManifests[mod]) {
          throw new Error('manifest missing for ' + mod);
        }
        Module.nexquakeActiveGame = mod;
        safeMkdirTree(USERFS_ROOT + '/' + mod);
        try { FS.symlink(USERFS_ROOT + '/' + mod, '/' + mod); } catch (e2) {}
      } catch (err) {
        console.error('Failed to install /' + mod + ' manifest:', err);
      }
    };

    fetchStartBundle()
      .then(normalizeStartBundle)
      .then(function(bundle) {
        manifestBundle = bundle.game;
        preloadAllManifestRoots();
        Module.nexquakeCdRemoteManifest = buildRemoteCdManifest(bundle.cd);
      })
      .then(function() {
        Module.nexquakeActiveGame = baseGame;
        nqSetBootstrapPhase(3);
        Module.addRunDependency(syncDependencyId);
        Module.removeRunDependency(manifestDependencyId);
        return syncSavedData().finally(function() {
          nqSetBootstrapRunning();
          Module.removeRunDependency(syncDependencyId);
        });
      })
      .catch(function(err) {
        console.error('Failed to preload manifest bundle (base game required):', err);
        try { Module.setStatus('failed to load game data'); } catch (e) {}
        // Intentionally do not remove the run dependency: Quake must not start without data.
      });
  }],
  postRun: [],
  print: (function() {
    outputElement.value = ''; // clear browser cache
    return function(text) {
      if (arguments.length > 1) text = Array.prototype.slice.call(arguments).join(' ');
      console.log(text);
      outputElement.value += text + "\n";
      outputElement.scrollTop = outputElement.scrollHeight; // focus on bottom
    };
  })(),
  canvas: canvasElement,
  setStatus: function(text) {
    if (!loaderStatusElement)
      return;
    if (nqIsRuntimeStatusText(text))
      return;
    text = String(text || '').trim();
    if (text)
      loaderStatusElement.textContent = text;
  },
  nqShowReloadScreen: function() {
    try {
      if (document.pointerLockElement && document.exitPointerLock)
        document.exitPointerLock();
    } catch (e) {}
    nqNeedsReload = true;
    nqMainLoopStarted = false;
    nqGameStarted = false;
    if (loaderProgressBar)
      loaderProgressBar.style.width = '0%';
    if (loaderStatusElement)
      loaderStatusElement.textContent = '';
    if (loaderReloadButton) {
      loaderReloadButton.textContent = 'ENTER';
      loaderReloadButton.disabled = false;
      loaderReloadButton.classList.remove('hidden');
    }
    if (loaderElement) {
      loaderElement.classList.remove('hidden');
      loaderElement.classList.add('enter-mode');
    }
    nqSetOverlayToggleVisible(true);
    try {
      if (Module.nqOverlayCtx) {
        if (typeof Module.nqOverlayCtx.syncCdEnabledFromPreference === 'function')
          Module.nqOverlayCtx.syncCdEnabledFromPreference();
        else if (typeof Module.nqOverlayCtx.setCdEnabled === 'function')
          Module.nqOverlayCtx.setCdEnabled(false, false);
      }
    } catch (e2) {}
    if (canvasElement)
      canvasElement.style.display = 'none';
    if (outputElement)
      outputElement.style.display = 'none';
    try {
      if (Module.nqOverlayCtx && Module.nqOverlayCtx.setPanelOpen)
        Module.nqOverlayCtx.setPanelOpen(false);
    } catch (e) {}
  },
  hideConsole: function() {
    nqNeedsReload = false;
    if (loaderElement)
      loaderElement.classList.add('hidden');
    outputElement.style.display = 'none';
    canvasElement.style.display = 'block';
    canvasElement.focus();
  },
  showConsole: function() {
    canvasElement.style.display = 'none';
    outputElement.style.display = 'block';
    outputElement.scrollTop = outputElement.scrollHeight;
    outputElement.focus();
  },
  exportFile: function(filePath) {
    try {
      const filePathSplit = filePath.split('/');
      const dataArray = new Uint8Array(FS.readFile(filePath));
      const dataBlob = new Blob([dataArray], {type: 'application/octet-stream'});
      const objURL = URL.createObjectURL(dataBlob);
      exportElement.href = objURL;
      exportElement.download = filePathSplit[filePathSplit.length - 1];
      exportElement.click();
      URL.revokeObjectURL(objURL);
    } catch (error) {
      console.error('Error exporting file:', error);
    }
  },
  setGamma: function(vidGamma) {
    vidGamma = Number(Number(vidGamma).toFixed(2));
    console.info('Detected canvas gamma change: ' + vidGamma);
    canvasElement.style.filter = 'brightness(' + ((1.35 - vidGamma) * 2) + ')';
  },
  totalDependencies: 0,
  monitorRunDependencies: function(left) {
    this.totalDependencies = Math.max(this.totalDependencies, left);
    if (nqBootstrapPhase !== 1 || !loaderProgressBar || this.totalDependencies <= 0)
      return;
    var done = this.totalDependencies - left;
    nqSetBootstrapProgress((done / this.totalDependencies) * NQ_BOOTSTRAP_PROGRESS_STEP);
  },
  onRuntimeInitialized: function() {
    nqRuntimeReady = true;
    outputElement.style.display = 'none';
    nqShowEnterButton();
  }
};

if (loaderReloadButton) {
  loaderReloadButton.onclick = function() {
    nqStartGameFromEnter();
  };
}
