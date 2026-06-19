var nqStoredPerModConfig = nqLoadPerModConfig();
var nqBootstrapReady = false;
var nqMainLoopStarted = false;
var NQ_BOOTSTRAP_PHASE_COUNT = 3;
var NQ_BOOTSTRAP_PROGRESS_MAX = 90;
var NQ_BOOTSTRAP_PROGRESS_STEP = NQ_BOOTSTRAP_PROGRESS_MAX / NQ_BOOTSTRAP_PHASE_COUNT;
var nqBootstrapPhase = 0;
var nqFirstStartHooksRan = false;

function nqLogBootstrapStage(text) {
  if (text)
    console.info('[nq-loader] ' + text);
}

function nqSetBootstrapProgress(percent) {
  if (!loaderProgressBar)
    return;
  if (!Number.isFinite(percent))
    percent = 0;
  if (percent < 0) percent = 0;
  if (percent > NQ_BOOTSTRAP_PROGRESS_MAX) percent = NQ_BOOTSTRAP_PROGRESS_MAX;
  loaderProgressBar.style.width = Math.round(percent) + '%';
}

function nqGetBootstrapPhaseText(phase) {
  if (NQ_BOOTSTRAP_PHASE_TEXT && NQ_BOOTSTRAP_PHASE_TEXT[phase])
    return NQ_BOOTSTRAP_PHASE_TEXT[phase];
  return '';
}

function nqSetBootstrapPhase(phase) {
  var phaseText;

  if (phase <= nqBootstrapPhase)
    return;
  nqBootstrapPhase = phase;
  nqSetBootstrapProgress((phase - 1) * NQ_BOOTSTRAP_PROGRESS_STEP);
  phaseText = nqGetBootstrapPhaseText(phase);
  if (!phaseText)
    return;
  if (loaderStatusElement)
    loaderStatusElement.textContent = phaseText;
  nqLogBootstrapStage(phaseText + ' (' + Math.round((phase - 1) * NQ_BOOTSTRAP_PROGRESS_STEP) + '%)');
}
nqSetBootstrapPhase(1);

function nqSetBootstrapRunning() {
  nqSetBootstrapProgress(NQ_BOOTSTRAP_PROGRESS_MAX);
  if (loaderStatusElement)
    loaderStatusElement.textContent = NQ_BOOTSTRAP_RUNNING_TEXT;
  nqLogBootstrapStage(NQ_BOOTSTRAP_RUNNING_TEXT + ' (' + NQ_BOOTSTRAP_PROGRESS_MAX + '%)');
}

function nqCaptureStartupMonitorSize(preferViewport) {
  var sw = 0;
  var sh = 0;
  var dpr = window.devicePixelRatio || 1;
  var useViewport = preferViewport !== false;
  var moduleRef = (typeof Module !== 'undefined' && Module) ? Module : (window.Module = window.Module || {});

  if (useViewport) {
    try {
      if (window.visualViewport && window.visualViewport.width && window.visualViewport.height) {
        sw = Math.max(window.visualViewport.width, window.visualViewport.height);
        sh = Math.min(window.visualViewport.width, window.visualViewport.height);
      } else if (window.innerWidth && window.innerHeight) {
        sw = Math.max(window.innerWidth, window.innerHeight);
        sh = Math.min(window.innerWidth, window.innerHeight);
      }
    } catch (e) {}
    if (!(sw > 0 && sh > 0))
      useViewport = false;
  }

  if (!useViewport) {
    try {
      if (window.screen && window.screen.width && window.screen.height) {
        sw = Math.max(window.screen.width, window.screen.height);
        sh = Math.min(window.screen.width, window.screen.height);
      } else if (window.visualViewport && window.visualViewport.width && window.visualViewport.height) {
        sw = Math.max(window.visualViewport.width, window.visualViewport.height);
        sh = Math.min(window.visualViewport.width, window.visualViewport.height);
      } else if (window.innerWidth && window.innerHeight) {
        sw = Math.max(window.innerWidth, window.innerHeight);
        sh = Math.min(window.innerWidth, window.innerHeight);
      }
    } catch (e2) {}
  }

  if (!(sw > 0 && sh > 0))
    return;

  moduleRef.nqStartupMonitorWidth = Math.max(1, Math.round(sw * dpr));
  moduleRef.nqStartupMonitorHeight = Math.max(1, Math.round(sh * dpr));
}

