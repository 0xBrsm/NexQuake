// nq-cdaudio: digital music backend for Quake CD track calls
(function() {
  var CD_DIR = ((typeof nqGetCdDir === 'function') ? nqGetCdDir() : '/cd/').replace(/\/+$/, '');
  var manifest = null;

  function notifyOverlayCdState() {
    if (Module && typeof Module.nqOverlayOnCdStateChange === 'function')
      try { Module.nqOverlayOnCdStateChange(); } catch (e) {}
  }

  function getTrackNumberFromName(name) {
    name = String(name || '').toLowerCase().replace(/\.(?:ogg|mp3)$/, '');
    var match = name.match(/^#?(\d+)/) || name.match(/(\d+)$/);
    var n = match ? (Number(match[1]) | 0) : 0;
    return n > 0 ? n : 0;
  }

  function getTrackNumberFromPath(path) {
    return getTrackNumberFromName(String(path || '').split(/[\\/]/).pop());
  }

  function loadRemoteManifest() {
    var xhr, raw;
    if (manifest !== null) return manifest;
    manifest = [];
    try {
      xhr = new XMLHttpRequest();
      xhr.open('GET', '/cd-manifest', false);
      xhr.send(null);
    } catch (e) { return manifest; }
    if (xhr.status === 404 || xhr.status === 204) return manifest;
    if (xhr.status !== 200 && xhr.status !== 0) return manifest;
    try { raw = JSON.parse(xhr.responseText || '[]'); } catch (e2) { return manifest; }
    if (!Array.isArray(raw)) return manifest;
    raw.forEach(function(entry) {
      var path = String(entry && entry.path || '').trim();
      var url = String(entry && entry.url || '').trim();
      if (path && url) manifest.push({ path: path, url: url });
    });
    return manifest;
  }

  function resolveLocalTrackPath(track) {
    var roots = [CD_DIR];

    for (var ri = 0; ri < roots.length; ri++) {
      var root = roots[ri];
      var q = [root];
      var qi = 0;
      var st = nqSafeStat(root);
      if (!st || !FS.isDir(st.mode))
        continue;

      for (; qi < q.length; qi++) {
        var dir = q[qi];
        var entries = nqSafeReadDir(dir).slice().sort(function(a, b) { return a.localeCompare(b); });
        for (var i = 0; i < entries.length; i++) {
          var name = entries[i];
          if (name === '.' || name === '..') continue;
          var path = dir + '/' + name;
          st = nqSafeStat(path);
          if (!st) continue;
          if (FS.isFile(st.mode)) {
            if (getTrackNumberFromName(name) === track) return path;
          } else if (FS.isDir(st.mode)) {
            q.push(path);
          }
        }
      }
    }
    return '';
  }

  function resolveTrackEntry(track) {
    var path = resolveLocalTrackPath(track);
    if (path) {
      try {
        var bytes = FS.readFile(path);
        var ext = path.toLowerCase().slice(path.lastIndexOf('.') + 1);
        var mime = ext === 'mp3' ? 'audio/mpeg' : 'audio/ogg';
        return { path: path, url: URL.createObjectURL(new Blob([bytes], { type: mime })) };
      } catch (e) {}
    }

    var entries = loadRemoteManifest();
    for (var i = 0; i < entries.length; i++) {
      var entry = entries[i];
      var name = String(entry.path || '').split(/[\\/]/).pop();
      if (getTrackNumberFromName(name) === track)
        return { path: CD_DIR + '/' + entry.path, url: entry.url };
    }
    return null;
  }

  function normalizeVolume(volume) {
    var v = Number(volume);
    if (!Number.isFinite(v)) v = 1;
    return Math.min(1, Math.max(0, v));
  }

  function revokeBlobURL(url) {
    if (!url) return;
    try { URL.revokeObjectURL(url); } catch (e) {}
  }

  var rqf = typeof requestAnimationFrame === 'function'
    ? requestAnimationFrame.bind(window)
    : function(fn) { return setTimeout(function() { fn(Date.now()); }, 16); };
  var cqf = typeof cancelAnimationFrame === 'function'
    ? cancelAnimationFrame.bind(window)
    : clearTimeout;

  function cancelFade(state) {
    if (!state || !state.fadeToken) return;
    cqf(state.fadeToken);
    state.fadeToken = 0;
  }

  function fadeToSilence(state, done) {
    var startVol;
    var startAt;
    var durationMs = 48;
    if (!state || !state.audio) {
      if (done) done();
      return;
    }
    cancelFade(state);
    if (state.audio.paused) {
      if (done) done();
      return;
    }
    startVol = Number(state.audio.volume);
    if (!Number.isFinite(startVol) || startVol <= 0.001) {
      if (done) done();
      return;
    }
    startAt = (typeof performance !== 'undefined' && performance.now) ? performance.now() : Date.now();
    function step(now) {
      var elapsed = Math.max(0, Number(now) - startAt);
      var t = Math.min(1, elapsed / durationMs);
      try { state.audio.volume = startVol * (1 - t); } catch (e) {}
      if (t >= 1) {
        state.fadeToken = 0;
        if (done) done();
        return;
      }
      state.fadeToken = rqf(step);
    }
    state.fadeToken = rqf(step);
  }

  function ensureState() {
    if (Module._nq_cdaudio) return Module._nq_cdaudio;
    var state = {
      audio: document.createElement('audio'),
      sourcePath: '',
      blobURL: '',
      status: 'stopped',
      resume: null,
      targetVolume: 1,
      stopTimer: 0,
      fadeToken: 0
    };
    state.audio.preload = 'auto';
    state.audio.volume = state.targetVolume;
    state.audio.onplaying = function() { state.status = 'playing'; notifyOverlayCdState(); };
    state.audio.onended = function() {
      cancelFade(state);
      revokeBlobURL(state.blobURL);
      state.blobURL = '';
      state.status = 'stopped';
      state.sourcePath = '';
      try { state.audio.volume = state.targetVolume; } catch (e2) {}
      notifyOverlayCdState();
    };
    state.audio.onerror = function() {
      cancelFade(state);
      revokeBlobURL(state.blobURL);
      state.blobURL = '';
      state.status = 'stopped';
      state.sourcePath = '';
      try { state.audio.volume = state.targetVolume; } catch (e3) {}
      notifyOverlayCdState();
    };
    state.resume = function() { if (state.status === 'loading' && state.audio.src) try { state.audio.play(); } catch (e) {} };
    document.addEventListener('click', state.resume);
    document.addEventListener('keydown', state.resume);
    Module._nq_cdaudio = state;
    return state;
  }

  function finishStopPlayback(state) {
    if (!state) return;
    cancelFade(state);
    if (state.stopTimer) {
      clearTimeout(state.stopTimer);
      state.stopTimer = 0;
    }
    try { state.audio.pause(); } catch (e) {}
    try { state.audio.currentTime = 0; } catch (e2) {}
    revokeBlobURL(state.blobURL);
    state.blobURL = '';
    state.status = 'stopped';
    state.sourcePath = '';
    try { state.audio.volume = state.targetVolume; } catch (e3) {}
  }

  function stopPlayback(state, immediate) {
    if (!state) return;
    cancelFade(state);
    if (state.stopTimer) {
      clearTimeout(state.stopTimer);
      state.stopTimer = 0;
    }
    if (immediate) {
      finishStopPlayback(state);
      return;
    }
    state.status = 'stopped';
    state.sourcePath = '';
    fadeToSilence(state, function() {
      finishStopPlayback(state);
      notifyOverlayCdState();
    });
  }

  function discardUnusedEntry(entry) {
    if (!entry) return;
    if (entry.url && entry.url.indexOf('blob:') === 0)
      revokeBlobURL(entry.url);
  }

  function continueCurrentTrack(state, entry, looping) {
    if (!state || !entry)
      return false;
    if (!state.sourcePath || state.sourcePath !== entry.path)
      return false;
    if (state.status !== 'playing' && state.status !== 'paused' && state.status !== 'loading')
      return false;
    try { state.audio.loop = !!looping; } catch (e) {}
    discardUnusedEntry(entry);
    if (state.status === 'paused') {
      cancelFade(state);
      try { state.audio.volume = state.targetVolume; } catch (e2) {}
      state.status = 'loading';
      notifyOverlayCdState();
      try { state.audio.play(); } catch (e3) {}
    } else {
      notifyOverlayCdState();
    }
    return true;
  }

  Module.nqCdInit = function() { ensureState(); notifyOverlayCdState(); return 1; };

  Module.nqCdShutdown = function() {
    var state = Module._nq_cdaudio;
    if (!state) return;
    stopPlayback(state, true);
    document.removeEventListener('click', state.resume);
    document.removeEventListener('keydown', state.resume);
    state.audio.onplaying = state.audio.onended = state.audio.onerror = null;
    try { state.audio.removeAttribute('src'); state.audio.load(); } catch (e) {}
    Module._nq_cdaudio = null;
    notifyOverlayCdState();
  };

  Module.nqCdSetVolume = function(volume) {
    var state = ensureState();
    state.targetVolume = normalizeVolume(volume);
    try { state.audio.volume = state.targetVolume; } catch (e) {}
  };

  Module.nqCdPlay = function(track, looping) {
    var state = ensureState();
    var requestedTrack = Number(track) | 0;
    if (requestedTrack <= 0) return 0;
    var entry = resolveTrackEntry(requestedTrack);
    if (!entry) return 0;
    if (continueCurrentTrack(state, entry, looping))
      return 1;
    stopPlayback(state, true);
    state.sourcePath = entry.path;
    state.status = 'loading';
    state.blobURL = entry.url.indexOf('blob:') === 0 ? entry.url : '';
    try { state.audio.loop = !!looping; if (state.audio.src !== entry.url) state.audio.src = entry.url; } catch (e) {}
    notifyOverlayCdState();
    try { state.audio.play(); } catch (e2) {}
    return 1;
  };

  Module.nqCdStop = function() { stopPlayback(Module._nq_cdaudio, false); notifyOverlayCdState(); };

  Module.nqCdPause = function() {
    var state = Module._nq_cdaudio;
    if (!state || state.status !== 'playing') return;
    state.status = 'paused';
    notifyOverlayCdState();
    fadeToSilence(state, function() {
      try { state.audio.pause(); } catch (e) {}
      try { state.audio.volume = state.targetVolume; } catch (e2) {}
    });
  };

  Module.nqCdResume = function() {
    var state = Module._nq_cdaudio;
    if (!state || state.status !== 'paused') return;
    cancelFade(state);
    try { state.audio.volume = state.targetVolume; } catch (e) {}
    state.status = 'loading';
    notifyOverlayCdState();
    try { state.audio.play(); } catch (e2) {}
  };

  Module.nqCdGetSource = function() { return Module._nq_cdaudio ? String(Module._nq_cdaudio.sourcePath || '') : ''; };
  Module.nqCdGetPlaybackState = function() { return Module._nq_cdaudio ? Module._nq_cdaudio.status : 'stopped'; };
  Module.nqCdGetRemoteTrackCount = function() { return loadRemoteManifest().length; };
  Module.nqCdGetTrackNumberFromPath = getTrackNumberFromPath;
  Module.nqCdGetRemoteTracks = function() {
    return loadRemoteManifest().map(function(entry) {
      return String(entry && entry.path || '');
    }).filter(function(path) {
      return !!path;
    });
  };
})();
