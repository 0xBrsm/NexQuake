// nq-remote-vfs: remote manifests, lazy VFS entries, and saved-data sync
(function() {
  Module.preRun.push(function() {
    var startupVfsSupport = /** @type {any} */ (window).nqStartupVfsSupport;
    var normalizeGameName = nqNormalizeGameName;
    var getBaseGameName = nqGetBaseGameName;
    var safeMkdirTree = nqSafeMkdirTree;
    var REMOTE_ROOT = NEXQUAKE_REMOTE_ROOT;
    var USERFS_ROOT = '/NexQuake';
    var USER_GAME_ROOT = USERFS_ROOT + '/game';
    var USER_CD_ROOT = USERFS_ROOT + '/cd';
    var USER_LINK_BASENAME = '.usr';
    var USER_CFG_SEED_MARKER = USERFS_ROOT + '/.nq.cfgseed-v1';

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
      // Export a handle so we can prefetch into this node later.

      function ensureLoaded() {
        if (node.contents) return;
        if (node.nqRetryAfterMs && Date.now() < node.nqRetryAfterMs) {
          throw new FS.ErrnoError(44); // ENOENT
        }

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

          if (xhr.status !== 200 && xhr.status !== 0) {
            throw new Error('remote file fetch failed: ' + xhr.status + ' for ' + node.url);
          }

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
    Module.nexquakePrefetchConcurrency =
      (Module.nexquakePrefetchConcurrency === undefined || Module.nexquakePrefetchConcurrency === null)
        ? 16
        : Module.nexquakePrefetchConcurrency;
    Module.nexquakePrefetchFailures = Module.nexquakePrefetchFailures || Object.create(null);
    var prefetchController = startupVfsSupport.createPrefetchController({
      moduleRef: Module,
      normalizeGameName: normalizeGameName,
      getBaseGameName: getBaseGameName,
      remoteRoot: REMOTE_ROOT
    });

    Module.nexquakePrefetchReset = prefetchController.reset;
    Module.nexquakePrefetchEnqueue = prefetchController.enqueue;
    Module.nexquakePrefetchStart = prefetchController.start;

    // Install virtualized manifests into REMOTE_ROOT/<mod> roots.
    // Files are installed as lazy-backed VFS entries (download on first read).
    var baseGame = getBaseGameName();
    var userFsController = startupVfsSupport.createUserFsController({
      safeMkdirTree: safeMkdirTree,
      remoteRoot: REMOTE_ROOT,
      userFsRoot: USERFS_ROOT,
      userGameRoot: USER_GAME_ROOT,
      userCdRoot: USER_CD_ROOT,
      userLinkBasename: USER_LINK_BASENAME,
      userCfgSeedMarker: USER_CFG_SEED_MARKER,
      baseGame: baseGame,
      normalizeGameName: normalizeGameName,
      getBaseGameName: getBaseGameName
    });
    var manifestDependencyId = 'manifest:' + baseGame;
    var syncDependencyId = 'sync:' + baseGame;
    nqSetBootstrapPhase(2);
    Module.addRunDependency(manifestDependencyId);

    function applyClientConfig(config) {
      if (!config || typeof config !== 'object')
        config = {};
      prefetchController.applyConcurrency(config.prefetchConcurrency);
      Module.nexquakeAutoSMenuOnFirstLoad = config.smenuOnFirstLoad === true;
      Module.nexquakeSendArgs = Array.isArray(config.sendArgs) ? config.sendArgs.slice() : [];
      Module.nexquakeURLArgs = config.urlArgs === true;
    }

    function fetchStartBundle() {
      return fetch('/start').then(function(response) {
        if (!response.ok) throw new Error('start bundle fetch failed: ' + response.status);
        Module.nexquakeAssetRef = String(response.headers.get('X-NexQuake-Ref') || '');
        if (!Module.nexquakeAssetRef)
          throw new Error('start bundle missing X-NexQuake-Ref header');
        return response.text();
      }).then(function(encoded) {
        var decoded = startupVfsSupport.decodeBase64UTF8(encoded);
        var bundle;
        try {
          bundle = JSON.parse(decoded);
        } catch (err) {
          throw new Error('start bundle decode failed: ' + err);
        }
        applyClientConfig(bundle.client);
        return bundle;
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
        installLazyFile(mod, path, startupVfsSupport.computeAssetURL(
          Module.nexquakeAssetRef,
          'mod',
          mod,
          path,
          normalizeGameName
        ), 0);
      });
      Module.nexquakeInstalledManifests[mod] = true;
      Module.nexquakeActiveGame = mod;
      try { if (typeof Module.nqOverlayUpdateDirs === 'function') Module.nqOverlayUpdateDirs(); } catch (e) {}
    }

    var manifestBundle = null;
    var manifestRefreshIntervalMs = 13 * 60 * 1000;
    var manifestRefreshInFlight = null;
    var manifestRefreshLoopStarted = false;
    var onDemandManifestRefreshAfterMs = 0;
    var onDemandManifestRefreshFailureCount = 0;

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
        var url = startupVfsSupport.computeAssetURL(
          Module.nexquakeAssetRef,
          'cd',
          '',
          path,
          normalizeGameName
        );
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
      if (manifestRefreshInFlight)
        return manifestRefreshInFlight;
      manifestRefreshInFlight = fetchStartBundle()
        .then(normalizeStartBundle)
        .then(installStartBundle)
        .finally(function() {
          manifestRefreshInFlight = null;
        });
      return manifestRefreshInFlight;
    }

    function requestOnDemandManifestRefresh() {
      var now = Date.now();
      if (now < onDemandManifestRefreshAfterMs)
        return;
      onDemandManifestRefreshAfterMs = now + 5000;
      refreshStartBundle().then(function() {
        onDemandManifestRefreshFailureCount = 0;
      }, function(err) {
        onDemandManifestRefreshFailureCount++;
        onDemandManifestRefreshAfterMs =
          Date.now() + Math.min(60000, 1000 * Math.pow(2, Math.min(onDemandManifestRefreshFailureCount, 6)));
        console.warn('Failed to refresh manifest bundle:', err);
      });
    }

    function startManifestRefreshLoop() {
      if (manifestRefreshLoopStarted)
        return;
      manifestRefreshLoopStarted = true;
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
        if (!Module.nexquakeInstalledManifests || !Module.nexquakeInstalledManifests[mod]) {
          throw new Error('manifest missing for ' + mod);
        }
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
        startManifestRefreshLoop();
        nqSetBootstrapPhase(3);
        Module.addRunDependency(syncDependencyId);
        Module.removeRunDependency(manifestDependencyId);
        return userFsController.syncSavedData()
          .then(function() {
            return userFsController.seedUserCfgFilesOnce();
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
