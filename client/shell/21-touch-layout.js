// nq-touch-layout: geometry, persistence, and drag helpers
var nqTouchLayoutSupport = {
  clamp01: function(v) {
    if (!Number.isFinite(v)) return 0;
    if (v < 0) return 0;
    if (v > 1) return 1;
    return v;
  },

  point: function(x, y) {
    return {
      x: this.clamp01(x),
      y: this.clamp01(y)
    };
  },

  displayXFromLayoutX: function(x, flip) {
    x = this.clamp01(x);
    return flip ? (1 - x) : x;
  },

  layoutXFromDisplayX: function(x, flip) {
    x = this.clamp01(x);
    return flip ? (1 - x) : x;
  },

  buildDefaultLayout: function(buttonBySlot, layoutSlots, fixedMenuAnchor) {
    var w = Math.max(window.innerWidth || 0, 1);
    var h = Math.max(window.innerHeight || 0, 1);
    var css;
    var size;
    var margin;
    var crossStep;
    var edgePad;
    var mLeftX;
    var mRightX;
    var mTopY;
    var cx;
    var cy;
    var touch4Y;
    var midY;
    var offset;
    if (buttonBySlot[layoutSlots[0]]) {
      try { css = window.getComputedStyle(buttonBySlot[layoutSlots[0]]); } catch (e) {}
    }
    size = parseFloat(css && css.width);
    if (!(Number.isFinite(size) && size > 0)) size = 62;
    margin = size * 0.58;
    crossStep = size * 0.90;
    edgePad = (size * 0.5) + 8;
    mLeftX = this.clamp01(edgePad / w);
    mRightX = this.clamp01(1 - (edgePad / w));
    mTopY = this.clamp01(edgePad / h);
    fixedMenuAnchor.left = mLeftX;
    fixedMenuAnchor.right = mRightX;
    fixedMenuAnchor.top = mTopY;
    cx = 1 - ((margin + crossStep) / w);
    cy = 1 - ((margin + crossStep) / h);
    touch4Y = this.clamp01(cy - (crossStep / h));
    midY = this.clamp01(mTopY + ((touch4Y - mTopY) * 0.5));
    offset = Math.max(0, midY - mTopY);

    return {
      1: this.point(cx, cy + (crossStep / h)),
      2: this.point(cx + (crossStep / w), cy),
      3: this.point(cx - (crossStep / w), cy),
      4: this.point(cx, touch4Y),
      5: this.point(mRightX, midY),
      6: this.point(mRightX - offset, mTopY),
      7: this.point(mLeftX, midY),
      8: this.point(mLeftX + offset, mTopY)
    };
  },

  loadLayout: function(storageKey, layoutSlots, defaultLayout, customLayout) {
    var raw;
    var parsed;
    var merged = {};

    try { raw = localStorage.getItem(storageKey); } catch (e) {}
    if (raw) {
      try { parsed = JSON.parse(raw); } catch (e2) {}
    }

    layoutSlots.forEach(function(slot) {
      var base = defaultLayout[slot] || { x: 0.5, y: 0.5 };
      var val = parsed && parsed[slot];
      var x = Number(val && val.x);
      var y = Number(val && val.y);
      var hasX = Number.isFinite(x);
      var hasY = Number.isFinite(y);
      customLayout[slot] = hasX && hasY;
      merged[slot] = {
        x: hasX ? nqTouchLayoutSupport.clamp01(x) : base.x,
        y: hasY ? nqTouchLayoutSupport.clamp01(y) : base.y
      };
    });

    return merged;
  },

  normalizeBinding: function(binding) {
    return (binding || '')
      .split(';', 1)[0]
      .trim()
      .toLowerCase()
      .replace(/\s+/g, ' ');
  },

  ensureLandscapeFullscreen: function() {
    var root = document.documentElement;
    var fullscreenEl = document.fullscreenElement || document.webkitFullscreenElement;
    var requestFullscreen = root.requestFullscreen || root.webkitRequestFullscreen;
    var request;
    var lockRequest;

    if (!fullscreenEl && requestFullscreen) {
      try {
        request = requestFullscreen.call(root, { navigationUI: 'hide' });
      } catch (e1) {
        try {
          request = requestFullscreen.call(root);
        } catch (e2) {}
      }
      if (request && request.catch)
        request.catch(function() {});
    }

    try {
      if (screen.orientation && screen.orientation.lock) {
        lockRequest = screen.orientation.lock('landscape');
        if (lockRequest && lockRequest.catch)
          lockRequest.catch(function() {});
      }
    } catch (e3) {}
  },

  prepareOverlayMode: function() {
    try {
      if (screen.orientation && screen.orientation.unlock)
        screen.orientation.unlock();
    } catch (e1) {}
  },

  toggleOverlayPanel: function(moduleRef, syncFixedButtons, syncVisibility, ev) {
    var ctx = moduleRef.nqOverlayCtx;
    var opening;
    if (!ctx || typeof ctx.setPanelOpen !== 'function' || !ctx.panel)
      return;

    if (ev) {
      ev.preventDefault();
      ev.stopPropagation();
    }

    opening = !ctx.panel.classList.contains('open');
    ctx.setPanelOpen(opening);
    if (opening && moduleRef.ccall && moduleRef.calledRun)
      nqWasmExecCommand('menu_options');

    syncFixedButtons();
    syncVisibility();
  },

  bindDragHandlers: function(options) {
    var button = options.button;
    var slot = options.slot;
    var moduleRef = options.moduleRef;
    var beginDrag = options.beginDrag;
    var endDrag = options.endDrag;
    var getDragState = options.getDragState;
    var setSlotLayout = options.setSlotLayout;
    var layoutXFromDisplayX = options.layoutXFromDisplayX;
    var applyLayout = options.applyLayout;
    var holdTimer = 0;
    var startX = 0;
    var startY = 0;

    function clearHold() {
      if (!holdTimer) return;
      clearTimeout(holdTimer);
      holdTimer = 0;
    }

    function finishPointer(ev) {
      clearHold();
      endDrag(ev.pointerId);
      try { button.releasePointerCapture(ev.pointerId); } catch (e) {}
    }

    button.addEventListener('pointerdown', function(ev) {
      if (ev.pointerType === 'mouse' && ev.button !== 0)
        return;
      if (!moduleRef.nqTouchMenuMode)
        return;

      startX = ev.clientX;
      startY = ev.clientY;
      clearHold();
      holdTimer = setTimeout(function() {
        if (!moduleRef.nqTouchMenuMode)
          return;
        holdTimer = 0;
        beginDrag(button, slot, ev.pointerId, ev.clientX, ev.clientY);
        try { button.setPointerCapture(ev.pointerId); } catch (e) {}
      }, 350);
    }, { passive: true });

    button.addEventListener('pointermove', function(ev) {
      var dragState;
      var dx;
      var dy;
      var x;
      var y;

      dragState = getDragState();
      if (!dragState || dragState.pointerId !== ev.pointerId) {
        if (holdTimer) {
          dx = ev.clientX - startX;
          dy = ev.clientY - startY;
          if ((dx * dx + dy * dy) > 64)
            clearHold();
        }
        return;
      }

      ev.preventDefault();
      x = layoutXFromDisplayX((ev.clientX + dragState.offsetX) / window.innerWidth);
      y = this.clamp01((ev.clientY + dragState.offsetY) / window.innerHeight);
      setSlotLayout(dragState.slot, x, y);
      applyLayout();
    }.bind(this), { passive: false });

    ['pointerup', 'pointercancel'].forEach(function(type) {
      button.addEventListener(type, finishPointer, { passive: true });
    });

    button.addEventListener('lostpointercapture', function(ev) {
      clearHold();
      endDrag(ev.pointerId);
    });
  }
};

/** @type {any} */ (window).nqTouchLayoutSupport = nqTouchLayoutSupport;