function nqRequestStartupFullscreen(onReady) {
  var done = false;
  function settleAndFinish() {
    function captureAndFinish() {
      nqCaptureStartupMonitorSize(true);
      finish();
    }
    if (typeof requestAnimationFrame === 'function') {
      requestAnimationFrame(function() {
        setTimeout(captureAndFinish, 120);
      });
      return;
    }
    setTimeout(captureAndFinish, 120);
  }

  function finish() {
    if (done)
      return;
    done = true;
    if (typeof onReady === 'function')
      onReady();
  }

  nqCaptureStartupMonitorSize(false);

  try {
    var request;
    if (Module && typeof Module.nqRequestFullscreen === 'function')
      request = Module.nqRequestFullscreen();
    if (request && request.then)
      request.then(settleAndFinish).catch(function(){ finish(); });
    else
      settleAndFinish();
  } catch (e) {
    finish();
  }
}

function nqSetLoaderEnterButtonEnabled() {
  if (!loaderReloadButton)
    return;
  loaderReloadButton.textContent = 'ENTER';
  loaderReloadButton.disabled = false;
  loaderReloadButton.classList.remove('hidden');
}

function nqReloadPageAfterQuit() {
  var done = false;
  function finish() {
    if (done)
      return;
    done = true;
    window.location.reload();
  }

  try {
    var exitFullscreen = document.exitFullscreen || document.webkitExitFullscreen;
    if (!exitFullscreen ||
        (!document.fullscreenElement && !document.webkitFullscreenElement)) {
      finish();
      return;
    }
    var request = exitFullscreen.call(document);
    if (request && request.then) {
      request.then(function() {
        setTimeout(finish, 0);
      }).catch(function() {
        setTimeout(finish, 0);
      });
      return;
    }
  } catch (e) {}

  setTimeout(finish, 0);
}

function nqSyncOverlayCdEnabled(fallbackEnabled) {
  var overlayCtx;
  try {
    overlayCtx = Module && Module.nqOverlayCtx;
    if (!overlayCtx)
      return;
    if (typeof overlayCtx.syncCdEnabledFromPreference === 'function')
      overlayCtx.syncCdEnabledFromPreference();
    else if (typeof overlayCtx.setCdEnabled === 'function')
      overlayCtx.setCdEnabled(!!fallbackEnabled, false);
  } catch (e) {}
}

function nqRefreshOverlayAfterStart() {
  var overlayCtx;
  try {
    overlayCtx = Module && Module.nqOverlayCtx;
    if (!overlayCtx)
      return;
    if (typeof overlayCtx.applyCdPreferenceToGame === 'function')
      overlayCtx.applyCdPreferenceToGame();
    if (typeof overlayCtx.refresh === 'function')
      overlayCtx.refresh();
  } catch (overlayErr) {
    console.warn('Overlay refresh failed:', overlayErr);
  }
}

function nqShowEnterButton() {
  nqBootstrapReady = true;
  nqSetOverlayToggleVisible(true);
  nqSyncOverlayCdEnabled(false);
  if (loaderElement)
    loaderElement.classList.add('enter-mode');
  if (loaderStatusElement)
    loaderStatusElement.textContent = '';
  nqSetLoaderEnterButtonEnabled();
  nqLogBootstrapStage('enter ready (100%)');
  if (nqRuntimeReady && new URLSearchParams(window.location.search).has('autostart')) {
    var isTouch = (window.matchMedia && window.matchMedia('(pointer: coarse)').matches) ||
                  ('ontouchstart' in window && screen.width <= 1024);
    if (!isTouch)
      nqStartGameFromEnter();
  }
}

function nqStartGameRuntime() {
  if (loaderReloadButton) {
    loaderReloadButton.textContent = 'STARTING...';
    loaderReloadButton.disabled = true;
  }
  nqSyncOverlayCdEnabled(true);
  nqLogBootstrapStage('starting game client...');
  try {
    if (typeof Module.callMain !== 'function')
      throw new Error('Module.callMain is not available');
    Module.callMain(Array.isArray(Module.nexquakeMainArgs) ? Module.nexquakeMainArgs : []);
    nqLogBootstrapStage('wasm main initialized');
    nqWasmStartMainLoop();
    nqLogBootstrapStage('main loop started');
    if (typeof Module.hideConsole === 'function')
      Module.hideConsole();
    if (!nqFirstStartHooksRan) {
      nqFirstStartHooksRan = true;
      // Host_Init just ran the client-side precaches (CL_InitTEnts temp-entity
      // sounds, S_Init ambient sounds) that no server sound_precache list carries
      // and CL_Prefetch never sees. Warm them in the background now — the VFS
      // manifest is already mounted — so their first in-game play (e.g. a rocket
      // explosion on a frag) doesn't trigger a blocking synchronous fetch.
      nqWasmPrefetchKnownSounds();
      if (Module.nexquakeAutoSMenuOnFirstLoad === true) {
        if (!nqWasmExecCommand('smenu'))
          console.info('Auto-open server search menu skipped: wasm command bridge unavailable');
      }
    }
    nqRefreshOverlayAfterStart();
  } catch (err) {
    console.error('Failed to start main loop:', err);
    nqMainLoopStarted = false;
    nqGameStarted = false;
    nqSetLoaderEnterButtonEnabled();
    nqSyncOverlayCdEnabled(false);
  }
}

