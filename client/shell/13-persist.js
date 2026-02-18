// ---------------------------------------------------------------
// nq-persist: user file persistence (IDBFS sync)
// ---------------------------------------------------------------
(function() {
  var USERFS_ROOT = '/NexQuake';
  var USER_GAME_ROOT = USERFS_ROOT + '/game';
  var USER_CD_ROOT = USERFS_ROOT + '/cd';
  var USER_EXTS = nqGetUserFileExts();
  var unloadShutdownRequested = false;

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
    Module.nqUserFSRoot = USER_GAME_ROOT;
    Module.nqCdUserFSRoot = USER_CD_ROOT;
    Module.nqUserFileExts = USER_EXTS.slice();
  } catch (e) {}

  function requestUnloadShutdown() {
    if (unloadShutdownRequested) return;
    unloadShutdownRequested = true;
    try {
      if (typeof Module !== 'undefined' && Module && typeof Module.ccall === 'function')
        Module.ccall('NexQuake_OnPageUnload', 'void', [], []);
    } catch (e) {
      console.warn('NexQuake_OnPageUnload failed:', e);
    }
    syncUserFS();
  }

  // These are unload/navigation signals (not tab-visibility changes).
  window.addEventListener('pagehide', requestUnloadShutdown);
  window.addEventListener('beforeunload', requestUnloadShutdown);
})();
