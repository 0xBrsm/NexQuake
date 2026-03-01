// nq-touch-controls: mobile touch HUD visibility and runtime control
(function() {
  var overlay = document.getElementById('nq-touch-overlay');
  var buttonsRoot = document.getElementById('nq-touch-buttons');
  var bindableSlots = [1, 2, 3, 4, 5, 6, 7, 8];
  if (!overlay || !buttonsRoot) return;

  var moduleRef = (typeof Module !== 'undefined') ? Module : (window.Module = {});
  var touchLayoutSupport = /** @type {any} */ (window).nqTouchLayoutSupport;
  var storageKey = 'nexquake.touch.layout.v1';
  var dragState = null;
  var touchIdleMs = 2500;
  var lastTouchMs = Date.now();
  var touchHeld = false;
  var boundBySlot = {};
  var customLayout = {};
  var bindingGlyphMap = {
    '+forward': '↑',
    '+back': '↓',
    '+moveleft': '←',
    '+moveright': '→',
    'togglemenu': 'Q',
    'toggleconsole': '~'
  };
  var isTouchInput = (function() {
    if (window.matchMedia && window.matchMedia('(pointer: coarse)').matches)
      return true;
    if ('ontouchstart' in window && screen.width <= 1024)
      return true;
    return false;
  })();
  moduleRef.nqIsTouchInput = isTouchInput;
  function syncBodyTouchClasses() {
    if (!document || !document.body)
      return;
    document.body.classList.toggle('nq-touch-input', !!isTouchInput);
    if (isTouchInput)
      document.body.classList.toggle('nq-touch-portrait', !isLandscapeOrientation());
    else
      document.body.classList.remove('nq-touch-portrait');
  }
  syncBodyTouchClasses();
  var fixedMenuAnchor = { left: 0.04, right: 0.96, top: 0.05 };

  var buttonBySlot = {};
  Array.prototype.slice.call(
    buttonsRoot.querySelectorAll('.nq-touch-btn[data-touch-slot]')
  ).forEach(function(btn) {
    var slot = Number(btn.getAttribute('data-touch-slot')) | 0;
    buttonBySlot[slot] = btn;
  });

  var menuBackButton = document.getElementById('nq-touch-menu-m1');
  var menuOverlayButton = document.getElementById('nq-touch-menu-m2');

  var layoutSlots = bindableSlots.filter(function(slot) {
    return !!buttonBySlot[slot];
  });

  var defaultLayout = touchLayoutSupport.buildDefaultLayout(buttonBySlot, layoutSlots, fixedMenuAnchor);
  var layout = touchLayoutSupport.loadLayout(storageKey, layoutSlots, defaultLayout, customLayout);

  moduleRef.nqTouchButtonElementIds = bindableSlots.map(function(slot) {
    return buttonBySlot[slot] ? buttonBySlot[slot].id : ('nq-touch-missing-slot' + slot);
  });
  moduleRef.nqTouchButtonElementIds.push(menuBackButton ? menuBackButton.id : 'nq-touch-missing-menu-m1');

  moduleRef.nqTouchDragActive = false;
  moduleRef.nqTouchControlsVisible = false;
  moduleRef.nqTouchMenuMode = !!moduleRef.nqTouchMenuMode;
  moduleRef.nqTouchFlip = !!moduleRef.nqTouchFlip;

  if (!isTouchInput) {
    overlay.style.display = 'none';
    return;
  }

  moduleRef.nqTouchActive = true;
  var resumeOverlayPending = false;

  overlay.addEventListener('contextmenu', function(e) { e.preventDefault(); });
  ['orientationchange', 'resize'].forEach(function(type) {
    window.addEventListener(type, onViewportChange, { passive: true });
  });
  ['touchstart', 'touchmove', 'touchend', 'touchcancel'].forEach(function(type) {
    document.addEventListener(type, onTouchActivity, { passive: true, capture: true });
  });

  layoutSlots.forEach(function(slot) {
    bindDragHandlers(buttonBySlot[slot], slot);
  });

  if (menuOverlayButton) {
    ['pointerdown', 'pointerup', 'pointercancel', 'touchstart', 'touchend'].forEach(function(type) {
      menuOverlayButton.addEventListener(type, function(ev) {
        ev.stopPropagation();
      }, true);
    });
    menuOverlayButton.addEventListener('click', function(ev) {
      touchLayoutSupport.toggleOverlayPanel(moduleRef, syncFixedButtons, syncVisibility, ev);
    }, true);
  }

  applyLayout();
  refreshBoundVisibility();
  syncFixedButtons();
  syncVisibility();

  function isLandscapeOrientation() {
    if (window.matchMedia)
      return window.matchMedia('(orientation: landscape)').matches;
    return window.innerWidth >= window.innerHeight;
  }

  function tryOpenOverlayAfterResume() {
    var ctx = moduleRef.nqOverlayCtx;
    if (!ctx || typeof ctx.setPanelOpen !== 'function' || !ctx.panel)
      return false;
    if (!ctx.panel.classList.contains('open'))
      ctx.setPanelOpen(true);
    return true;
  }

  function consumeOverlayResumeRequest() {
    if (!resumeOverlayPending || document.visibilityState !== 'visible')
      return;
    resumeOverlayPending = false;
    if (!tryOpenOverlayAfterResume())
      resumeOverlayPending = true;
  }

  document.addEventListener('visibilitychange', function() {
    if (document.visibilityState === 'hidden') {
      resumeOverlayPending = true;
      return;
    }
    consumeOverlayResumeRequest();
  }, { passive: true });

  window.addEventListener('pageshow', function() {
    consumeOverlayResumeRequest();
  }, { passive: true });

  moduleRef.nqTouchEnsureLandscapeFullscreen = touchLayoutSupport.ensureLandscapeFullscreen;
  moduleRef.nqTouchPrepareOverlayMode = touchLayoutSupport.prepareOverlayMode;

  function displayXFromLayoutX(x) {
    return touchLayoutSupport.displayXFromLayoutX(x, moduleRef.nqTouchFlip);
  }

  function layoutXFromDisplayX(x) {
    return touchLayoutSupport.layoutXFromDisplayX(x, moduleRef.nqTouchFlip);
  }

  function saveLayout() {
    try { localStorage.setItem(storageKey, JSON.stringify(layout)); } catch (e) {}
  }

  function applyLayout() {
    layoutSlots.forEach(function(slot) {
      var btn = buttonBySlot[slot];
      var cfg = layout[slot] || defaultLayout[slot];
      var displayX;
      if (!btn || !cfg) return;
      displayX = displayXFromLayoutX(cfg.x);
      btn.style.left = (displayX * 100) + '%';
      btn.style.top = (cfg.y * 100) + '%';
      btn.classList.toggle('nq-touch-btn-hidden', boundBySlot[slot] === false);
    });
  }

  function getSlotBinding(slot) {
    var key;
    if (!moduleRef.ccall || !moduleRef.calledRun) return null;
    key = 218 + slot; // touch1..touch8 => K_AUX13..K_AUX20 (219..226)
    return nqWasmGetKeyBinding(key) || '';
  }

  function setSlotGlyph(slot, binding) {
    var btn = buttonBySlot[slot];
    var norm;
    var svg;
    var text;
    if (!btn) return;
    norm = touchLayoutSupport.normalizeBinding(binding);
    svg = window.NQ_TOUCH_GLYPHS && window.NQ_TOUCH_GLYPHS[norm];
    if (svg) {
      btn.innerHTML = svg;
      btn.classList.add('nq-touch-btn-glyph');
      return;
    }
    if (!norm) {
      btn.textContent = String(slot);
      btn.classList.add('nq-touch-btn-glyph');
      return;
    }
    text = bindingGlyphMap[norm] || '';
    btn.textContent = text;
    btn.classList.toggle('nq-touch-btn-glyph', !!text);
  }

  function refreshBoundVisibility() {
    var changed = false;
    if (!moduleRef.ccall || !moduleRef.calledRun)
      return;

    bindableSlots.forEach(function(slot) {
      var binding = getSlotBinding(slot);
      var bound;
      if (binding === null)
        return;
      bound = !!binding;

      setSlotGlyph(slot, binding);
      if (boundBySlot[slot] !== bound) {
        boundBySlot[slot] = bound;
        changed = true;
      }
    });

    if (changed)
      applyLayout();
  }

  function syncFixedButtons() {
    var panelOpen;
    var leftX;
    var rightX;

    if (menuBackButton) {
      menuBackButton.textContent = 'Q';
      menuBackButton.classList.add('nq-touch-btn-glyph');
    }

    panelOpen = !!(moduleRef.nqOverlayCtx &&
      moduleRef.nqOverlayCtx.panel &&
      moduleRef.nqOverlayCtx.panel.classList.contains('open'));

    if (menuOverlayButton)
      menuOverlayButton.classList.toggle('active', panelOpen);

    leftX = displayXFromLayoutX(fixedMenuAnchor.left);
    rightX = displayXFromLayoutX(fixedMenuAnchor.right);

    if (menuBackButton) {
      menuBackButton.style.left = (leftX * 100) + '%';
      menuBackButton.style.top = (fixedMenuAnchor.top * 100) + '%';
    }
    if (menuOverlayButton) {
      menuOverlayButton.style.left = (rightX * 100) + '%';
      menuOverlayButton.style.top = (fixedMenuAnchor.top * 100) + '%';
    }
  }

  function onTouchActivity(ev) {
    touchHeld = !!(ev.touches && ev.touches.length);
    lastTouchMs = Date.now();
    syncVisibility();
  }

  function onViewportChange() {
    defaultLayout = touchLayoutSupport.buildDefaultLayout(buttonBySlot, layoutSlots, fixedMenuAnchor);
    layoutSlots.forEach(function(slot) {
      if (customLayout[slot]) return;
      layout[slot].x = defaultLayout[slot].x;
      layout[slot].y = defaultLayout[slot].y;
    });
    applyLayout();
    syncVisibility();
    syncBodyTouchClasses();
  }

  function syncVisibility() {
    consumeOverlayResumeRequest();

    var canvasShown = canvasElement && canvasElement.style.display === 'block';
    var landscape = isLandscapeOrientation();
    var modalOpen = moduleRef && moduleRef.nqOverlayModalOpen;
    var menuMode = !!moduleRef.nqTouchMenuMode;
    var visible = canvasShown && landscape;
    var interactive = visible && !modalOpen;

    overlay.style.display = visible ? 'flex' : 'none';
    moduleRef.nqTouchControlsVisible = interactive;
    overlay.classList.toggle('nq-touch-menu-mode', visible && menuMode);
    overlay.classList.toggle('nq-touch-flip', visible && !!moduleRef.nqTouchFlip);
    overlay.classList.toggle('nq-touch-modal-open', visible && modalOpen);

    if ((!interactive || !menuMode) && dragState)
      endDrag();

    overlay.classList.toggle('nq-touch-idle', interactive && !touchHeld &&
      !moduleRef.nqTouchDragActive && (Date.now() - lastTouchMs) >= touchIdleMs);

    syncFixedButtons();
    syncBodyTouchClasses();
  }

  function setDragActive(active) {
    moduleRef.nqTouchDragActive = !!active;
    overlay.classList.toggle('nq-touch-layout-edit', !!active);
  }

  function beginDrag(button, slot, pointerId, clientX, clientY) {
    var cfg = layout[slot] || defaultLayout[slot];
    var displayX;
    if (!cfg) return;
    displayX = displayXFromLayoutX(cfg.x);

    dragState = {
      button: button,
      slot: slot,
      pointerId: pointerId,
      offsetX: (displayX * window.innerWidth) - clientX,
      offsetY: (cfg.y * window.innerHeight) - clientY
    };

    setDragActive(true);
    button.classList.add('dragging');
  }

  function endDrag(pointerId) {
    if (!dragState) return;
    if (pointerId != null && dragState.pointerId !== pointerId)
      return;

    dragState.button.classList.remove('dragging');
    customLayout[dragState.slot] = true;
    dragState = null;
    setDragActive(false);
    saveLayout();
  }

  function bindDragHandlers(button, slot) {
    touchLayoutSupport.bindDragHandlers({
      button: button,
      slot: slot,
      moduleRef: moduleRef,
      beginDrag: beginDrag,
      endDrag: endDrag,
      getDragState: function() { return dragState; },
      setSlotLayout: function(targetSlot, x, y) {
        if (!layout[targetSlot])
          return;
        layout[targetSlot].x = x;
        layout[targetSlot].y = y;
      },
      layoutXFromDisplayX: layoutXFromDisplayX,
      applyLayout: applyLayout
    });
  }

  setInterval(syncVisibility, 200);
  setInterval(refreshBoundVisibility, 1000);

  if (moduleRef.nqOverlayCtx) {
    var baseSyncModalOpen = moduleRef.nqOverlayCtx.syncModalOpen;
    moduleRef.nqOverlayCtx.syncModalOpen = function() {
      if (baseSyncModalOpen) baseSyncModalOpen.call(moduleRef.nqOverlayCtx);
      syncVisibility();
    };
  }
})();
