// nq-overlay: event wiring and runtime boot
(function() {
  if (!Module || !Module.nqOverlayInstall) return;

  Module.nqOverlayInstall(function(ctx) {
    function isOverlayOpen() {
      return ctx.panel.classList.contains('open') || ctx.editor.classList.contains('open');
    }

    function isOverlayEl(el) {
      return !!el && (ctx.panel.contains(el) || ctx.editor.contains(el) || el === ctx.toggle);
    }

    function isOverlayTextInput(el) {
      return el === ctx.editorText;
    }

    function setPanelOpen(open) {
      if (open) {
        ctx.panel.classList.add('open');
        if (document.pointerLockElement) document.exitPointerLock();
        ctx.refresh();
      } else {
        ctx.panel.classList.remove('open');
        ctx.setTabsOpen(false);
        if (ctx.closeConfirmModal)
          ctx.closeConfirmModal(false);
        ctx.closeEditor();
      }
      ctx.syncModalOpen();
    }

    function dismissOverlayForGameplay() {
      if (isOverlayOpen())
        setPanelOpen(false);
    }

    function openGameOptionsMenu() {
      try {
        Module.ccall('NexQuake_ExecCommand', 'void', ['string'], ['menu_options']);
      } catch (e) {}
    }

    function isTextInputEl(el) {
      var tag;
      if (!el)
        return false;
      tag = (el.tagName || '').toLowerCase();
      if (tag === 'input' || tag === 'textarea' || tag === 'select')
        return true;
      return !!el.isContentEditable;
    }

    function isArrowKey(ev) {
      return ev.key === 'ArrowUp' || ev.key === 'ArrowDown' || ev.key === 'ArrowLeft' || ev.key === 'ArrowRight';
    }

    function preventOverlayControlFocus(ev) {
      var focusEl = ev.target;
      if (!focusEl || isOverlayTextInput(focusEl) || isTextInputEl(focusEl))
        return;
      ev.preventDefault();
    }

    function stopPropagation(ev) {
      ev.stopPropagation();
    }

    var overlayMouseTargets = [ctx.panel, ctx.toggle, ctx.editor];
    ['mousedown', 'mouseup', 'mousemove', 'click', 'dblclick', 'contextmenu'].forEach(function(eventName) {
      overlayMouseTargets.forEach(function(el) {
        el.addEventListener(eventName, stopPropagation);
      });
    });
    overlayMouseTargets.forEach(function(el) {
      el.addEventListener('wheel', stopPropagation, { passive: true });
    });

    ['keydown', 'keyup', 'keypress'].forEach(function(eventName) {
      document.addEventListener(eventName, function(ev) {
        var keyEv = /** @type {KeyboardEvent} */ (ev);
        var targetOverlay;
        var targetInput;
        var arrowKey;
        if (!isOverlayOpen())
          return;
        targetOverlay = isOverlayEl(keyEv.target);
        targetInput = isTextInputEl(keyEv.target);
        arrowKey = isArrowKey(keyEv);
        if (eventName === 'keydown' && keyEv.key === 'Escape') {
          if (ctx.closeConfirmModal && ctx.closeConfirmModal(false)) {
            if (keyEv.cancelable)
              keyEv.preventDefault();
            keyEv.stopImmediatePropagation();
            return;
          }
          ctx.closeEditor() || setPanelOpen(false);
          if (keyEv.cancelable)
            keyEv.preventDefault();
          keyEv.stopImmediatePropagation();
          return;
        }
        if (targetOverlay) {
          if (targetInput) {
            keyEv.stopImmediatePropagation();
            return;
          }
          if (arrowKey) {
            if (keyEv.cancelable)
              keyEv.preventDefault();
            return;
          }
          keyEv.stopImmediatePropagation();
          return;
        }
        if (!arrowKey)
          keyEv.stopImmediatePropagation();
      }, true);
    });

    ctx.panel.addEventListener('mousedown', preventOverlayControlFocus, true);
    ctx.editor.addEventListener('mousedown', preventOverlayControlFocus, true);
    ctx.panel.addEventListener('focusin', function(ev) {
      var el = ev.target;
      if (!el || isOverlayTextInput(el))
        return;
      if (typeof el.blur === 'function')
        el.blur();
    });
    ctx.editor.addEventListener('focusin', function(ev) {
      var el = ev.target;
      if (!el || isOverlayTextInput(el))
        return;
      if (typeof el.blur === 'function')
        el.blur();
    });
    ctx.toggle.addEventListener('focus', function() {
      ctx.toggle.blur();
    });

    document.addEventListener('mousedown', function(ev) {
      if (isOverlayEl(ev.target))
        ev.stopImmediatePropagation();
    }, true);

    document.addEventListener('click', function(ev) {
      var target = /** @type {Element|null} */ (ev.target);
      if (ev.target === ctx.toggle || ctx.toggle.contains(ev.target)) {
        var opening = !ctx.panel.classList.contains('open');
        ev.preventDefault();
        setPanelOpen(opening);
        if (opening)
          openGameOptionsMenu();
        return;
      }
      if (target && typeof target.closest === 'function') {
        if (target.closest('#nq-dir-label') ||
            target.closest('#nq-tabs-wrap') ||
            target.closest('#nq-vfs-list li[data-path]'))
          return;
      }
      ctx.setTabsOpen(false);
    }, true);

    if (typeof canvasElement !== 'undefined' && canvasElement) {
      canvasElement.addEventListener('mousedown', function() {
        dismissOverlayForGameplay();
      }, true);
    }

    document.addEventListener('pointerlockchange', function() {
      if (typeof canvasElement !== 'undefined' && document.pointerLockElement === canvasElement)
        dismissOverlayForGameplay();
    });

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
      var li = ev.target && ev.target.closest ? ev.target.closest('li[data-path]') : null;
      var path = li ? (li.getAttribute('data-path') || '') : '';
      if (!li || !path || ctx.isCdDir(ctx.currentDir))
        return;
      if (ev.target && ev.target.closest && ev.target.closest('button'))
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
      var btn = ev.target && ev.target.closest ? ev.target.closest('button.nq-tab') : null;
      var dir = btn ? (btn.dataset.dir || '') : '';
      if (!dir || dir === ctx.currentDir)
        return;
      ctx.currentDir = dir;
      ctx.refresh();
    });
    if (ctx.dirLabel) {
      ctx.dirLabel.addEventListener('click', function(ev) {
        if (ctx.isCdDir(ctx.currentDir))
          return;
        ev.preventDefault();
        ctx.setTabsOpen(!ctx.tabsWrap.classList.contains('open'));
      });
    }

    ctx.tabs.addEventListener('dragover', function(ev) {
      var btn;
      var dir;
      if (!dragSourcePath)
        return;
      btn = ev.target && ev.target.closest ? ev.target.closest('button.nq-tab') : null;
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
      var btn = ev.target && ev.target.closest ? ev.target.closest('button.nq-tab') : null;
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
      var btn = ev.target && ev.target.closest ? ev.target.closest('button') : null;
      var cdRow = ev.target && ev.target.closest ? ev.target.closest('li[data-cd-track-path]') : null;
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
          if (!btn && !(ev.target && ev.target.closest && ev.target.closest('.nq-file-actions'))) {
            ctx.toggleCdTrack(cdTrackPath, cdForceDisabled);
            return;
          }
        }
      }

      li = ev.target && ev.target.closest ? ev.target.closest('li[data-path]') : null;
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
      } else if (btn.classList.contains('nq-del')) {
        ctx.requestDeleteFile(displayPath)
          .catch(reportError('Delete failed', 'Delete failed:'));
      }
    });

    ctx.list.addEventListener('dblclick', function(ev) {
      var li = ev.target && ev.target.closest ? ev.target.closest('li[data-path]') : null;
      var displayPath = li ? (li.getAttribute('data-path') || '') : '';
      if (!displayPath || ctx.isCdDir(ctx.currentDir))
        return;
      if (ev.target && ev.target.closest && ev.target.closest('button'))
        return;
      if (!displayPath.toLowerCase().endsWith('.cfg'))
        return;
      ctx.openEditor(displayPath);
    });

    ctx.setPanelOpen = setPanelOpen;
    ctx.dismissOverlayForGameplay = dismissOverlayForGameplay;
  });

  if (typeof Module.nqOverlayBoot === 'function')
    Module.nqOverlayBoot();
})();
