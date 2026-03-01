// nq-ui: settings panel bootstrap
(function() {
  var toggle = document.getElementById('nq-overlay-toggle');
  var panel = document.getElementById('nq-overlay-panel');
  if (!toggle || !panel) return;

  function syncOverlayViewportHeight() {
    var viewport = window.visualViewport;
    var h = viewport && viewport.height ? viewport.height : window.innerHeight;
    if (!h) return;
    document.documentElement.style.setProperty('--nq-overlay-vh', Math.round(h) + 'px');
  }
  syncOverlayViewportHeight();
  window.addEventListener('resize', syncOverlayViewportHeight, { passive: true });
  window.addEventListener('orientationchange', syncOverlayViewportHeight, { passive: true });
  if (window.visualViewport) {
    window.visualViewport.addEventListener('resize', syncOverlayViewportHeight, { passive: true });
    window.visualViewport.addEventListener('scroll', syncOverlayViewportHeight, { passive: true });
  }

  var cdRow = document.getElementById('nq-cd-row');
  var userExts = (Module && Array.isArray(Module.nqUserFileExts) && Module.nqUserFileExts.length)
    ? Module.nqUserFileExts.slice()
    : nqGetUserFileExts();
  var cdExts = ['ogg', 'mp3'];
  var storedCdEnabled = (typeof nqLoadCdEnabled === 'function') ? nqLoadCdEnabled() : null;

  var ctx = {
    toggle: toggle,
    closeButton: document.getElementById('nq-overlay-close'),
    panel: panel,
    tabs: document.getElementById('nq-vfs-tabs'),
    list: document.getElementById('nq-vfs-list'),
    cdRow: cdRow,
    cdButtons: cdRow ? cdRow.querySelectorAll('.nq-cd-btn[data-cd-command]') : [],
    cdEjectBtn: cdRow ? cdRow.querySelector('.nq-cd-btn[data-cd-command="eject"]') : null,
    cdPowerBtn: cdRow ? cdRow.querySelector('.nq-cd-btn[data-cd-command="toggle"]') : null,
    cdPauseToggleBtn: cdRow ? cdRow.querySelector('.nq-cd-btn[data-cd-command="pause-toggle"]') : null,
    upload: document.getElementById('nq-vfs-upload'),
    fileInput: document.getElementById('nq-vfs-file'),
    uploadError: document.getElementById('nq-upload-error'),
    editor: document.getElementById('nq-editor'),
    editorPath: document.getElementById('nq-editor-path'),
    editorText: document.getElementById('nq-editor-text'),
    editorSave: document.getElementById('nq-editor-save'),
    editorCancel: document.getElementById('nq-editor-cancel'),
    confirm: document.getElementById('nq-confirm'),
    confirmText: document.getElementById('nq-confirm-text'),
    confirmOk: document.getElementById('nq-confirm-ok'),
    confirmCancel: document.getElementById('nq-confirm-cancel'),
    configGlobalBtn: document.getElementById('nq-config-global'),
    joinCodeBtn: document.getElementById('nq-join-code'),
    joinCodeValue: document.getElementById('nq-join-code-value'),
    brandingRow: document.getElementById('nq-branding-row'),
    branding: document.getElementById('nq-branding'),
    versionEl: document.getElementById('nq-version'),
    tabsWrap: document.getElementById('nq-tabs-wrap'),
    dirHeader: document.getElementById('nq-dir-header'),
    dirLabel: document.getElementById('nq-dir-label'),
    tabsMeasureCtx: document.createElement('canvas').getContext('2d'),

    USERFS: (Module && Module.nqUserFSRoot) ? Module.nqUserFSRoot : '/NexQuake/game',
    CD_USERFS: (Module && Module.nqCdUserFSRoot) ? Module.nqCdUserFSRoot : '/NexQuake/cd',
    USER_EXTS: userExts,
    USER_FILE_ACCEPT: userExts.map(function(ext) { return '.' + ext; }).join(','),
    USER_FILE_DESC: userExts.map(function(ext) { return '.' + ext; }).join(' '),
    CD_DIR: (typeof nqGetCdDir === 'function') ? nqGetCdDir() : '/cd/',
    CD_EXTS: cdExts,
    CD_FILE_ACCEPT: cdExts.map(function(ext) { return '.' + ext; }).join(','),
    CD_FILE_DESC: '.ogg .mp3',

    currentDir: null,
    editingFile: null,
    uploadBusy: false,
    cdPreferredEnabled: storedCdEnabled === null ? true : !!storedCdEnabled,
    cdEnabled: true,
    cdPaused: false,
    cdPlaying: false,
    joinCodePort: 0,
    nonCdDir: null,

    safeReadDir: nqSafeReadDir,
    safeStat: nqSafeStat,
    safeMkdirTree: nqSafeMkdirTree,
    safeUnlink: nqSafeUnlink,
    safeSyncFS: nqSafeSyncFS,

    installers: [],
    booted: false
  };

  ctx.syncModalOpen = function() {
    Module.nqOverlayModalOpen = ctx.panel.classList.contains('open') || ctx.editor.classList.contains('open');
  };
  ctx.syncModalOpen();

  Module.nqOverlayCtx = ctx;
  Module.nqOverlayInstall = function(init) {
    if (typeof init === 'function')
      ctx.installers.push(init);
  };

  Module.nqOverlayBoot = function() {
    var originalInit;
    if (ctx.booted)
      return;

    ctx.booted = true;
    ctx.installers.forEach(function(init) {
      init(ctx);
    });

    ctx.currentDir = ctx.getDefaultDirForConfig();
    ctx.setCdEnabled(!!nqGameStarted && ctx.cdPreferredEnabled, false);
    ctx.syncModalOpen();

    originalInit = Module.onRuntimeInitialized;
    Module.onRuntimeInitialized = function() {
      if (originalInit) originalInit.call(Module);
      ctx.refresh();
    };
    Module.nqOverlayUpdateDirs = function() {
      ctx.refresh();
    };
  };
})();
