// nq-overlay: VFS tabs, file list, editor, and directory ops
(function() {
  if (!Module || !Module.nqOverlayInstall) return;

  Module.nqOverlayInstall(function(ctx) {
    var FILE_DELETE_ICON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>';

    function getDirs() {
      var baseDir = ctx.getBaseGameDir();
      var dirs = [baseDir];
      var seen = {};
      var installed = Module.nexquakeInstalledManifests || {};

      seen[baseDir] = true;

      function add(dir) {
        if (!dir || dir === ctx.USERFS + '/' || dir === ctx.CD_DIR || seen[dir]) return;
        seen[dir] = true;
        dirs.push(dir);
      }

      Object.keys(installed).forEach(function(mod) {
        if (installed[mod]) add('/' + mod + '/');
      });

      if (typeof FS !== 'undefined') {
        ctx.safeReadDir(ctx.USERFS).forEach(function(name) {
          var st;
          if (name === '.' || name === '..') return;
          st = ctx.safeStat(ctx.USERFS + '/' + name);
          if (st && FS.isDir(st.mode)) add('/' + name + '/');
        });
      }

      dirs.sort(function(a, b) { return a === baseDir ? -1 : b === baseDir ? 1 : a.localeCompare(b); });
      return dirs;
    }

    function buildTabs() {
      var dirs = getDirs();
      var labels = ['GAME'];
      ctx.tabs.innerHTML = '';

      dirs.forEach(function(dir) {
        var btn = document.createElement('button');
        var name = dir.replace(/^\/|\/$/g, '');
        btn.className = 'nq-tab' + (ctx.currentDir === dir ? ' active' : '');
        btn.textContent = name;
        btn.dataset.dir = dir;
        ctx.tabs.appendChild(btn);
        labels.push(name);
      });

      ctx.setTabsOpenWidth(labels);
    }

    function collectFiles(dir, includeFile) {
      var out = [];
      ctx.safeReadDir(dir).forEach(function(name) {
        var path;
        var st;
        if (name === '.' || name === '..') return;
        path = dir + (dir === '/' ? '' : '/') + name;
        st = ctx.safeStat(path);
        if (!st) return;
        if (FS.isDir(st.mode)) out = out.concat(collectFiles(path, includeFile));
        else if (FS.isFile(st.mode) && (!includeFile || includeFile(path))) out.push(path);
      });
      return out;
    }

    function isCfg(path) {
      return path.toLowerCase().endsWith('.cfg');
    }

    function openEditor(displayPath, backupPath) {
      try {
        var data = FS.readFile(backupPath, { encoding: 'utf8' });
        ctx.editingFile = { display: displayPath, backup: backupPath };
        ctx.editorPath.textContent = displayPath;
        ctx.editorText.value = data;
        ctx.editor.classList.add('open');
        ctx.editorText.setSelectionRange(0, 0);
        ctx.editorText.scrollTop = 0;
        ctx.editorText.focus();
      } catch (e) {
        console.error('Failed to read file for editor:', e);
      }
    }

    function closeEditor() {
      if (!ctx.editor.classList.contains('open')) return false;
      ctx.editor.classList.remove('open');
      ctx.editingFile = null;
      ctx.editorText.value = '';
      return true;
    }

    function safeSymlink(target, path) {
      try { FS.symlink(target, path); } catch (e) {}
    }

    function syncAndRefresh() {
      ctx.safeSyncFS();
      ctx.refresh();
    }

    function reportMutationFailure(message, err) {
      ctx.showErrorMessage(message, 3000);
      console.error(message + ':', err);
    }

    async function deleteFile(displayPath) {
      var backupPath = ctx.USERFS + displayPath;
      var runtimeState;
      var stateForDeletedTrack;

      try {
        if (ctx.isCdDir(ctx.currentDir)) {
          runtimeState = ctx.getCdRuntimeState();
          stateForDeletedTrack = ctx.getCdTrackButtonState(displayPath, runtimeState);
          if (stateForDeletedTrack.isCurrentTrack && runtimeState.state !== 'stopped') {
            try {
              await ctx.runCdCommand('stop');
            } catch (stopErr) {
              console.error('CD stop failed during delete:', stopErr);
            }
          }
        }
        ctx.safeUnlink(backupPath);
        syncAndRefresh();
      } catch (e) {
        reportMutationFailure('Delete failed', e);
      }
    }

    async function requestDeleteFile(displayPath) {
      if (!displayPath)
        return;
      if (!await ctx.confirmAsync('Delete ' + displayPath + '?', 'Delete'))
        return;
      await deleteFile(displayPath);
    }

    async function moveFileToDir(displayPath, targetDir) {
      var srcPath = String(displayPath || '').trim();
      var dstDir = String(targetDir || '').trim();
      var name;
      var dstPath;
      var srcBackup;
      var dstDirPath;
      var dstBackupDir;
      var dstBackupPath;

      if (!srcPath || !dstDir)
        return;
      if (!srcPath.startsWith('/') || !dstDir.startsWith('/'))
        return;

      name = srcPath.slice(srcPath.lastIndexOf('/') + 1).toLowerCase();
      if (!name || !ctx.isUserFile(name))
        return;

      if (!dstDir.endsWith('/'))
        dstDir += '/';
      dstPath = dstDir + name;
      if (dstPath === srcPath)
        return;

      srcBackup = ctx.USERFS + srcPath;
      dstDirPath = dstDir.replace(/\/$/, '');
      dstBackupDir = ctx.USERFS + dstDirPath;
      dstBackupPath = dstBackupDir + '/' + name;

      if (ctx.safeStat(dstBackupPath)) {
        if (!await ctx.confirmAsync('Overwrite ' + dstPath + '?', 'Overwrite'))
          return;
      }

      try {
        ctx.safeMkdirTree(dstBackupDir);
        safeSymlink(dstBackupDir, dstDirPath);
        ctx.safeUnlink(dstBackupPath);
        FS.rename(srcBackup, dstBackupPath);
        syncAndRefresh();
      } catch (e) {
        reportMutationFailure('Move failed', e);
      }
    }

    function refresh() {
      var files;
      var dirName;
      var isCdMode;
      var serverTracks = [];
      var runtime = null;
      var localTrackNumbers = null;

      ctx.syncCdModeUI();
      ctx.syncConfigToggle();
      if (ctx.fileInput) ctx.fileInput.accept = ctx.getCurrentUploadSettings().accept;
      buildTabs();

      dirName = ctx.currentDir.replace(/^\/|\/$/g, '');
      ctx.dirLabel.textContent = dirName;
      ctx.list.innerHTML = '';
      closeEditor();

      if (typeof FS === 'undefined') {
        ctx.list.innerHTML = '<li style="color:#888">FS not ready</li>';
        return;
      }

      isCdMode = ctx.isCdDir(ctx.currentDir);

      if (isCdMode) {
        var cdBackupRoot = (ctx.USERFS + ctx.CD_DIR).replace(/\/$/, '');

        ctx.ensureCdDirs();
        files = collectFiles(cdBackupRoot, ctx.isCdFile)
          .map(function(path) {
            return path.indexOf(ctx.USERFS + '/') === 0 ? path.slice(ctx.USERFS.length) : path;
          })
          .filter(function(path) {
            return path.startsWith('/');
          });
        localTrackNumbers = Object.create(null);
        files.forEach(function(path) {
          var trackNumber = ctx.getCdTrackNumber(path);
          if (trackNumber > 0)
            localTrackNumbers[trackNumber] = true;
        });
        serverTracks = ctx.getCdRemoteTracks().sort();
        runtime = ctx.getCdRuntimeState();
      } else {
        files = collectFiles(ctx.USERFS, ctx.isUserFile)
          .map(function(path) { return path.indexOf(ctx.USERFS + '/') === 0 ? path.slice(ctx.USERFS.length) : path; })
          .filter(function(path) { return path.startsWith('/'); });
        files = files.filter(function(path) { return path.indexOf(ctx.currentDir) === 0; });
      }

      files.sort();

      if (isCdMode && !files.length && !serverTracks.length) {
        ctx.list.innerHTML = '<li style="color:#888">No user CD tracks</li>';
        return;
      }

      if (isCdMode && !files.length)
        ctx.list.innerHTML = '<li style="color:#888">No user CD tracks</li>';

      files.forEach(function(displayPath) {
        var shownName = displayPath;
        var li;
        var span;
        var actions;
        var cdBtn;
        var dlBtn;
        var delBtn;

        if (displayPath.startsWith(ctx.currentDir))
          shownName = displayPath.slice(ctx.currentDir.length);

        li = document.createElement('li');
        li.setAttribute('data-path', displayPath);
        li.draggable = !isCdMode;

        span = document.createElement('span');
        span.className = 'nq-fname';
        span.textContent = shownName;
        if (!isCdMode && isCfg(displayPath))
          span.classList.add('nq-editable');

        if (isCdMode) {
          ctx.setCdTrackMeta(li, displayPath, false);
          cdBtn = ctx.createCdTrackToggleButton(displayPath, false, runtime);
          li.appendChild(cdBtn);
          ctx.applyCdTrackElementState(li, runtime);
        }
        li.appendChild(span);
        actions = document.createElement('div');
        actions.className = 'nq-file-actions';

        dlBtn = document.createElement('button');
        dlBtn.className = 'nq-dl';
        dlBtn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>';
        dlBtn.title = 'Download';
        actions.appendChild(dlBtn);

        delBtn = document.createElement('button');
        delBtn.innerHTML = FILE_DELETE_ICON;
        delBtn.className = 'nq-del';
        delBtn.title = 'Delete';
        actions.appendChild(delBtn);

        li.appendChild(actions);
        ctx.list.appendChild(li);
      });

      if (!isCdMode && !files.length) {
        ctx.list.innerHTML = '<li style="color:#888">No user files</li>';
        return;
      }

      if (isCdMode)
        ctx.appendCdServerTracks(serverTracks, runtime, localTrackNumbers);
    }

    Object.assign(ctx, {
      openEditor,
      closeEditor,
      deleteFile,
      requestDeleteFile,
      moveFileToDir,
      refresh
    });
  });
})();
