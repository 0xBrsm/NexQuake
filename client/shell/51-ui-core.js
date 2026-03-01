// nq-ui: core state, status, and mod config
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

    var getDefaultDirForConfig = getBaseGameDir;
    function getUploadDir() { return ctx.currentDir; }

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
      return Object.prototype.hasOwnProperty.call(statusSlots, key) ? key : STATUS_TOAST;
    }

    function renderStatusMessages() {
      ctx.uploadError.innerHTML = '';
      STATUS_ORDER.forEach(function(key) {
        var entry = statusSlots[key];
        var item;
        if (!entry) return;
        item = document.createElement('div');
        item.className = 'nq-status-message ' + entry.level;
        item.textContent = entry.text;
        ctx.uploadError.appendChild(item);
      });
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

    function showInfoMessage(msg, ms, sticky, opts) { showStatusMessage('info', msg, ms, sticky, opts); }
    function showWarningMessage(msg, ms, sticky, opts) { showStatusMessage('warning', msg, ms, sticky, opts); }
    function showErrorMessage(msg, ms, sticky, opts) { showStatusMessage('error', msg, ms, sticky, opts); }

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
      var hoverText = isGlobal ? 'global cfgs enabled' : 'global cfgs disabled';
      if (!ctx.configGlobalBtn) return;
      ctx.configGlobalBtn.classList.toggle('active', isGlobal);
      ctx.configGlobalBtn.setAttribute('aria-pressed', isGlobal ? 'true' : 'false');
      ctx.configGlobalBtn.title = hoverText;
      ctx.configGlobalBtn.setAttribute('aria-label', hoverText);
    }

    function syncFooterLayout() {
      var row;
      var rowWidth;
      var gapPx;
      var requiredWidth;
      var hasJoinCode;
      var isCompact;
      var measureCtx;
      var versionStyle;
      var versionText;
      var versionWidth;
      var letterSpacing;
      var spacingExtra = 0;
      if (!ctx.brandingRow || !ctx.branding || !ctx.joinCodeBtn || !ctx.versionEl)
        return;

      row = ctx.brandingRow;
      hasJoinCode = !ctx.joinCodeBtn.hidden;
      row.classList.toggle('nq-no-join-code', !hasJoinCode);
      if (!hasJoinCode) {
        row.classList.remove('nq-branding-compact');
        return;
      }

      rowWidth = row.clientWidth;
      if (rowWidth <= 0)
        return;

      gapPx = parseFloat(window.getComputedStyle(row).columnGap || '0') || 0;
      measureCtx = ctx.tabsMeasureCtx;
      versionStyle = window.getComputedStyle(ctx.versionEl);
      versionText = ctx.versionEl.textContent || '';
      versionWidth = Math.ceil(ctx.versionEl.scrollWidth) || 0;
      if (measureCtx && versionText) {
        measureCtx.font = versionStyle.font || [
          versionStyle.fontStyle,
          versionStyle.fontVariant,
          versionStyle.fontWeight,
          versionStyle.fontSize,
          versionStyle.fontFamily
        ].join(' ');
        versionWidth = Math.ceil(measureCtx.measureText(versionText).width);
        letterSpacing = parseFloat(versionStyle.letterSpacing || '');
        if (Number.isFinite(letterSpacing) && letterSpacing !== 0 && versionText.length > 1)
          spacingExtra = Math.ceil(letterSpacing * (versionText.length - 1));
      }
      requiredWidth =
        Math.ceil(ctx.branding.getBoundingClientRect().width) +
        Math.ceil(ctx.joinCodeBtn.getBoundingClientRect().width) +
        versionWidth +
        spacingExtra +
        Math.ceil(gapPx * 2);

      isCompact = row.classList.contains('nq-branding-compact');
      if (!isCompact && requiredWidth > rowWidth) {
        row.classList.add('nq-branding-compact');
        return;
      }
      if (isCompact && requiredWidth <= (rowWidth - 12))
        row.classList.remove('nq-branding-compact');
    }

    function syncJoinCode() {
      var port;
      var label;
      if (!ctx.joinCodeBtn || !ctx.joinCodeValue)
        return;
      port = Number(nqWasmGetConnectedServerListenPort()) | 0;
      if (port < 1 || port > 65535)
        port = 0;
      ctx.joinCodePort = port;
      ctx.joinCodeBtn.hidden = port <= 0;
      if (port > 0) {
        ctx.joinCodeValue.textContent = String(port);
        label = 'join code ' + port + ' (click to copy)';
        ctx.joinCodeBtn.title = label;
        ctx.joinCodeBtn.setAttribute('aria-label', label);
      }
      syncFooterLayout();
    }

    function copyJoinCode() {
      var port;
      var writeText;
      var joinCode;
      function reportCopyResult(copied) {
        if (copied) showInfoMessage('Join code copied to clipboard.', 1600);
        else showWarningMessage('Could not copy join code. Copy it manually.', 2200);
      }

      function tryLegacyCopy(text) {
        var input = document.createElement('input');
        var copied = false;
        input.value = text;
        input.style.position = 'fixed';
        input.style.opacity = '0';
        document.body.appendChild(input);
        input.select();
        try {
          copied = document.execCommand('copy');
        } catch (e) {}
        document.body.removeChild(input);
        return !!copied;
      }

      syncJoinCode();
      port = ctx.joinCodePort | 0;
      if (port <= 0)
        return;
      joinCode = String(port);

      writeText = navigator.clipboard && navigator.clipboard.writeText;
      if (typeof writeText !== 'function') {
        reportCopyResult(tryLegacyCopy(joinCode));
        return;
      }

      writeText.call(navigator.clipboard, joinCode).then(function() {
        reportCopyResult(true);
      }, function() {
        reportCopyResult(tryLegacyCopy(joinCode));
      });
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
      syncConfigToggle,
      syncJoinCode,
      syncFooterLayout,
      copyJoinCode
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
