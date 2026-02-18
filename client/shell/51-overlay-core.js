// nq-overlay: core state, status, and mod config
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
      var backup = String(ctx.CD_USERFS || '').replace(/\/$/, '');
      if (!backup)
        return;
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
    function setUploadBusyState(busy) {
      ctx.uploadBusy = !!busy;
      ctx.upload.disabled = ctx.uploadBusy;
      if (ctx.syncCdButtonsState)
        ctx.syncCdButtonsState();
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
      setUploadBusyState,
      syncConfigToggle
    });

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
