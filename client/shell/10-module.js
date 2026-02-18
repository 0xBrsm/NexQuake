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
  if (text === 'preparing...')
    return true;
  if (text === 'loading...')
    return true;
  if (text === NQ_BOOTSTRAP_RUNNING_TEXT)
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
  preRun: [],
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
