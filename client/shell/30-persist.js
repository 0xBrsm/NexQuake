// ---------------------------------------------------------------
// nq-persist: user file persistence (IDBFS backup/restore)
// ---------------------------------------------------------------
(function() {
  var USERFS_ROOT = '/nexquake';
  var USER_EXTS = nqGetUserFileExts();
  var shutdownRequested = false;

  var getBaseGameName = nqGetBaseGameName;
  var safeReadDir = nqSafeReadDir;
  var safeStat = nqSafeStat;
  var safeMkdirTree = nqSafeMkdirTree;
  var safeUnlink = nqSafeUnlink;

  function stripTrailingSlash(path) {
    path = String(path || '');
    if (path.length > 1 && path.endsWith('/')) return path.slice(0, -1);
    return path;
  }

  function isAllowedUserFile(fullPath) {
    fullPath = String(fullPath || '').toLowerCase();
    var dot = fullPath.lastIndexOf('.');
    if (dot < 0) return false;
    var ext = fullPath.slice(dot + 1);
    return USER_EXTS.indexOf(ext) >= 0;
  }

  function isRemoteNode(path) {
    try {
      var node = FS.lookupPath(path).node;
      return !!(node && node.url);
    } catch (e) {
      return false;
    }
  }

  function listCandidateGameDirs() {
    var baseGame = getBaseGameName();
    var baseGameRoot = '/' + baseGame;
    var dirs = Object.create(null);
    dirs[baseGameRoot] = true;

    safeMkdirTree(USERFS_ROOT);
    safeReadDir(USERFS_ROOT).forEach(function(name) {
      if (name === '.' || name === '..') return;
      var p = '/' + name;
      var st = safeStat(USERFS_ROOT + p);
      if (st && FS.isDir(st.mode)) dirs[p] = true;
    });

    safeReadDir(USERFS_ROOT + baseGameRoot).forEach(function(name) {
      if (name === '.' || name === '..') return;
      var st = safeStat(USERFS_ROOT + baseGameRoot + '/' + name);
      if (st && FS.isDir(st.mode)) dirs[baseGameRoot + '/' + name] = true;
    });

    var remotes = Module.nexquakeRemoteFiles || {};
    Object.keys(remotes).forEach(function(path) {
      path = String(path || '');
      var m = path.match(/^\/([^\/]+)\//);
      if (m && m[1] && m[1] !== 'nexquake') {
        dirs['/' + m[1]] = true;
      }

      var m2 = path.match(/^\/([^\/]+)\/([^\/]+)\/(pak[0-9]+\.pak|progs\.dat)$/i);
      if (m2 && m2[1] && m2[2]) {
        var prefix = String(m2[1]).toLowerCase();
        if (prefix === baseGame) {
          dirs['/' + m2[1] + '/' + m2[2]] = true;
        }
      }
    });

    var out = Object.keys(dirs);
    out.sort();
    return out;
  }

  function collectUserFilesInDir(dir) {
    var results = [];
    safeReadDir(dir).forEach(function(name) {
      if (name === '.' || name === '..') return;
      var fullPath = dir + (dir === '/' ? '' : '/') + name;
      var stat = safeStat(fullPath);
      if (stat && FS.isFile(stat.mode) && isAllowedUserFile(fullPath) && !isRemoteNode(fullPath)) {
        results.push(fullPath);
      }
    });
    return results;
  }

  function collectPersistedUserFiles(dir) {
    var results = [];
    safeReadDir(dir).forEach(function(name) {
      if (name === '.' || name === '..') return;
      var fullPath = dir + (dir === '/' ? '' : '/') + name;
      var stat = safeStat(fullPath);
      if (!stat) return;
      if (FS.isDir(stat.mode)) {
        results = results.concat(collectPersistedUserFiles(fullPath));
      } else if (FS.isFile(stat.mode) && isAllowedUserFile(fullPath)) {
        results.push(fullPath);
      }
    });
    return results;
  }

  function restoreUserFiles() {
    if (typeof FS === 'undefined') return;
    var backups = collectPersistedUserFiles(USERFS_ROOT).sort();
    backups.forEach(function(backupPath) {
      backupPath = String(backupPath || '');
      if (backupPath.indexOf(USERFS_ROOT + '/') !== 0) return;
      var destPath = backupPath.substring(USERFS_ROOT.length);
      if (!destPath || destPath === backupPath) return;
      var destDir = destPath.substring(0, destPath.lastIndexOf('/'));
      if (destDir) safeMkdirTree(destDir);
      try {
        var data = FS.readFile(backupPath);
        safeUnlink(destPath);
        FS.writeFile(destPath, data);
      } catch (e) {}
    });
  }

  function persistUserFiles() {
    if (typeof FS === 'undefined') return;
    try {
      safeMkdirTree(USERFS_ROOT);
      var dirs = listCandidateGameDirs();
      dirs.forEach(function(d) {
        d = stripTrailingSlash(d);
        if (!d || d === '/' || d === USERFS_ROOT) return;
        var backupDir = USERFS_ROOT + d;
        safeMkdirTree(backupDir);
        collectUserFilesInDir(d).forEach(function(fullPath) {
          var name = fullPath.substring(fullPath.lastIndexOf('/') + 1);
          if (!name) return;
          try {
            var data = FS.readFile(fullPath);
            safeUnlink(backupDir + '/' + name);
            FS.writeFile(backupDir + '/' + name, data);
          } catch (e) {}
        });
      });
      FS.syncfs(false, function(err) {
        if (err) console.warn('User file syncfs error:', err);
      });
    } catch (e) {
      console.warn('User file persist failed:', e);
    }
  }

  try {
    Module.nqPersistUserFiles = persistUserFiles;
    Module.nqRestoreUserFiles = restoreUserFiles;
    Module.nqUserFSRoot = USERFS_ROOT;
    Module.nqUserFileExts = USER_EXTS.slice();
  } catch (e) {}

  function requestShutdown() {
    try {
      if (shutdownRequested) return;
      shutdownRequested = true;
      if (typeof Module !== 'undefined' && Module && typeof Module.ccall === 'function') {
        Module.ccall('NexQuake_OnPageHide', 'void', [], []);
      }
      persistUserFiles();
    } catch (e) { console.warn('requestShutdown failed:', e); }
  }
  window.addEventListener('pagehide', requestShutdown);
  window.addEventListener('beforeunload', requestShutdown);
})();

Module.setStatus('Downloading...');