function nqStartGameFromEnter() {
  if (nqGameStarted) {
    window.location.reload();
    return;
  }
  if (!nqBootstrapReady || !nqRuntimeReady || nqMainLoopStarted)
    return;
  nqMainLoopStarted = true;
  nqGameStarted = true;
  if (Module && Module.nexquakeTouchEnabled !== false && Module.nqTouchActive) {
    nqRequestStartupFullscreen(nqStartGameRuntime);
    return;
  }
  nqCaptureStartupMonitorSize(false);
  nqStartGameRuntime();
}

Module = Object.assign(Module || {}, {
  nexquakeBaseGameName: NEXQUAKE_GAMENAME,
  nexquakeAutoSMenuOnFirstLoad: false,
  nexquakeSendArgs: [],
  nexquakeURLArgs: false,
  nexquakeTouchEnabled: true,
  nqQuitInProgress: false,
  dataFileDownloads: (Module && Module.dataFileDownloads && typeof Module.dataFileDownloads === 'object')
    ? Module.dataFileDownloads
    : {},
  noInitialRun: true,
  nqPerModConfig: nqStoredPerModConfig === null ? false : !!nqStoredPerModConfig,
  preRun: Array.isArray(Module && Module.preRun) ? Module.preRun : [],
  postRun: Array.isArray(Module && Module.postRun) ? Module.postRun : [],
  print: (function() {
    outputElement.value = ''; // clear browser cache
    var OUTPUT_CAP = 64 * 1024; // unbounded growth re-copies the whole string per line
    var pending = [];
    var flushTimer = 0;
    // Batch DOM writes: boot and level loads print hundreds of lines in
    // bursts, and a per-line value+scrollHeight round trip costs a string
    // copy plus a forced layout each. setTimeout (not rAF) so the flush
    // still runs in backgrounded tabs.
    function flush() {
      flushTimer = 0;
      var next = outputElement.value + pending.join("\n") + "\n";
      pending.length = 0;
      if (next.length > OUTPUT_CAP) next = next.slice(next.length - OUTPUT_CAP);
      outputElement.value = next;
      outputElement.scrollTop = outputElement.scrollHeight; // focus on bottom
    }
    return function(text) {
      if (arguments.length > 1) text = Array.prototype.slice.call(arguments).join(' ');
      console.log(text);
      pending.push(text);
      if (!flushTimer) flushTimer = setTimeout(flush, 50);
    };
  })(),
  canvas: canvasElement,
  setStatus: function(text) {
    var statusText;
    var normalizedStatus;
    var phaseMatch;
    if (!loaderStatusElement)
      return;
    statusText = String(text || '').trim();
    if (!statusText)
      return;
    normalizedStatus = statusText.toLowerCase();
    phaseMatch = normalizedStatus.match(/^([^(]+)\(\s*\d+(?:\.\d+)?\s*\/\s*\d+\s*\)$/);
    if (phaseMatch)
      normalizedStatus = phaseMatch[1].trim();
    if (normalizedStatus === 'preparing...' ||
        normalizedStatus === 'loading...' ||
        normalizedStatus === NQ_BOOTSTRAP_RUNNING_TEXT ||
        normalizedStatus === 'all downloads complete.' ||
        normalizedStatus === 'downloading...')
      return;
    loaderStatusElement.textContent = statusText;
  },
  nqShowReloadScreen: function() {
    try {
      if (document.pointerLockElement && document.exitPointerLock)
        document.exitPointerLock();
    } catch (e) {}
    nqMainLoopStarted = false;
    nqGameStarted = false;
    if (loaderProgressBar)
      loaderProgressBar.style.width = '0%';
    if (loaderStatusElement)
      loaderStatusElement.textContent = '';
    nqSetLoaderEnterButtonEnabled();
    if (loaderElement) {
      loaderElement.classList.remove('hidden');
      loaderElement.classList.add('enter-mode');
    }
    nqSetOverlayToggleVisible(true);
    nqSyncOverlayCdEnabled(false);
    if (canvasElement)
      canvasElement.style.display = 'none';
    if (outputElement)
      outputElement.style.display = 'none';
    try {
      if (Module.nqOverlayCtx && Module.nqOverlayCtx.setPanelOpen)
        Module.nqOverlayCtx.setPanelOpen(false);
    } catch (e) {}
    nqReloadPageAfterQuit();
  },
  hideConsole: function() {
    if (loaderElement)
      loaderElement.classList.add('hidden');
    outputElement.style.display = 'none';
    var ar = canvasElement.width / canvasElement.height;
    if (Number.isFinite(ar) && ar > 0)
      canvasElement.style.setProperty('--nq-ar', String(ar));
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
});

(function() {
  var NQ_DEFAULT_HEAP_MB = 64;
  var NQ_WASM_OVERHEAD_MB = 32;
  var NQ_WASM_MIN_MB = 64;
  var NQ_WASM_MAX_MB = 2048;
  var NQ_WASM_PAGE_BYTES = 64 * 1024;

  function parseURLArgs() {
    var out = [];
    var tokens = window.location.search.length > 1 ? window.location.search.slice(1).split('&') : [];
    var i;
    var token;

    for (i = 0; i < tokens.length; i++) {
      token = tokens[i];
      if (!token)
        continue;
      if (token.indexOf('=') !== -1)
        continue;
      try {
        token = decodeURIComponent(token);
      } catch (e) {}
      token = String(token || '').trim();
      if (!token)
        continue;
      out.push(token);
    }

    return out;
  }

  function buildMainArgs() {
    var out = Module.nexquakeURLArgs === true ? parseURLArgs() : [];
    var source = Array.isArray(Module.nexquakeSendArgs) ? Module.nexquakeSendArgs : [];
    var i;
    var token;

    for (i = 0; i < source.length; i++) {
      token = String(source[i] || '').trim();
      if (!token)
        continue;
      out.push(token);
    }

    return out;
  }

  function parseMemArgMB(args) {
    var pnum = args.indexOf('-mem');
    var value = parseInt(pnum >= 0 ? args[pnum + 1] : '', 10);
    return Number.isFinite(value) && value > 0 ? value : NQ_DEFAULT_HEAP_MB;
  }

  function configureStartupMemory(args) {
    var initialMB;
    var initialPages;
    var maximumPages;

    if (Module.wasmMemory || typeof WebAssembly === 'undefined' || typeof WebAssembly.Memory !== 'function')
      return;

    // Keep a fixed wasm-side budget above Quake's own -mem heap.
    initialMB = Math.max(NQ_WASM_MIN_MB,
      Math.min(parseMemArgMB(args) + NQ_WASM_OVERHEAD_MB, NQ_WASM_MAX_MB));
    initialPages = Math.ceil((initialMB * 1024 * 1024) / NQ_WASM_PAGE_BYTES);
    maximumPages = Math.ceil((NQ_WASM_MAX_MB * 1024 * 1024) / NQ_WASM_PAGE_BYTES);

    Module.nexquakeStartupMemoryMB = initialPages * NQ_WASM_PAGE_BYTES / (1024 * 1024);
    Module.wasmMemory = new WebAssembly.Memory({
      initial: initialPages,
      maximum: maximumPages
    });
  }

  function applyClientConfig(config) {
    config = config && typeof config === 'object' ? config : {};
    Module.nexquakeAutoSMenuOnFirstLoad = config.smenuOnFirstLoad === true;
    Module.nexquakeSendArgs = Array.isArray(config.sendArgs) ? config.sendArgs.slice() : [];
    Module.nexquakeURLArgs = config.urlArgs === true;
    // Public OIDC params for client-side `rcon login` PKCE, or null when Nexus
    // isn't OIDC-configured (the rcon shell then uses password / edge login).
    Module.nexquakeOIDC = (config.oidc && typeof config.oidc === 'object') ? config.oidc : null;
    Module.nexquakeMainArgs = buildMainArgs();
    Module.nexquakeTouchEnabled = Module.nexquakeMainArgs.indexOf('-notouch') === -1;
    configureStartupMemory(Module.nexquakeMainArgs);
    if (typeof Module.nqTouchControlsRefresh === 'function')
      Module.nqTouchControlsRefresh();
  }

  Module.nexquakeApplyClientConfig = applyClientConfig;
  Module.nexquakeApplyClientConfig((nqTryLoadStartBundleSync() || {}).client);
})();

if (loaderReloadButton) {
  loaderReloadButton.onclick = nqStartGameFromEnter;
}
