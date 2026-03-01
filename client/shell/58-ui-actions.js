// nq-ui-actions: editor, upload, list, and tab interaction handlers
/** @type {any} */ (window).nqUiInstallActionHandlers = function(ctx) {
  function reportError(userMessage, logPrefix) {
    return function(err) {
      ctx.showErrorMessage(userMessage, 3000);
      console.error(logPrefix, err);
    };
  }

  ctx.editorSave.onclick = function() {
    if (!ctx.editingFile) return;
    var data = new TextEncoder().encode(ctx.editorText.value);
    try {
      FS.writeFile(ctx.editingFile.backup, data);
      ctx.safeSyncFS();
      ctx.closeEditor();
      ctx.refresh();
    } catch (e) {
      console.error('Save failed:', e);
    }
  };

  ctx.editorCancel.onclick = ctx.closeEditor;
  ctx.upload.onclick = function() { if (!ctx.uploadBusy) ctx.fileInput.click(); };
  if (ctx.joinCodeBtn) {
    ctx.joinCodeBtn.onclick = function(ev) {
      ev.preventDefault();
      ctx.copyJoinCode();
    };
  }

  ctx.cdButtons.forEach(function(btn) {
    btn.onclick = function() {
      ctx.runCdCommand(btn.getAttribute('data-cd-command'))
        .catch(reportError('CD command failed', 'CD command failed:'));
    };
  });

  ctx.fileInput.onchange = function(e) {
    var settings = ctx.getCurrentUploadSettings();
    settings.refreshOnSuccess = true;
    ctx.processUploads(e.target.files, settings).catch(reportError('Upload failed', 'Upload queue failed:'));
    ctx.fileInput.value = '';
  };

  var dragSourcePath = '';
  var dragLi = null;
  var dropTargetBtn = null;

  function clearDropTarget() {
    if (!dropTargetBtn)
      return;
    dropTargetBtn.classList.remove('nq-drop-target');
    dropTargetBtn = null;
  }

  ctx.list.addEventListener('dragstart', function(ev) {
    var li = ev.target.closest('li[data-path]');
    var path = li ? (li.getAttribute('data-path') || '') : '';
    if (!li || !path || ctx.isCdDir(ctx.currentDir))
      return;
    if (ev.target.closest('button'))
      return;

    dragSourcePath = path;
    dragLi = li;
    li.classList.add('nq-dragging');
    ctx.setTabsOpen(true);
    if (ev.dataTransfer) {
      ev.dataTransfer.effectAllowed = 'move';
      try { ev.dataTransfer.setData('text/nq-file-path', path); } catch (e1) {}
      try { ev.dataTransfer.setData('text/plain', path); } catch (e2) {}
    }
  });

  ctx.list.addEventListener('dragend', function() {
    if (dragLi)
      dragLi.classList.remove('nq-dragging');
    dragLi = null;
    dragSourcePath = '';
    clearDropTarget();
  });

  ctx.tabs.addEventListener('click', function(ev) {
    var btn = ev.target.closest('button.nq-tab');
    var dir = btn ? (btn.dataset.dir || '') : '';
    if (!dir || dir === ctx.currentDir)
      return;
    ctx.currentDir = dir;
    ctx.refresh();
  });
  function toggleDirTabs(ev) {
    var rawTarget = /** @type {Node|null} */ (ev.target);
    var target = rawTarget && rawTarget.nodeType === 1
      ? /** @type {Element} */ (rawTarget)
      : (rawTarget && rawTarget.parentElement ? rawTarget.parentElement : null);
    if (!target || ctx.isCdDir(ctx.currentDir) || target.closest('#nq-overlay-close'))
      return;
    ev.preventDefault();
    if (Module && Module.nqIsTouchInput) {
      if (ctx.dirLabel && ctx.dirLabel.contains(target) && typeof ctx.cycleDir === 'function')
        ctx.cycleDir(1);
      return;
    }
    ctx.setTabsOpen(!ctx.tabsWrap.classList.contains('open'));
  }

  if (ctx.dirHeader)
    ctx.dirHeader.addEventListener('click', toggleDirTabs);

  ctx.tabs.addEventListener('dragover', function(ev) {
    var btn;
    var dir;
    if (!dragSourcePath)
      return;
    btn = ev.target.closest('button.nq-tab');
    dir = btn ? (btn.dataset.dir || '') : '';
    if (!btn || !dir) {
      clearDropTarget();
      return;
    }
    ev.preventDefault();
    if (dropTargetBtn !== btn) {
      clearDropTarget();
      dropTargetBtn = btn;
      dropTargetBtn.classList.add('nq-drop-target');
    }
  });

  ctx.tabs.addEventListener('dragleave', function(ev) {
    var rt = ev.relatedTarget;
    if (rt && ctx.tabs.contains(rt))
      return;
    clearDropTarget();
  });

  ctx.tabs.addEventListener('drop', function(ev) {
    var btn = ev.target.closest('button.nq-tab');
    var dir = btn ? (btn.dataset.dir || '') : '';
    var src = dragSourcePath;
    if (!src || !dir)
      return;
    ev.preventDefault();
    if (ev.dataTransfer)
      src = ev.dataTransfer.getData('text/nq-file-path') || ev.dataTransfer.getData('text/plain') || src;
    if (dragLi)
      dragLi.classList.remove('nq-dragging');
    dragLi = null;
    dragSourcePath = '';
    clearDropTarget();
    ctx.moveFileToDir(src, dir);
  });

  ctx.list.addEventListener('click', function(ev) {
    var btn = ev.target.closest('button');
    var cdRow = ev.target.closest('li[data-cd-track-path]');
    var cdTrackPath;
    var cdForceDisabled;
    var li;
    var displayPath;
    if (cdRow && ctx.list.contains(cdRow)) {
      cdTrackPath = cdRow.getAttribute('data-cd-track-path') || '';
      if (cdTrackPath) {
        cdForceDisabled = cdRow.getAttribute('data-cd-track-disabled') === '1';
        if (btn && btn.classList.contains('nq-cd-track-toggle')) {
          ctx.toggleCdTrack(cdTrackPath, cdForceDisabled);
          return;
        }
        if (!btn && !ev.target.closest('.nq-file-actions')) {
          ctx.toggleCdTrack(cdTrackPath, cdForceDisabled);
          return;
        }
      }
    }

    li = ev.target.closest('li[data-path]');
    displayPath = li ? (li.getAttribute('data-path') || '') : '';

    if (!btn) {
      if (!displayPath)
        ctx.setTabsOpen(false);
      return;
    }

    if (!ctx.list.contains(btn))
      return;
    if (!displayPath)
      return;

    if (btn.classList.contains('nq-dl')) {
      Module.exportFile(displayPath);
    } else if (btn.classList.contains('nq-exec')) {
      ctx.execCfgFile(displayPath);
    } else if (btn.classList.contains('nq-del')) {
      ctx.requestDeleteFile(displayPath)
        .catch(reportError('Delete failed', 'Delete failed:'));
    } else if (btn.classList.contains('nq-edit')) {
      ctx.openEditor(displayPath);
    }
  });

  ctx.list.addEventListener('dblclick', function(ev) {
    if (Module && Module.nqIsTouchInput)
      return;
    var li = ev.target.closest('li[data-path]');
    var displayPath = li ? (li.getAttribute('data-path') || '') : '';
    if (!displayPath || ctx.isCdDir(ctx.currentDir))
      return;
    if (ev.target.closest('button'))
      return;
    if (!displayPath.toLowerCase().endsWith('.cfg'))
      return;
    ctx.openEditor(displayPath);
  });
};
