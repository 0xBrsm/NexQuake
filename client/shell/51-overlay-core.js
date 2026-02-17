// nq-overlay: core state, status, and CD controls
(function() {
  if (!Module || !Module.nqOverlayInstall) return;

  Module.nqOverlayInstall(function(ctx) {
    function isUserFile(path) {
      var ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase();
      return ctx.USER_EXTS.indexOf(ext) >= 0;
    }

    function isCdFile(path) {
      var ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase();
      return ctx.CD_EXTS.indexOf(ext) >= 0;
    }

    function isCdDir(dir) {
      return dir === ctx.CD_DIR;
    }

    function getBaseGameDir() {
      var baseName = (typeof nqGetBaseGameName === 'function')
        ? nqGetBaseGameName()
        : (Module.nexquakeBaseGameName || 'id1');
      baseName = String(baseName || 'id1').replace(/^\/+|\/+$/g, '') || 'id1';
      return '/' + baseName + '/';
    }

    function getDefaultDirForConfig() {
      return getBaseGameDir();
    }

    function getUploadDir() {
      if (isCdDir(ctx.currentDir))
        return ctx.CD_DIR;
      return ctx.currentDir;
    }

    function getCurrentUploadSettings() {
      if (isCdDir(ctx.currentDir)) {
        return {
          extensions: ctx.CD_EXTS,
          invalidText: 'CD tracks ' + ctx.CD_FILE_DESC + ' only',
          dir: ctx.CD_DIR,
          accept: ctx.CD_FILE_ACCEPT
        };
      }
      return {
        extensions: ctx.USER_EXTS,
        invalidText: 'Quake ' + ctx.USER_FILE_DESC + ' only',
        dir: getUploadDir(),
        accept: ctx.USER_FILE_ACCEPT
      };
    }

    function ensureCdDirs() {
      var root = ctx.CD_DIR.replace(/\/$/, '');
      var backup = (ctx.USERFS + ctx.CD_DIR).replace(/\/$/, '');
      ctx.safeMkdirTree(backup);
      try { FS.symlink(backup, root); } catch (e) {}
    }

    function setTabsOpen(open) {
      if (open && isCdDir(ctx.currentDir))
        open = false;
      ctx.tabsWrap.classList.toggle('open', open);
    }

    function setTabsOpenWidth(labels) {
      var maxWidth = 0;
      var measureCtx = ctx.tabsMeasureCtx;
      if (!labels || !labels.length) labels = ['GAME'];

      measureCtx.font = '11px monospace';
      labels.forEach(function(label) {
        maxWidth = Math.max(maxWidth, measureCtx.measureText(label).width);
      });

      ctx.panel.style.setProperty('--nq-tabs-open-width', Math.ceil(maxWidth + 26) + 'px');
    }

    var STATUS_PROGRESS = 'upload-progress';
    var STATUS_TOAST = 'toast';
    var STATUS_CD_INFO = 'cd-info';
    var STATUS_ORDER = [STATUS_PROGRESS, STATUS_TOAST, STATUS_CD_INFO];
    var statusSlots = {
      'upload-progress': null,
      'toast': null,
      'cd-info': null
    };

    function normalizeStatusKey(key) {
      key = String(key || '').trim();
      if (key === STATUS_PROGRESS || key === STATUS_CD_INFO || key === STATUS_TOAST)
        return key;
      return STATUS_TOAST;
    }

    function appendStatusSlot(key) {
      var entry = statusSlots[key];
      var item;
      if (!entry) return;
      item = document.createElement('div');
      item.className = 'nq-status-message ' + entry.level;
      item.textContent = entry.text;
      ctx.uploadError.appendChild(item);
    }

    function renderStatusMessages() {
      ctx.uploadError.innerHTML = '';
      STATUS_ORDER.forEach(appendStatusSlot);
    }

    function clearStatusMessage(key) {
      key = String(key || '').trim();
      if (!key) {
        STATUS_ORDER.forEach(function(slotKey) {
          var entry = statusSlots[slotKey];
          if (entry && entry.timeoutId)
            clearTimeout(entry.timeoutId);
          statusSlots[slotKey] = null;
        });
        renderStatusMessages();
        return;
      }

      key = normalizeStatusKey(key);
      var entry = statusSlots[key];
      if (!entry) return;
      if (entry.timeoutId) clearTimeout(entry.timeoutId);
      statusSlots[key] = null;
      renderStatusMessages();
    }

    function showStatusMessage(level, msg, clearAfterMs, sticky, options) {
      var text = String(msg || '').trim();
      var opts = options || {};
      var key = normalizeStatusKey(opts.key || STATUS_TOAST);
      var timeoutMs;

      if (!text) {
        clearStatusMessage(key);
        return;
      }

      clearStatusMessage(key);
      var entry = statusSlots[key] = {
        level: level,
        text: text,
        timeoutId: 0
      };

      renderStatusMessages();

      timeoutMs = Number(clearAfterMs) | 0;
      if (!sticky && timeoutMs > 0) {
        entry.timeoutId = setTimeout(function() {
          if (statusSlots[key] === entry)
            clearStatusMessage(key);
        }, timeoutMs);
      }
    }

    function showInfoMessage(msg, clearAfterMs, sticky, options) {
      showStatusMessage('info', msg, clearAfterMs, sticky, options);
    }

    function showWarningMessage(msg, clearAfterMs, sticky, options) {
      showStatusMessage('warning', msg, clearAfterMs, sticky, options);
    }

    function showErrorMessage(msg, clearAfterMs, sticky, options) {
      showStatusMessage('error', msg, clearAfterMs, sticky, options);
    }

    var pendingConfirmResolve = null;

    function closeConfirmModal(result) {
      var resolve = pendingConfirmResolve;
      if (!resolve)
        return false;
      pendingConfirmResolve = null;
      ctx.confirm.classList.remove('open');
      ctx.confirm.setAttribute('aria-hidden', 'true');
      resolve(!!result);
      return true;
    }

    function confirmAsync(message, okText) {
      var text = String(message || '').trim();
      if (!text)
        return Promise.resolve(false);
      if (!ctx.confirm || !ctx.confirmText || !ctx.confirmOk || !ctx.confirmCancel)
        return Promise.resolve(false);
      closeConfirmModal(false);
      clearStatusMessage(STATUS_TOAST);
      ctx.confirmText.textContent = text;
      ctx.confirmOk.textContent = String(okText || 'confirm').trim() || 'confirm';
      ctx.confirm.classList.add('open');
      ctx.confirm.setAttribute('aria-hidden', 'false');
      return new Promise(function(resolve) {
        pendingConfirmResolve = resolve;
      });
    }

    if (ctx.confirm) {
      ctx.confirm.addEventListener('mousedown', function(ev) {
        if (ev.target === ctx.confirm) {
          ev.preventDefault();
          closeConfirmModal(false);
        }
      });
    }
    if (ctx.confirmCancel) {
      ctx.confirmCancel.onclick = function(ev) {
        ev.preventDefault();
        closeConfirmModal(false);
      };
    }
    if (ctx.confirmOk) {
      ctx.confirmOk.onclick = function(ev) {
        ev.preventDefault();
        closeConfirmModal(true);
      };
    }

    var CD_PLAY_ICON = '<svg viewBox="0 0 24 24" fill="currentColor"><polygon points="8,5 20,12 8,19"/></svg>';
    var CD_PAUSE_ICON = '<svg viewBox="0 0 24 24" fill="currentColor"><rect x="7" y="5" width="4" height="14"/><rect x="13" y="5" width="4" height="14"/></svg>';

    function syncCdPauseToggleButton() {
      var showPlay;
      var icon;
      if (!ctx.cdPauseToggleBtn)
        return;
      showPlay = !ctx.cdPlaying || ctx.cdPaused;
      ctx.cdPauseToggleBtn.classList.toggle('cd-paused', showPlay);
      icon = showPlay ? CD_PLAY_ICON : CD_PAUSE_ICON;
      if (ctx.cdPauseToggleBtn.innerHTML !== icon)
        ctx.cdPauseToggleBtn.innerHTML = icon;
      ctx.cdPauseToggleBtn.title = showPlay ? 'cd resume' : 'cd pause';
      ctx.cdPauseToggleBtn.setAttribute('aria-label', showPlay ? 'cd resume' : 'cd pause');
    }

    function getCdRuntimeState() {
      var state = 'stopped';
      var path = '';
      var file = '';

      try {
        if (Module && typeof Module.nqCdGetPlaybackState === 'function')
          state = String(Module.nqCdGetPlaybackState() || 'stopped').toLowerCase();
      } catch (e) {}

      if (state !== 'playing' && state !== 'paused' && state !== 'loading' && state !== 'stopped')
        state = 'stopped';

      try {
        if (Module && typeof Module.nqCdGetSource === 'function')
          path = String(Module.nqCdGetSource() || '');
      } catch (e2) {}

      file = path ? path.split(/[\\/]/).pop() : '';
      return { state: state, file: file, path: path };
    }

    function getCdTrackNumber(path) {
      if (!Module.nqCdGetTrackNumberFromPath)
        return 0;
      return Number(Module.nqCdGetTrackNumberFromPath(path) || 0) | 0;
    }

    function isGameLoaded() {
      return !!nqGameStarted;
    }

    function syncCdRuntimeState() {
      var runtime = getCdRuntimeState();

      if (!ctx.cdEnabled) {
        ctx.cdPlaying = false;
        ctx.cdPaused = false;
      } else {
        ctx.cdPlaying = runtime.state === 'playing' || runtime.state === 'paused' || runtime.state === 'loading';
        ctx.cdPaused = runtime.state === 'paused';
      }

      syncCdPauseToggleButton();
      syncCdInfoMessage(runtime);
      return runtime;
    }

    function syncCdInfoMessage(runtime) {
      var text;
      var level = 'info';
      if (isCdDir(ctx.currentDir)) {
        if (ctx.cdInfoText || ctx.cdInfoLevel) {
          ctx.cdInfoText = '';
          ctx.cdInfoLevel = '';
          clearStatusMessage(STATUS_CD_INFO);
        }
        return;
      }

      if (!ctx.cdEnabled)
        text = '';
      else if (runtime.state === 'paused' || runtime.state === 'playing' || runtime.state === 'loading') {
        text = runtime.file ? ('CD: ' + runtime.file) : 'CD playing';
        if (runtime.state === 'playing' || runtime.state === 'loading')
          level = 'warning';
      } else
        text = '';

      if (text === ctx.cdInfoText && level === ctx.cdInfoLevel)
        return;
      ctx.cdInfoText = text;
      ctx.cdInfoLevel = level;

      if (!text)
        clearStatusMessage(STATUS_CD_INFO);
      else
        showStatusMessage(level, text, 0, true, {
          key: STATUS_CD_INFO,
          prepend: false
        });
    }

    function syncCdButtonsState() {
      var disabled;
      var command;
      ctx.cdButtons.forEach(function(btn) {
        command = btn.getAttribute('data-cd-command');
        disabled = ctx.uploadBusy || (!ctx.cdEnabled && command !== 'toggle') || (!isGameLoaded() && command === 'toggle');
        btn.disabled = disabled;
      });
    }

    function setUploadBusyState(busy) {
      ctx.uploadBusy = !!busy;
      ctx.upload.disabled = ctx.uploadBusy;
      syncCdButtonsState();
    }

    function syncCdModeUI() {
      var inCdMode = isCdDir(ctx.currentDir);
      var showEjectFocus;
      syncCdRuntimeState();
      ctx.panel.classList.toggle('cd-mode', inCdMode);
      showEjectFocus = inCdMode && ctx.cdEnabled;
      if (ctx.cdEjectBtn) {
        ctx.cdEjectBtn.classList.toggle('active', showEjectFocus);
        ctx.cdEjectBtn.setAttribute('aria-pressed', showEjectFocus ? 'true' : 'false');
      }
      if (inCdMode)
        setTabsOpen(false);
      if (!inCdMode) {
        syncCdRuntimeState();
      }
    }

    function setCdEnabled(enabled, persistPreference) {
      ctx.cdEnabled = !!enabled;
      if (persistPreference !== false) {
        ctx.cdPreferredEnabled = ctx.cdEnabled;
        if (typeof nqSaveCdEnabled === 'function')
          nqSaveCdEnabled(ctx.cdPreferredEnabled);
      }
      if (ctx.cdRow)
        ctx.cdRow.classList.toggle('cd-off', !ctx.cdEnabled);
      if (ctx.cdPowerBtn) {
        ctx.cdPowerBtn.setAttribute('aria-pressed', ctx.cdEnabled ? 'true' : 'false');
        ctx.cdPowerBtn.title = ctx.cdEnabled ? 'cd off' : 'cd on';
      }
      syncCdRuntimeState();
      syncCdButtonsState();
      if (ctx.panel && ctx.panel.classList.contains('open') && isCdDir(ctx.currentDir))
        ctx.refresh();
    }

    function syncCdEnabledFromPreference() {
      setCdEnabled(isGameLoaded() && !!ctx.cdPreferredEnabled, false);
    }

    function applyCdPreferenceToGame() {
      var command;
      if (!isGameLoaded())
        return;
      syncCdEnabledFromPreference();
      command = ctx.cdPreferredEnabled ? 'cd on' : 'cd off';
      try {
        Module.ccall('NexQuake_ExecCommand', 'void', ['string'], [command]);
      } catch (e) {
        console.error('Failed to apply CD preference:', e);
      }
    }

    function toggleCdView() {
      if (isCdDir(ctx.currentDir)) {
        ctx.currentDir = (ctx.nonCdDir && !isCdDir(ctx.nonCdDir)) ? ctx.nonCdDir : getDefaultDirForConfig();
        ctx.nonCdDir = null;
        ctx.refresh();
        return;
      }

      ctx.nonCdDir = ctx.currentDir;
      ensureCdDirs();
      setTabsOpen(false);
      ctx.currentDir = ctx.CD_DIR;
      ctx.refresh();
    }

    function syncConfigToggle() {
      var isGlobal = !Module.nqPerModConfig;
      var hoverText = isGlobal ? 'Global cfgs enabled' : 'Global cfgs disabled';
      if (!ctx.configGlobalBtn) return;
      ctx.configGlobalBtn.classList.toggle('active', isGlobal);
      ctx.configGlobalBtn.setAttribute('aria-pressed', isGlobal ? 'true' : 'false');
      ctx.configGlobalBtn.title = hoverText;
      ctx.configGlobalBtn.setAttribute('aria-label', hoverText);
    }

    async function runCdCommand(cdCommand, trackOverride) {
      var command = String(cdCommand || '').trim().toLowerCase();
      var finalCommand;
      if (!command) return;
      if (!isGameLoaded()) {
        syncCdEnabledFromPreference();
        return;
      }

      if (command === 'eject') {
        if (ctx.uploadBusy)
          return;
        toggleCdView();
        return;
      }

      if (command === 'toggle')
        command = ctx.cdEnabled ? 'off' : 'on';
      if (command === 'pause-toggle')
        command = (!ctx.cdPlaying || ctx.cdPaused) ? 'resume' : 'pause';

      if (command === 'loop') {
        var track = Number(trackOverride) | 0;
        if (track <= 0) {
          showErrorMessage('No track number to loop', 2000);
          return;
        }
        finalCommand = 'cd loop ' + track;
      } else {
        finalCommand = 'cd ' + command;
      }

      try {
        Module.ccall('NexQuake_ExecCommand', 'void', ['string'], [finalCommand]);
      } catch (e) {
        showErrorMessage('Failed to run: ' + finalCommand, 3000);
        console.error('Exec command failed:', e);
        return;
      }

      if (command === 'on')
        setCdEnabled(true);
      if (command === 'off')
        setCdEnabled(false);
      syncCdRuntimeState();

      if (command === 'loop' || command === 'resume' || command === 'pause') {
        setTimeout(function() {
          syncCdRuntimeState();
        }, 0);
        return;
      }

      if (command === 'off' || command === 'stop' || command === 'on')
        return;

      showInfoMessage(finalCommand, 1200);
    }

    Object.assign(ctx, {
      isUserFile,
      isCdFile,
      isCdDir,
      getBaseGameDir,
      getDefaultDirForConfig,
      getUploadDir,
      getCurrentUploadSettings,
      ensureCdDirs,
      setTabsOpen,
      setTabsOpenWidth,
      showInfoMessage,
      showWarningMessage,
      showErrorMessage,
      confirmAsync,
      closeConfirmModal,
      clearStatusMessage,
      CD_PLAY_ICON,
      CD_PAUSE_ICON,
      getCdTrackNumber,
      getCdRuntimeState,
      isGameLoaded,
      setUploadBusyState,
      syncCdModeUI,
      syncCdEnabledFromPreference,
      applyCdPreferenceToGame,
      setCdEnabled,
      syncConfigToggle,
      runCdCommand
    });
    Module.nqOverlayOnCdStateChange = function() {
      var runtime = syncCdRuntimeState();
      if (ctx.panel && ctx.panel.classList.contains('open') && isCdDir(ctx.currentDir)) {
        if (!ctx.updateCdTrackRows || !ctx.updateCdTrackRows(runtime))
          ctx.refresh();
      }
      return runtime;
    };

    if (ctx.configGlobalBtn) {
      ctx.configGlobalBtn.addEventListener('click', function() {
        Module.nqPerModConfig = !Module.nqPerModConfig;
        nqSavePerModConfig(Module.nqPerModConfig);
        if (Module.nqPerModConfig)
          showInfoMessage('Switched to per-mod cfg files.', 2200);
        else
          showInfoMessage('Switched to shared cfg files for all mods.', 2400);
        ctx.currentDir = getDefaultDirForConfig();
        if (ctx.panel.classList.contains('open')) ctx.refresh();
      });
    }
  });
})();
