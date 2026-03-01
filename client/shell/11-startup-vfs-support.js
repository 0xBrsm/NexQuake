// nq-startup-vfs: support utilities (asset URL hashing, decode, prefetch controller)
var nqStartupHost = /** @type {any} */ (window);
nqStartupHost.nqStartupVfsSupport = (function() {
  function parseNonNegativeInt(value, fallback) {
    var n = Number(value);
    if (!Number.isFinite(n)) return fallback;
    n = Math.floor(n);
    return n >= 0 ? n : fallback;
  }

  function resolvePrefetchWorkerCount(concurrency, queueLength, fallbackConcurrency) {
    concurrency = parseNonNegativeInt(concurrency, fallbackConcurrency);
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

  function computeAssetURL(assetRef, kind, mod, relPath, normalizeGameName) {
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

  function createPrefetchController(options) {
    var moduleRef = options.moduleRef;
    var normalizeGameName = options.normalizeGameName;
    var getBaseGameName = options.getBaseGameName;
    var remoteRoot = options.remoteRoot;

    async function prefetchOne(lowerRel) {
      var baseGame = getBaseGameName();
      var activeGame = normalizeGameName(moduleRef.nexquakeActiveGame || baseGame);
      var outPath = remoteRoot + '/' + activeGame + '/' + lowerRel;
      var node = moduleRef.nexquakeRemoteFiles && moduleRef.nexquakeRemoteFiles[outPath];
      if (!node && activeGame !== baseGame) {
        node = moduleRef.nexquakeRemoteFiles && moduleRef.nexquakeRemoteFiles[remoteRoot + '/' + baseGame + '/' + lowerRel];
      }
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
      for (var i = 0; i < paths.length; i++) {
        var p = String(paths[i] || '').trim();
        if (!p || uniq[p]) continue;
        uniq[p] = true;
        queue.push(p);
      }
      concurrency = resolvePrefetchWorkerCount(concurrency, queue.length, moduleRef.nexquakePrefetchConcurrency);
      if (concurrency <= 0) return;

      var idx = 0;
      async function worker() {
        while (true) {
          var j = idx++;
          if (j >= queue.length) return;
          try {
            await prefetchOne(queue[j]);
          } catch (e) {
            moduleRef.nexquakePrefetchFailures[queue[j]] = String(e && e.message ? e.message : e);
          }
        }
      }

      var workers = [];
      for (var w = 0; w < concurrency; w++) workers.push(worker());
      await Promise.all(workers);
    }

    function applyConcurrency(value) {
      if (value !== null && value !== undefined && value !== '') {
        moduleRef.nexquakePrefetchConcurrency =
          parseNonNegativeInt(value, moduleRef.nexquakePrefetchConcurrency);
      }
    }

    function reset() {
      moduleRef.nexquakePrefetchQueue = [];
      moduleRef.nexquakePrefetchFailures = Object.create(null);
    }

    function enqueue(relPath) {
      relPath = String(relPath || '').replace(/^\/+/, '').trim();
      if (!relPath) return;
      var lowerRel = relPath.split('/').filter(Boolean).map(function(p) { return p.toLowerCase(); }).join('/');
      moduleRef.nexquakePrefetchQueue.push(lowerRel);
    }

    function start() {
      if (moduleRef.nexquakePrefetchBusy) return;
      var list = (moduleRef.nexquakePrefetchQueue || []).slice();
      moduleRef.nexquakePrefetchQueue = [];
      if (!list.length) return;
      moduleRef.nexquakePrefetchBusy = 1;
      moduleRef.nexquakePrefetchFailures = Object.create(null);
      prefetchMany(list, moduleRef.nexquakePrefetchConcurrency)
        .finally(function() { moduleRef.nexquakePrefetchBusy = 0; });
    }

    return {
      applyConcurrency: applyConcurrency,
      reset: reset,
      enqueue: enqueue,
      start: start
    };
  }

  function createUserFsController(options) {
    var safeMkdirTree = options.safeMkdirTree;
    var remoteRoot = options.remoteRoot;
    var userFsRoot = options.userFsRoot;
    var userGameRoot = options.userGameRoot;
    var userCdRoot = options.userCdRoot;
    var userLinkBasename = options.userLinkBasename;
    var userCfgSeedMarker = options.userCfgSeedMarker;
    var baseGame = options.baseGame;
    var normalizeGameName = options.normalizeGameName;
    var getBaseGameName = options.getBaseGameName;

    function syncSavedData() {
      safeMkdirTree(userFsRoot);
      try { FS.mount(IDBFS, {}, userFsRoot); } catch (e) {}
      return new Promise(function(resolve) {
        try {
          FS.syncfs(true, function(err) {
            if (err) console.warn('Failed to sync saved data:', err);
            try {
              safeMkdirTree(remoteRoot);
              safeMkdirTree(userGameRoot + '/' + baseGame);
              safeMkdirTree(userCdRoot);
              try { FS.symlink(userGameRoot, remoteRoot + '/' + userLinkBasename); } catch (e1) {}
              try { FS.symlink(userCdRoot, '/cd'); } catch (e2) {}
              try { FS.symlink(remoteRoot + '/' + userLinkBasename + '/' + baseGame, '/' + baseGame); } catch (e3) {}
              FS.readdir(userGameRoot).forEach(function(name) {
                if (name === '.' || name === '..' || name === baseGame) return;
                var st = null;
                try { st = FS.stat(userGameRoot + '/' + name); } catch (e4) {}
                if (st && FS.isDir(st.mode))
                  try { FS.symlink(userGameRoot + '/' + name, '/' + name); } catch (e5) {}
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
      targetRoot = userGameRoot + '/' + seedGame;

      try {
        FS.stat(userCfgSeedMarker);
        try {
          markerValue = String(FS.readFile(userCfgSeedMarker, { encoding: 'utf8' }) || '').trim();
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
        FS.writeFile(userCfgSeedMarker, '1\n');
      } catch (err) {
        console.error('Failed to seed user cfg files:', err);
        return Promise.resolve();
      }

      return syncUserFS();
    }

    return {
      syncSavedData: syncSavedData,
      seedUserCfgFilesOnce: seedUserCfgFilesOnce
    };
  }

  return {
    computeAssetURL: computeAssetURL,
    decodeBase64UTF8: decodeBase64UTF8,
    createPrefetchController: createPrefetchController,
    createUserFsController: createUserFsController
  };
})();
