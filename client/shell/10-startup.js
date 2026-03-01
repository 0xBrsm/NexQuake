var nqStoredPerModConfig = nqLoadPerModConfig();
var nqBootstrapReady = false;
var nqMainLoopStarted = false;
var nqNeedsReload = false;
var NQ_AUTOSTART_RELOAD_STORAGE_KEY = 'nexquake.autostart_after_reload';
var NQ_BOOTSTRAP_PHASE_COUNT = 3;
var NQ_BOOTSTRAP_PROGRESS_MAX = 90;
var NQ_BOOTSTRAP_PROGRESS_STEP = NQ_BOOTSTRAP_PROGRESS_MAX / NQ_BOOTSTRAP_PHASE_COUNT;
var nqBootstrapPhase = 0;
var nqAutoStartAfterReload = false;
var nqFirstStartHooksRan = false;

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

function nqIsRuntimeStatusText(text) {
  var phaseMatch;
  if (!text)
    return false;
  text = String(text).trim().toLowerCase();
  phaseMatch = text.match(/^([^(]+)\(\s*\d+(?:\.\d+)?\s*\/\s*\d+\s*\)$/);
  if (phaseMatch)
    text = phaseMatch[1].trim();
  return text === 'preparing...' ||
    text === 'loading...' ||
    text === NQ_BOOTSTRAP_RUNNING_TEXT ||
    text === 'all downloads complete.' ||
    text === 'downloading...';
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

function nqSettleStartupMonitorSize(onReady) {
  nqCaptureStartupMonitorSize(true);
  if (typeof requestAnimationFrame === 'function') {
    requestAnimationFrame(function() {
      nqCaptureStartupMonitorSize(true);
      setTimeout(function() {
        nqCaptureStartupMonitorSize(true);
        if (typeof onReady === 'function')
          onReady();
      }, 120);
    });
    return;
  }
  setTimeout(function() {
    nqCaptureStartupMonitorSize(true);
    if (typeof onReady === 'function')
      onReady();
  }, 120);
}

function nqRequestStartupFullscreen(onReady) {
  var done = false;
  function finish() {
    if (done)
      return;
    done = true;
    if (typeof onReady === 'function')
      onReady();
  }

  nqCaptureStartupMonitorSize(false);

  try {
    var el = /** @type {any} */ (document.documentElement);
    var rfs = el.requestFullscreen || el.webkitRequestFullscreen;
    var request;
    if (!rfs)
      return finish();
    try {
      request = rfs.call(el, { navigationUI: 'hide' });
    } catch (optErr) {
      request = rfs.call(el);
    }
    if (request && request.then)
      request.then(function() { nqSettleStartupMonitorSize(finish); }).catch(function(){ finish(); });
    else
      nqSettleStartupMonitorSize(finish);
  } catch (e) {
    finish();
  }

  try {
    var orient = /** @type {any} */ (screen.orientation);
    if (orient && orient.lock)
      orient.lock('landscape').catch(function(){});
  } catch (e2) {}
}

function nqSetLoaderEnterButtonEnabled() {
  if (!loaderReloadButton)
    return;
  loaderReloadButton.textContent = 'ENTER';
  loaderReloadButton.disabled = false;
  loaderReloadButton.classList.remove('hidden');
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
  if (nqAutoStartAfterReload) {
    nqAutoStartAfterReload = false;
    setTimeout(nqStartGameFromEnter, 0);
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
    var mainArgs = [];
    if (typeof Module.nexquakeBuildMainArgs === 'function') {
      try {
        mainArgs = Module.nexquakeBuildMainArgs();
      } catch (argsErr) {
        console.warn('Failed to build startup args:', argsErr);
      }
    }
    if (!Array.isArray(mainArgs))
      mainArgs = [];
    Module.callMain(mainArgs);
    nqLogBootstrapStage('wasm main initialized');
    nqWasmStartMainLoop();
    nqLogBootstrapStage('main loop started');
    if (typeof Module.hideConsole === 'function')
      Module.hideConsole();
    if (!nqFirstStartHooksRan) {
      nqFirstStartHooksRan = true;
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
  if (nqNeedsReload || nqGameStarted) {
    if (nqNeedsReload)
      nqMarkAutoStartAfterReload();
    window.location.reload();
    return;
  }
  if (!nqBootstrapReady || !nqRuntimeReady || nqMainLoopStarted)
    return;
  nqMainLoopStarted = true;
  nqGameStarted = true;
  if (Module && Module.nqTouchActive) {
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
  dataFileDownloads: (Module && Module.dataFileDownloads && typeof Module.dataFileDownloads === 'object')
    ? Module.dataFileDownloads
    : {},
  noInitialRun: true,
  nqPerModConfig: nqStoredPerModConfig === null ? false : !!nqStoredPerModConfig,
  preRun: Array.isArray(Module && Module.preRun) ? Module.preRun : [],
  postRun: Array.isArray(Module && Module.postRun) ? Module.postRun : [],
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
  },
  hideConsole: function() {
    nqNeedsReload = false;
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

if (loaderReloadButton) {
  loaderReloadButton.onclick = nqStartGameFromEnter;
}
