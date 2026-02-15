var nqStoredPerModConfig = nqLoadPerModConfig();

var Module = {
  nexquakeBaseGameName: NEXQUAKE_GAMENAME,
  nqPerModConfig: nqStoredPerModConfig === null ? false : !!nqStoredPerModConfig,
  preRun: [function() {
    var normalizeGameName = nqNormalizeGameName;
    var getBaseGameName = nqGetBaseGameName;
    var safeMkdirTree = nqSafeMkdirTree;
    var safeUnlink = nqSafeUnlink;

    function ensureGameDir(mod) {
      mod = normalizeGameName(mod);
      safeMkdirTree('/' + mod);
      return mod;
    }

    // Create the baseline game mount root.
    ensureGameDir(getBaseGameName());

    function ensureDirForPath(path) {
      var parts = String(path).split('/').filter(Boolean);
      if (parts.length <= 1) return;
      parts.pop(); // filename
      safeMkdirTree('/' + parts.join('/'));
    }

    // Remote assets act like pack files: they are readable, but "loose" user files
    // should take precedence. To achieve this we:
    // - avoid overwriting existing local files when installing remote nodes
    // - promote a remote node to a real writable file on first write attempt
    // - recreate a remote node on-demand when a read fails with ENOENT
    if (!Module.nexquakeRemoteFiles) Module.nexquakeRemoteFiles = Object.create(null);
    if (!Module.nexquakeRemoteURLs) Module.nexquakeRemoteURLs = Object.create(null);
    if (!Module.nexquakeInstalledManifests) Module.nexquakeInstalledManifests = Object.create(null);
    Module.nexquakeActiveGame = normalizeGameName(Module.nexquakeActiveGame || getBaseGameName());

    function resolveVFSPath(p) {
      p = String(p || '');
      if (!p) return p;
      try {
        if (p[0] === '/') return PATH.normalize(p);
        return PATH.normalize(FS.cwd() + '/' + p);
      } catch (e) {
        if (p[0] === '/') return p;
        var cwd = (typeof FS !== 'undefined' && FS && typeof FS.cwd === 'function') ? FS.cwd() : '/';
        if (!cwd.endsWith('/')) cwd += '/';
        return cwd + p;
      }
    }

    function isWriteOpenFlags(flags) {
      if (typeof flags === 'string') {
        // "w", "a", and "+" imply write intent.
        return flags.indexOf('w') !== -1 || flags.indexOf('a') !== -1 || flags.indexOf('+') !== -1;
      }
      // Numeric flags: POSIX open() uses O_RDONLY=0, O_WRONLY=1, O_RDWR=2.
      // (flags & 3) != 0 indicates a write-capable open.
      return (flags & 3) !== 0;
    }

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

    (function installUnionOpenShim() {
      if (!FS || !FS.open) return;
      if (FS._nexquake_union_open_shim) return;
      FS._nexquake_union_open_shim = true;

      var origOpen = FS.open;
      FS.open = function(path, flags, mode) {
        try {
          return origOpen.call(FS, path, flags, mode);
        } catch (e) {
          var absPath = resolveVFSPath(path);

          if (isWriteOpenFlags(flags)) {
            // If a remote (read-only) node exists at this path, replace it with a real
            // local file so Quake can write (and we can persist) user-owned content.
            try {
              var node = FS.lookupPath(absPath).node;
              if (node && node.url) {
                safeUnlink(absPath);
                delete Module.nexquakeRemoteFiles[absPath];
                return origOpen.call(FS, path, flags, mode);
              }
            } catch (e4) {}
          } else if (e && e.errno === 2) { // ENOENT
            // If a remote mapping exists for this path, recreate the remote stub and retry.
            try {
              var ent = Module.nexquakeRemoteURLs && Module.nexquakeRemoteURLs[absPath];
              if (ent && ent.url) {
                ensureDirForPath(absPath);
                var parent = absPath.substring(0, absPath.lastIndexOf('/')) || '/';
                var name = absPath.substring(absPath.lastIndexOf('/') + 1);
                var node = createRemoteFile(parent, name, ent.url, Number(ent.size || 0));
                Module.nexquakeRemoteFiles[absPath] = node;
                return origOpen.call(FS, path, flags, mode);
              }
            } catch (e5) {}
          }
          throw e;
        }
      };
    })();

    function installLazyFile(mod, vfsRelPath, url, size) {
      mod = normalizeGameName(mod);
      vfsRelPath = String(vfsRelPath || '').replace(/^\/+/, '');
      url = String(url || '').trim();
      if (!vfsRelPath || !url) return;

      // Store everything as lowercase in the VFS (Quake expects lowercase paths).
      var lowerRel = vfsRelPath.split('/').filter(Boolean).map(function(p) { return p.toLowerCase(); }).join('/');
      var outPath = '/' + mod + '/' + lowerRel;
      var parent = outPath.substring(0, outPath.lastIndexOf('/')) || '/';
      var name = outPath.substring(outPath.lastIndexOf('/') + 1);

      Module.nexquakeRemoteURLs[outPath] = { url: url, size: Number(size || 0) };

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
      var outPath = '/' + activeGame + '/' + lowerRel;
      var node = Module.nexquakeRemoteFiles && Module.nexquakeRemoteFiles[outPath];
      if (!node && activeGame !== baseGame) {
        node = Module.nexquakeRemoteFiles && Module.nexquakeRemoteFiles['/' + baseGame + '/' + lowerRel];
      }
      if (!node || node.contents) return;
      var resp = await fetch(node.url, { cache: 'force-cache' });
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

    // Mirror virtualized manifests into /<mod> roots.
    // Files are installed as lazy-backed VFS entries (download on first read).
    var baseGame = getBaseGameName();
    var manifestDependencyId = 'manifest:' + baseGame;
    Module.addRunDependency(manifestDependencyId);

    function applyPrefetchConcurrency(value) {
      if (value !== null && value !== undefined && value !== '') {
        Module.nexquakePrefetchConcurrency = parsePositiveInt(value, Module.nexquakePrefetchConcurrency);
      }
    }

    function fetchManifest(mod) {
      mod = normalizeGameName(mod);
      return fetch('/data-manifest/' + encodeURIComponent(mod)).then(function(response) {
        if (!response.ok) throw new Error('manifest fetch failed for ' + mod + ': ' + response.status);
        applyPrefetchConcurrency(response.headers.get('X-NQ-VFS-Prefetch-Concurrency'));
        return response.json();
      });
    }

    function fetchManifestSync(mod) {
      mod = normalizeGameName(mod);
      var xhr = new XMLHttpRequest();
      xhr.open('GET', '/data-manifest/' + encodeURIComponent(mod), false);
      xhr.send(null);
      if (xhr.status !== 200 && xhr.status !== 0) {
        throw new Error('manifest fetch failed for ' + mod + ': ' + xhr.status);
      }
      applyPrefetchConcurrency(xhr.getResponseHeader('X-NQ-VFS-Prefetch-Concurrency'));
      var text = xhr.responseText || '';
      return JSON.parse(text);
    }

    function installManifest(mod, entries) {
      mod = normalizeGameName(mod);
      if (!Array.isArray(entries) || entries.length === 0) {
        throw new Error('manifest empty for ' + mod);
      }

      // Ensure the mount root exists.
      ensureGameDir(mod);
      entries.forEach(function(ent) {
        installLazyFile(mod, ent.path, ent.url, Number(ent.size || 0));
      });
      Module.nexquakeInstalledManifests[mod] = true;
      Module.nexquakeActiveGame = mod;
      try { if (typeof Module.nqOverlayUpdateDirs === 'function') Module.nqOverlayUpdateDirs(); } catch (e) {}
    }

    function ensureManifestSync(mod) {
      mod = normalizeGameName(mod);
      if (Module.nexquakeInstalledManifests && Module.nexquakeInstalledManifests[mod]) {
        Module.nexquakeActiveGame = mod;
        return;
      }
      installManifest(mod, fetchManifestSync(mod));
    }

    Module.nexquakeSwitchGameData = function(mod) {
      mod = normalizeGameName(mod);
      try {
        ensureManifestSync(mod);
      } catch (err) {
        console.error('Failed to install /' + mod + ' manifest:', err);
      }
    };

    fetchManifest(baseGame)
      .then(function(entries) { installManifest(baseGame, entries); })
      .then(function() {
        Module.removeRunDependency(manifestDependencyId);
      })
      .catch(function(err) {
        console.error('Failed to install /' + baseGame + ' manifest:', err);
        try { Module.setStatus('Failed to load game data (see console)'); } catch (e) {}
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
    if (!Module.setStatus.last) Module.setStatus.last = { time: Date.now(), text: '' };
    if (text === Module.setStatus.last.text) return;
    var m = text.match(/([^(]+)\((\d+(\.\d+)?)\/(\d+)\)/);
    var now = Date.now();
    if (m && now - Module.setStatus.last.time < 30) return;
    Module.setStatus.last.time = now;
    Module.setStatus.last.text = text;
    if (m) {
      text = m[1].trim();
      var pct = Math.round((parseInt(m[2]) / parseInt(m[4])) * 100);
      loaderProgressBar.style.width = pct + '%';
    } else if (!text) {
      loaderElement.classList.add('hidden');
    }
    loaderStatusElement.textContent = text || 'Ready';
  },
  hideConsole: function() {
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
    Module.setStatus(left ? 'Preparing... (' + (this.totalDependencies-left) + '/' + this.totalDependencies + ')' : 'All downloads complete.');
  },
  onRuntimeInitialized: function() {
    outputElement.style.display = 'block';
  }
};
