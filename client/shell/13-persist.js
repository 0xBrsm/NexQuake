// ---------------------------------------------------------------
// nq-persist: user file persistence (IDBFS sync)
// ---------------------------------------------------------------
(function() {
  var USERFS_ROOT = '/NexQuake';
  var USER_EXTS = nqGetUserFileExts();
  var shutdownRequested = false;

  function syncUserFS() {
    if (typeof FS === 'undefined') return;
    try {
      FS.syncfs(false, function(err) {
        if (err) console.warn('User file syncfs error:', err);
      });
    } catch (e) {
      console.warn('User file syncfs failed:', e);
    }
  }

  try {
    Module.nqPersistUserFiles = syncUserFS;
    Module.nqUserFSRoot = USERFS_ROOT;
    Module.nqUserFileExts = USER_EXTS.slice();
  } catch (e) {}

  function requestShutdown() {
    if (shutdownRequested) return;
    shutdownRequested = true;
    try {
      if (typeof Module !== 'undefined' && Module && typeof Module.ccall === 'function')
        Module.ccall('NexQuake_OnPageHide', 'void', [], []);
    } catch (e) {
      console.warn('NexQuake_OnPageHide failed:', e);
    }
    syncUserFS();
  }

  window.addEventListener('pagehide', requestShutdown);
  window.addEventListener('beforeunload', requestShutdown);
})();
