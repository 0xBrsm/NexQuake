// nq-ui: event wiring and runtime boot
(function() {
  if (!Module || !Module.nqOverlayInstall) return;

  Module.nqOverlayInstall(function(ctx) {
    var OVERLAY_WIDTH_MAX = 340;
    var OVERLAY_WIDTH_MIN = 280;
    var OPTIONS_MENU_BASE_WIDTH = 320;
    var OPTIONS_MENU_RIGHT_EDGE = 308;
    var OPTIONS_MENU_CLEARANCE = 8;

    function isOverlayOpen() {
      return ctx.panel.classList.contains('open') || ctx.editor.classList.contains('open');
    }

    function isOverlayEl(el) {
      return !!el && (
        ctx.panel.contains(el) ||
        ctx.editor.contains(el) ||
        el === ctx.toggle ||
        el === ctx.closeButton
      );
    }

    function syncOverlayToggleState(panelOpen) {
      document.body.classList.toggle('nq-overlay-panel-open', !!panelOpen);
      ctx.toggle.classList.toggle('active', !!panelOpen);
    }

    function getViewportWidth() {
      var viewport = window.visualViewport;
      if (viewport && viewport.width)
        return viewport.width;
      return window.innerWidth || document.documentElement.clientWidth || 0;
    }

    function getVideoWidth() {
      var width = 0;
      if (!Module || !Module.ccall || !Module.calledRun)
        return 0;
      width = nqWasmGetVideoWidth();
      return width > 0 ? width : 0;
    }

    function getOptionsMenuSafeWidth() {
      var viewportWidth = getViewportWidth();
      var canvasRect;
      var videoWidth;
      var menuRight;
      var menuRightInViewport;
      var available;
      if (!viewportWidth || typeof canvasElement === 'undefined' || !canvasElement || !canvasElement.getBoundingClientRect)
        return 0;
      canvasRect = canvasElement.getBoundingClientRect();
      if (!canvasRect || canvasRect.width <= 0)
        return 0;
      videoWidth = getVideoWidth();
      if (!videoWidth)
        return 0;
      menuRight = ((videoWidth - OPTIONS_MENU_BASE_WIDTH) * 0.5) + OPTIONS_MENU_RIGHT_EDGE;
      if (menuRight < 0)
        menuRight = 0;
      if (menuRight > videoWidth)
        menuRight = videoWidth;
      menuRightInViewport = canvasRect.left + (canvasRect.width * (menuRight / videoWidth));
      available = Math.floor(viewportWidth - menuRightInViewport - OPTIONS_MENU_CLEARANCE);
      return available > 0 ? available : 0;
    }

    function syncOverlayPanelWidth() {
      var viewportWidth = getViewportWidth();
      var width;
      var safeWidth;
      var minDesktopWidth;

      if (Module && Module.nqIsTouchInput) {
        width = Math.floor(viewportWidth);
      } else {
        width = Math.floor(Math.min(OVERLAY_WIDTH_MAX, viewportWidth));
        minDesktopWidth = Math.floor(Math.min(OVERLAY_WIDTH_MIN, viewportWidth));
        if (ctx.panel.classList.contains('open')) {
          safeWidth = getOptionsMenuSafeWidth();
          if (safeWidth > 0)
            width = Math.min(width, safeWidth);
        }
        width = Math.max(width, minDesktopWidth);
      }
      if (!Number.isFinite(width) || width <= 0) {
        ctx.panel.style.removeProperty('width');
        if (ctx.syncFooterLayout)
          ctx.syncFooterLayout();
        return;
      }
      ctx.panel.style.width = width + 'px';
      if (ctx.syncFooterLayout)
        ctx.syncFooterLayout();
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
        closeGameMenuFromOverlayExit();
      }
      syncOverlayPanelWidth();
      if (Module && Module.nqTouchActive) {
        if (open && typeof Module.nqTouchPrepareOverlayMode === 'function')
          Module.nqTouchPrepareOverlayMode();
        if (!open && typeof Module.nqTouchEnsureLandscapeFullscreen === 'function')
          Module.nqTouchEnsureLandscapeFullscreen();
      }
      ctx.syncModalOpen();
      syncOverlayToggleState(open);
    }

    function dismissOverlayForGameplay() {
      if (isOverlayOpen())
        setPanelOpen(false);
    }

    function openGameOptionsMenu() {
      nqWasmExecCommand('menu_options');
    }

    function closeGameMenuFromOverlayExit() {
      if (!Module || !Module.ccall || !Module.calledRun)
        return;
      if (!Module.nqTouchMenuMode)
        return;
      nqWasmExecCommand('togglemenu');
      nqWasmExecCommand('togglemenu');
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

    function preventOverlayControlFocus(ev) {
      var focusEl = ev.target;
      if (!focusEl || isOverlayTextInput(focusEl) || isTextInputEl(focusEl))
        return;
      ev.preventDefault();
    }

    function stopPropagation(ev) {
      ev.stopPropagation();
    }

    var overlayMouseTargets = [ctx.panel, ctx.toggle, ctx.editor, ctx.closeButton].filter(Boolean);
    ['mousedown', 'mouseup', 'mousemove', 'click', 'dblclick', 'contextmenu'].forEach(function(eventName) {
      overlayMouseTargets.forEach(function(el) {
        el.addEventListener(eventName, stopPropagation);
      });
    });
    overlayMouseTargets.forEach(function(el) {
      el.addEventListener('wheel', stopPropagation, { passive: true });
    });
    window.addEventListener('resize', syncOverlayPanelWidth, { passive: true });
    window.addEventListener('orientationchange', syncOverlayPanelWidth, { passive: true });
    if (window.visualViewport) {
      window.visualViewport.addEventListener('resize', syncOverlayPanelWidth, { passive: true });
      window.visualViewport.addEventListener('scroll', syncOverlayPanelWidth, { passive: true });
    }

    document.addEventListener('keydown', function(ev) {
      var keyEv = /** @type {KeyboardEvent} */ (ev);
      if (keyEv.key !== 'Escape' || !isOverlayOpen())
        return;
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
    }, true);

    ctx.panel.addEventListener('mousedown', preventOverlayControlFocus, true);
    ctx.editor.addEventListener('mousedown', preventOverlayControlFocus, true);
    function blurNonTextFocusin(ev) {
      if (!isOverlayTextInput(ev.target)) ev.target.blur();
    }
    ctx.panel.addEventListener('focusin', blurNonTextFocusin);
    ctx.editor.addEventListener('focusin', blurNonTextFocusin);
    ctx.toggle.addEventListener('focus', function() {
      ctx.toggle.blur();
    });
    if (ctx.closeButton) {
      ctx.closeButton.addEventListener('focus', function() {
        ctx.closeButton.blur();
      });
    }

    document.addEventListener('mousedown', function(ev) {
      if (isOverlayEl(ev.target))
        ev.stopImmediatePropagation();
    }, true);

    document.addEventListener('click', function(ev) {
      var target = /** @type {Element|null} */ (ev.target);
      if (ev.target === ctx.toggle || ctx.toggle.contains(ev.target)) {
        ev.preventDefault();
        if (!ctx.panel.classList.contains('open')) {
          setPanelOpen(true);
          openGameOptionsMenu();
        }
        return;
      }
      if (ctx.closeButton && (ev.target === ctx.closeButton || ctx.closeButton.contains(ev.target))) {
        ev.preventDefault();
        if (ctx.panel.classList.contains('open'))
          setPanelOpen(false);
        return;
      }
      if (target.closest('#nq-dir-header') ||
          target.closest('#nq-tabs-wrap') ||
          target.closest('#nq-vfs-list li[data-path]'))
        return;
      ctx.setTabsOpen(false);
    }, true);

    if (typeof canvasElement !== 'undefined' && canvasElement)
      canvasElement.addEventListener('mousedown', dismissOverlayForGameplay, true);

    document.addEventListener('pointerlockchange', function() {
      if (typeof canvasElement !== 'undefined' && document.pointerLockElement === canvasElement)
        dismissOverlayForGameplay();
    });

    var installActionHandlers = /** @type {any} */ (window).nqUiInstallActionHandlers;
    if (typeof installActionHandlers === 'function')
      installActionHandlers(ctx);

    ctx.setPanelOpen = setPanelOpen;
    ctx.dismissOverlayForGameplay = dismissOverlayForGameplay;
    ctx.syncOverlayPanelWidth = syncOverlayPanelWidth;
    syncOverlayToggleState(ctx.panel.classList.contains('open'));
    syncOverlayPanelWidth();
  });

  if (typeof Module.nqOverlayBoot === 'function')
    Module.nqOverlayBoot();
})();
