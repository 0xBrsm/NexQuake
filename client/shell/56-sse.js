// SSE "state changed" client (session-scoped). The JS shell owns one always-on
// stream to GET /events for the whole session, carrying a JSON snapshot of
// server-to-client state (DEC-021). Each snapshot:
//   - repopulates the engine hostcache from `servers` (NET_SlistBegin/
//     IngestEntry/Commit) — the `slist` command, server browser, and
//     connect-by-name all read that continuously-warm cache;
//   - on a changed `manifestGen`, refetches /gamedir (nexquakeRefreshRemoteManifest)
//     to pick up assets added/changed on the server.
// New change-sources are added as further snapshot fields, not new streams.
//
// Two transports: EventSource in the browser (same-origin, carries the session
// cookie automatically; auto-reconnects), and a fetch + ReadableStream reader
// with manual backoff for runtimes without EventSource (the headless Node test
// client). The stream starts once the wasm runtime is ready, so the initial
// snapshot is ingested rather than dropped, and in the browser it pauses while
// the tab is hidden (visibilitychange) to spare the mobile radio.

(function () {
  'use strict';

  var handle = null;            // { es } | { ctrl } while streaming, else null
  var enabled = false;          // session lifecycle armed (runtime ready)
  var reconnectTimer = null;    // pending fetch-fallback retry
  var reconnectDelay = 1000;    // backoff, reset on a healthy connection
  var RECONNECT_MAX = 15000;

  function runtimeReady() {
    return typeof Module !== 'undefined' && Module &&
      typeof Module.ccall === 'function' && Module.calledRun;
  }

  function documentHidden() {
    return typeof document !== 'undefined' && document && document.hidden === true;
  }

  // Stream only while armed and the page is visible (always, when there is no
  // document — e.g. the headless Node client).
  function shouldStream() {
    return enabled && !documentHidden();
  }

  function ccall(symbol, argTypes, args) {
    if (!runtimeReady()) return;
    try {
      Module.ccall(symbol, 'void', argTypes || [], args || []);
    } catch (e) {}
  }

  // Mirror net_ws.c's URL resolution: same-origin in the browser, an absolute
  // URL from the harness (Module.nexusBaseUrl / WEBSOCKET_URL) when there is no
  // page.
  function eventsUrl() {
    if (typeof location !== 'undefined' && location.host)
      return '/events';
    var base = (typeof Module !== 'undefined' && Module && Module.nexusBaseUrl) || '';
    if (!base && typeof Module !== 'undefined' && Module) {
      var ws = Module.WEBSOCKET_URL || Module.websocketUrl || '';
      if (ws)
        base = String(ws).replace(/^ws/, 'http').replace(/\/connect.*$/, '');
    }
    base = String(base).replace(/\/$/, '');
    return base ? base + '/events' : '/events';
  }

  // Last-seen manifest generation. The first snapshot establishes the baseline
  // (the client just booted from /gamedir, so its manifest is already current);
  // only a later change triggers a refetch.
  var lastManifestGen = null;

  function noteManifestGen(gen) {
    if (lastManifestGen === null) {
      lastManifestGen = gen; // adopt baseline, don't refetch
      return;
    }
    if (gen === lastManifestGen)
      return;
    lastManifestGen = gen;
    // Jitter the refetch so a fleet of clients doesn't stampede /gamedir and the
    // asset gateway the instant the server's manifest changes.
    var delay = Math.floor(Math.random() * 3000);
    setTimeout(function () {
      if (typeof Module !== 'undefined' && Module &&
          typeof Module.nexquakeRefreshRemoteManifest === 'function') {
        try {
          var p = Module.nexquakeRefreshRemoteManifest();
          if (p && typeof p.catch === 'function') p.catch(function () {});
        } catch (e) {}
      }
    }, delay);
  }

  function ingestSnapshot(json) {
    if (!runtimeReady())
      return;
    var data;
    try {
      data = JSON.parse(json);
    } catch (e) {
      return;
    }

    // The state snapshot also carries the client-asset manifest generation; a
    // change means files were added/changed on the server, so refetch /gamedir.
    if (data && typeof data.manifestGen === 'string')
      noteManifestGen(data.manifestGen);

    var servers = (data && Array.isArray(data.servers)) ? data.servers : [];

    ccall('NET_SlistBegin', [], []);
    for (var i = 0; i < servers.length; i++) {
      var s = servers[i] || {};
      ccall(
        'NET_SlistIngestEntry',
        ['number', 'string', 'string', 'string', 'number', 'number', 'number'],
        [
          Number(s.port) | 0,
          String(s.hostname || ''),
          String(s.map || ''),
          String(s.gamedir || ''),
          Number(s.users) | 0,
          Number(s.maxusers) | 0,
          Number(s.instances) | 0
        ]
      );
    }
    ccall('NET_SlistCommit', [], []);
  }

  // Parse one SSE record (lines until a blank line) and ingest `servers` events.
  function handleRecord(record) {
    var lines = record.split('\n');
    var ev = 'message';
    var data = '';
    for (var i = 0; i < lines.length; i++) {
      var line = lines[i];
      if (!line || line.charAt(0) === ':')
        continue; // blank or comment (heartbeat)
      if (line.indexOf('event:') === 0)
        ev = line.slice(6).trim();
      else if (line.indexOf('data:') === 0)
        data += line.slice(5).replace(/^ /, '');
    }
    if (ev === 'state' && data)
      ingestSnapshot(data);
  }

  function scheduleReconnect() {
    if (reconnectTimer !== null || !shouldStream())
      return;
    reconnectTimer = setTimeout(function () {
      reconnectTimer = null;
      if (shouldStream())
        open();
    }, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX);
  }

  // fetch + ReadableStream reader for runtimes without EventSource. Reconnects
  // with backoff on server close or transient error; a deliberate abort (close)
  // flips signal.aborted, which suppresses the retry.
  function openFetchStream(url) {
    if (typeof fetch !== 'function' || typeof AbortController === 'undefined' ||
        typeof TextDecoder === 'undefined')
      return null;
    var ctrl = new AbortController();
    fetch(url, { headers: { 'Accept': 'text/event-stream' }, signal: ctrl.signal })
      .then(function (resp) {
        if (!resp || !resp.ok || !resp.body || !resp.body.getReader) {
          if (handle && handle.ctrl === ctrl) handle = null;
          scheduleReconnect();
          return;
        }
        reconnectDelay = 1000; // healthy connection: reset backoff
        var reader = resp.body.getReader();
        var decoder = new TextDecoder();
        var buf = '';
        function pump() {
          return reader.read().then(function (r) {
            if (r.done) { // server closed the stream
              if (handle && handle.ctrl === ctrl) handle = null;
              scheduleReconnect();
              return;
            }
            buf += decoder.decode(r.value, { stream: true });
            var idx;
            while ((idx = buf.indexOf('\n\n')) >= 0) {
              handleRecord(buf.slice(0, idx));
              buf = buf.slice(idx + 2);
            }
            return pump();
          });
        }
        return pump();
      })
      .catch(function () {
        if (handle && handle.ctrl === ctrl) handle = null;
        if (!ctrl.signal.aborted)
          scheduleReconnect();
      });
    return ctrl;
  }

  function open() {
    if (handle)
      return;
    var url = eventsUrl();
    if (typeof EventSource !== 'undefined') {
      try {
        var es = new EventSource(url); // auto-reconnects; cookie-authed same-origin
        es.addEventListener('state', function (e) { ingestSnapshot(e.data); });
        handle = { es: es };
        return;
      } catch (e) { /* fall through to fetch */ }
    }
    var ctrl = openFetchStream(url);
    if (ctrl)
      handle = { ctrl: ctrl };
  }

  function close() {
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    if (!handle)
      return;
    try {
      if (handle.es)
        handle.es.close();
      else if (handle.ctrl)
        handle.ctrl.abort();
    } catch (e) {}
    handle = null;
  }

  // Open or tear down to match shouldStream() — the single place lifecycle and
  // visibility are reconciled.
  function reconcile() {
    if (shouldStream())
      open();
    else
      close();
  }

  function start() {
    if (enabled)
      return;
    enabled = true;
    reconcile();
  }

  if (typeof document !== 'undefined' && document && document.addEventListener)
    document.addEventListener('visibilitychange', reconcile);

  // Start once the runtime is ready so the first snapshot is ingested (a stream
  // opened pre-runtime would drop it, and the hub only resends on change).
  if (typeof Module === 'undefined' || !Module)
    Module = {};
  if (runtimeReady()) {
    start();
  } else {
    var priorInit = Module.onRuntimeInitialized;
    Module.onRuntimeInitialized = function () {
      if (priorInit) priorInit.call(Module);
      start();
    };
  }
})();
