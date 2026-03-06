// nq-remote-vfs: remote manifests, lazy VFS entries, and saved-data sync
(function() {
  Module.preRun.push(function() {
    var normalizeGameName = nqNormalizeGameName;
    var getBaseGameName = nqGetBaseGameName;
    var safeMkdirTree = nqSafeMkdirTree;
    var REMOTE_ROOT = NEXQUAKE_REMOTE_ROOT;
    var USERFS_ROOT = '/NexQuake';
    var USER_GAME_ROOT = USERFS_ROOT + '/game';
    var USER_CD_ROOT = USERFS_ROOT + '/cd';
    var USER_LINK_BASENAME = '.usr';
    var USER_CFG_SEED_MARKER = USERFS_ROOT + '/.nq.cfgseed-v1';

    function resolvePrefetchWorkerCount(concurrency, queueLength, fallbackConcurrency) {
      concurrency = Number(concurrency);
      if (!Number.isFinite(concurrency) || concurrency < 0)
        concurrency = fallbackConcurrency;
      else
        concurrency = Math.floor(concurrency);
      if (queueLength <= 0) return 0;
      if (concurrency === 0) return queueLength;
      return Math.min(concurrency, queueLength);
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

    function computeAssetURL(assetRef, kind, mod, relPath) {
      var ref = String(assetRef || '').trim();
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

    function ensureGameDir(mod) {
      mod = normalizeGameName(mod);
      safeMkdirTree(REMOTE_ROOT + '/' + mod);
      return mod;
    }

    // Create the baseline remote mount root.
    ensureGameDir(getBaseGameName());
    try { FS.chdir(REMOTE_ROOT); } catch (e) {}

    function ensureDirForPath(path) {
      var parts = String(path).split('/').filter(Boolean);
      if (parts.length <= 1) return;
      parts.pop(); // filename
      safeMkdirTree('/' + parts.join('/'));
    }

    // Remote assets live under REMOTE_ROOT/<mod> as lazy VFS entries.
    // User-writable mod content lives in /NexQuake/game/<mod> (IDBFS), linked into REMOTE_ROOT/.usr/<mod>.
    // User CD uploads live in /NexQuake/cd and are exposed at /cd.
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

      function ensureLoaded() {
        if (node.contents) return;
        if (node.nqRetryAfterMs && Date.now() < node.nqRetryAfterMs)
          throw new FS.ErrnoError(44); // ENOENT

        function markErrorAndThrow(error) {
          node.nqErrorCount++;
          node.nqRetryAfterMs = Date.now() + Math.min(15000, 500 * Math.pow(2, Math.min(node.nqErrorCount, 5)));
          node.nqLastLoadError = String(error && error.message ? error.message : error);
          requestOnDemandManifestRefresh();
          throw new FS.ErrnoError(44); // ENOENT
        }

        // Synchronous XHR is still permitted on the main thread, but Chrome disallows
        // setting responseType for sync requests. Use the classic "x-user-defined" path
        // and convert responseText -> Uint8Array.
        var xhr = new XMLHttpRequest();
        try {
          xhr.open('GET', node.url, false);
          try { xhr.overrideMimeType('text/plain; charset=x-user-defined'); } catch (e) {}
          xhr.send(null);

          if (xhr.status !== 200 && xhr.status !== 0)
            throw new Error('remote file fetch failed: ' + xhr.status + ' for ' + node.url);

          var text = xhr.responseText || '';
          var bytes = new Uint8Array(text.length);
          for (var i = 0; i < text.length; i++) bytes[i] = text.charCodeAt(i) & 0xFF;
          node.contents = bytes;
          node.usedBytes = bytes.length;
          node.nqErrorCount = 0;
          node.nqRetryAfterMs = 0;
          node.nqLastLoadError = '';
        } catch (e) {
          markErrorAndThrow(e);
        }
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
          var dataSize = contents.length;
          if (position >= dataSize) return 0;
          var end = Math.min(dataSize, position + length);
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
    Module.nexquakePrefetchConcurrency =
      (Module.nexquakePrefetchConcurrency === undefined || Module.nexquakePrefetchConcurrency === null)
        ? 16
        : Module.nexquakePrefetchConcurrency;
    Module.nexquakePrefetchFailures = Module.nexquakePrefetchFailures || Object.create(null);

    async function prefetchOne(lowerRel) {
      var baseGame = getBaseGameName();
      var activeGame = normalizeGameName(Module.nexquakeActiveGame || baseGame);
      var outPath = REMOTE_ROOT + '/' + activeGame + '/' + lowerRel;
      var node = Module.nexquakeRemoteFiles && Module.nexquakeRemoteFiles[outPath];
      if (!node && activeGame !== baseGame)
        node = Module.nexquakeRemoteFiles && Module.nexquakeRemoteFiles[REMOTE_ROOT + '/' + baseGame + '/' + lowerRel];
      if (!node || node.contents) return;
      var resp = await fetch(node.url, { cache: 'no-store' });
      if (!resp.ok) throw new Error('prefetch failed: ' + resp.status + ' for ' + node.url);
      var buf = await resp.arrayBuffer();
      node.contents = new Uint8Array(buf);
      node.usedBytes = node.contents.length;
    }

    async function prefetchMany(paths, concurrency) {
      var uniq = Object.create(null);
      var queue = [];
      var i;

      for (i = 0; i < paths.length; i++) {
        var p = String(paths[i] || '').trim();
        if (!p || uniq[p]) continue;
        uniq[p] = true;
        queue.push(p);
      }

      concurrency = resolvePrefetchWorkerCount(concurrency, queue.length, Module.nexquakePrefetchConcurrency);
      if (concurrency <= 0) return;

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

    function applyPrefetchConcurrency(value) {
      if (value === null || value === undefined || value === '')
        return;
      value = Number(value);
      if (Number.isFinite(value) && value >= 0)
        Module.nexquakePrefetchConcurrency = Math.floor(value);
    }

    function resetPrefetch() {
      Module.nexquakePrefetchQueue = [];
      Module.nexquakePrefetchFailures = Object.create(null);
    }

    function enqueuePrefetch(relPath) {
      relPath = String(relPath || '').replace(/^\/+/, '').trim();
      if (!relPath) return;
      var lowerRel = relPath.split('/').filter(Boolean).map(function(p) { return p.toLowerCase(); }).join('/');
      Module.nexquakePrefetchQueue.push(lowerRel);
    }

    function startPrefetch() {
      var list;
      if (Module.nexquakePrefetchBusy) return;
      list = (Module.nexquakePrefetchQueue || []).slice();
      Module.nexquakePrefetchQueue = [];
      if (!list.length) return;
      Module.nexquakePrefetchBusy = 1;
      Module.nexquakePrefetchFailures = Object.create(null);
      prefetchMany(list, Module.nexquakePrefetchConcurrency)
        .finally(function() { Module.nexquakePrefetchBusy = 0; });
    }

    Module.nexquakePrefetchReset = resetPrefetch;
    Module.nexquakePrefetchEnqueue = enqueuePrefetch;
    Module.nexquakePrefetchStart = startPrefetch;

    function prefetchGfx() {
      var entries = manifestBundle && manifestBundle[baseGame];
      var paths = [];
      if (!Array.isArray(entries)) return;
      entries.forEach(function(ent) {
        var path = String(ent && ent.path || '').trim().toLowerCase();
        if (path && path.indexOf('gfx/') === 0)
          paths.push(path);
      });
      if (paths.length > 0)
        prefetchMany(paths, Module.nexquakePrefetchConcurrency);
    }

    // Install virtualized manifests into REMOTE_ROOT/<mod> roots.
    // Files are installed as lazy-backed VFS entries (download on first read).
    var baseGame = getBaseGameName();

    function syncSavedData() {
      safeMkdirTree(USERFS_ROOT);
      try { FS.mount(IDBFS, {}, USERFS_ROOT); } catch (e) {}
      return new Promise(function(resolve) {
        try {
          FS.syncfs(true, function(err) {
            if (err) console.warn('Failed to sync saved data:', err);
            try {
              safeMkdirTree(REMOTE_ROOT);
              safeMkdirTree(USER_GAME_ROOT + '/' + baseGame);
              safeMkdirTree(USER_CD_ROOT);
              try { FS.symlink(USER_GAME_ROOT, REMOTE_ROOT + '/' + USER_LINK_BASENAME); } catch (e1) {}
              try { FS.symlink(USER_CD_ROOT, '/cd'); } catch (e2) {}
              try { FS.symlink(REMOTE_ROOT + '/' + USER_LINK_BASENAME + '/' + baseGame, '/' + baseGame); } catch (e3) {}
              FS.readdir(USER_GAME_ROOT).forEach(function(name) {
                var st = null;
                if (name === '.' || name === '..' || name === baseGame) return;
                try { st = FS.stat(USER_GAME_ROOT + '/' + name); } catch (e4) {}
                if (st && FS.isDir(st.mode))
                  try { FS.symlink(USER_GAME_ROOT + '/' + name, '/' + name); } catch (e5) {}
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

    function syncUserFS() {
      return new Promise(function(resolve) {
        try {
          FS.syncfs(false, function(err) {
            if (err) console.warn('Failed to sync saved data:', err);
            resolve();
          });
        } catch (e) {
          console.warn('Failed to sync saved data:', e);
          resolve();
        }
      });
    }

    function seedUserCfgFilesOnce() {
      var seedGame;
      var preloadRoot;
      var targetRoot;
      var autoexecData;
      var nexquakeData;
      var markerValue = '';

      seedGame = normalizeGameName(baseGame || getBaseGameName());
      targetRoot = USER_GAME_ROOT + '/' + seedGame;

      try {
        FS.stat(USER_CFG_SEED_MARKER);
        try {
          markerValue = String(FS.readFile(USER_CFG_SEED_MARKER, { encoding: 'utf8' }) || '').trim();
        } catch (markerReadErr) {
          markerValue = '';
        }
        if (markerValue === '1')
          return Promise.resolve();
      } catch (e) {
        var markerMissing = e && e.name === 'ErrnoError' && e.errno === 44;
        if (!markerMissing) {
          console.error('Failed to stat user cfg seed marker:', e);
          return Promise.resolve();
        }
      }

      try {
        preloadRoot = '/nqseed/' + seedGame;
        autoexecData = FS.readFile(preloadRoot + '/autoexec.cfg');
        nexquakeData = FS.readFile(preloadRoot + '/nexquake.cfg');
      } catch (missingErr) {
        console.error('Failed to load cfg seed payload from index.data:', missingErr);
        return Promise.resolve();
      }

      try {
        safeMkdirTree(targetRoot);
        FS.writeFile(targetRoot + '/autoexec.cfg', autoexecData);
        FS.writeFile(targetRoot + '/nexquake.cfg', nexquakeData);
        FS.writeFile(USER_CFG_SEED_MARKER, '1\n');
      } catch (err) {
        console.error('Failed to seed user cfg files:', err);
        return Promise.resolve();
      }

      return syncUserFS();
    }

    var manifestDependencyId = 'manifest:' + baseGame;
    var syncDependencyId = 'sync:' + baseGame;
    var manifestBundle = null;
    var manifestRefreshIntervalMs = 13 * 60 * 1000;
    var manifestRefreshState = {
      inFlight: null,
      loopStarted: false,
      retryAfterMs: 0,
      failureCount: 0
    };

    nqSetBootstrapPhase(2);
    Module.addRunDependency(manifestDependencyId);

    function applyClientConfig(config) {
      config = config && typeof config === 'object' ? config : {};
      applyPrefetchConcurrency(config.prefetchConcurrency);
      Module.nexquakeApplyClientConfig(config);
    }

    function fetchStartBundle() {
      var rawBundle = Module.nexquakeStartBundle;
      var bundlePromise;

      if (rawBundle)
        Module.nexquakeStartBundle = null;
      bundlePromise = rawBundle
        ? Promise.resolve(rawBundle)
        : fetch('/start').then(function(response) {
          if (!response.ok) throw new Error('start bundle fetch failed: ' + response.status);
          Module.nexquakeAssetRef = String(response.headers.get('X-NexQuake-Ref') || '');
          if (!Module.nexquakeAssetRef)
            throw new Error('start bundle missing X-NexQuake-Ref header');
          return response.text();
        }).then(nqParseStartBundle);

      return bundlePromise.then(function(rawBundle) {
        var rawGame = (rawBundle && rawBundle.game && typeof rawBundle.game === 'object') ? rawBundle.game : {};
        var rawCd = (rawBundle && Array.isArray(rawBundle.cd)) ? rawBundle.cd : [];
        var game = Object.create(null);

        applyClientConfig(rawBundle && rawBundle.client);
        Object.keys(rawGame).forEach(function(rawMod) {
          var mod = normalizeGameName(rawMod);
          if (mod)
            game[mod] = Array.isArray(rawGame[rawMod]) ? rawGame[rawMod] : [];
        });

        if (!game[baseGame])
          throw new Error('manifest bundle missing base game: ' + baseGame);

        return { game: game, cd: rawCd };
      });
    }

    function installManifest(mod, entries) {
      mod = normalizeGameName(mod);
      if (!Array.isArray(entries))
        throw new Error('manifest invalid for ' + mod);

      // Ensure the mount root exists.
      ensureGameDir(mod);
      entries.forEach(function(ent) {
        var path = String(ent && ent.path || '').trim();
        if (!path) return;
        installLazyFile(mod, path, computeAssetURL(Module.nexquakeAssetRef, 'mod', mod, path), 0);
      });
      Module.nexquakeInstalledManifests[mod] = true;
      Module.nexquakeActiveGame = mod;
      try { if (typeof Module.nqOverlayUpdateDirs === 'function') Module.nqOverlayUpdateDirs(); } catch (e) {}
    }

    function buildRemoteCdManifest(entries) {
      var out = [];
      if (!Array.isArray(entries))
        return out;
      entries.forEach(function(ent) {
        var path = String(ent && ent.path || '').trim();
        var url;
        if (!path) return;
        url = computeAssetURL(Module.nexquakeAssetRef, 'cd', '', path);
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

    function installStartBundle(bundle) {
      var activeGame = normalizeGameName(Module.nexquakeActiveGame || baseGame);
      manifestBundle = bundle.game;
      preloadAllManifestRoots();
      Module.nexquakeCdRemoteManifest = buildRemoteCdManifest(bundle.cd);
      if (!Module.nexquakeInstalledManifests || !Module.nexquakeInstalledManifests[activeGame])
        activeGame = baseGame;
      Module.nexquakeActiveGame = activeGame;
      if (activeGame !== baseGame && typeof Module.nexquakeSwitchGameData === 'function')
        Module.nexquakeSwitchGameData(activeGame);
    }

    function refreshStartBundle() {
      if (manifestRefreshState.inFlight)
        return manifestRefreshState.inFlight;
      manifestRefreshState.inFlight = fetchStartBundle()
        .then(installStartBundle)
        .finally(function() {
          manifestRefreshState.inFlight = null;
        });
      return manifestRefreshState.inFlight;
    }

    function requestOnDemandManifestRefresh() {
      var now = Date.now();
      if (now < manifestRefreshState.retryAfterMs)
        return;
      manifestRefreshState.retryAfterMs = now + 5000;
      refreshStartBundle().then(function() {
        manifestRefreshState.failureCount = 0;
      }, function(err) {
        manifestRefreshState.failureCount++;
        manifestRefreshState.retryAfterMs =
          Date.now() + Math.min(60000, 1000 * Math.pow(2, Math.min(manifestRefreshState.failureCount, 6)));
        console.warn('Failed to refresh manifest bundle:', err);
      });
    }

    function startManifestRefreshLoop() {
      if (manifestRefreshState.loopStarted)
        return;
      manifestRefreshState.loopStarted = true;
      setInterval(function() {
        refreshStartBundle().catch(function(err) { console.warn('Failed to refresh manifest bundle:', err); });
      }, manifestRefreshIntervalMs);
    }

    Module.nexquakeRefreshRemoteManifest = refreshStartBundle;

    Module.nexquakeOnWebSocketOpen = function() {
      refreshStartBundle().catch(function(err) {
        console.warn('Failed to refresh manifest bundle on websocket open:', err);
      });
    };

    Module.nexquakeSwitchGameData = function(mod) {
      mod = normalizeGameName(mod);
      try {
        if (!Module.nexquakeInstalledManifests || !Module.nexquakeInstalledManifests[mod])
          throw new Error('manifest missing for ' + mod);
        Module.nexquakeActiveGame = mod;
        safeMkdirTree(USER_GAME_ROOT + '/' + mod);
        try { FS.symlink(REMOTE_ROOT + '/' + USER_LINK_BASENAME + '/' + mod, '/' + mod); } catch (e2) {}
      } catch (err) {
        console.error('Failed to install /' + mod + ' manifest:', err);
      }
    };

    refreshStartBundle()
      .then(function() {
        Module.nexquakeActiveGame = baseGame;
        prefetchGfx();
        startManifestRefreshLoop();
        nqSetBootstrapPhase(3);
        Module.addRunDependency(syncDependencyId);
        Module.removeRunDependency(manifestDependencyId);
        return syncSavedData()
          .then(function() {
            return seedUserCfgFilesOnce();
          })
          .finally(function() {
            nqSetBootstrapRunning();
            Module.removeRunDependency(syncDependencyId);
          });
      })
      .catch(function(err) {
        console.error('Failed to preload manifest bundle (base game required):', err);
        try { Module.setStatus('failed to load game data'); } catch (e) {}
        // Intentionally do not remove the run dependency: Quake must not start without data.
      });
  });
})();
