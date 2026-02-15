// nq-overlay: settings overlay (VFS explorer, upload, inline editor)
(function() {
  var toggle = document.getElementById('nq-overlay-toggle');
  var panel = document.getElementById('nq-overlay-panel');
  if (!toggle || !panel) return;
  var tabs = document.getElementById('nq-vfs-tabs');
  var list = document.getElementById('nq-vfs-list');
  var upload = document.getElementById('nq-vfs-upload');
  var fileInput = document.getElementById('nq-vfs-file');
  var uploadError = document.getElementById('nq-upload-error');
  var editor = document.getElementById('nq-editor');
  var editorPath = document.getElementById('nq-editor-path');
  var editorText = document.getElementById('nq-editor-text');
  var editorSave = document.getElementById('nq-editor-save');
  var editorCancel = document.getElementById('nq-editor-cancel');

  var configToggle = document.getElementById('nq-config-toggle');
  var tabsToggle = document.getElementById('nq-tabs-toggle');
  var tabsWrap = document.getElementById('nq-tabs-wrap');
  var dirLabel = document.getElementById('nq-dir-label');
  var tabsMeasureCanvas = document.createElement('canvas');
  var tabsMeasureCtx = tabsMeasureCanvas.getContext('2d');

  var USERFS = (Module && Module.nqUserFSRoot) ? Module.nqUserFSRoot : '/nexquake';
  var USER_EXTS = (Module && Array.isArray(Module.nqUserFileExts) && Module.nqUserFileExts.length) ? Module.nqUserFileExts.slice() : nqGetUserFileExts();
  var USER_FILE_ACCEPT = USER_EXTS.map(function(ext) { return '.' + ext; }).join(',');
  var USER_FILE_DESC = USER_EXTS.map(function(ext) { return '.' + ext; }).join(', ');
  var currentDir;
  var editingFile = null;
  var uploadMessageTimer = null;
  var uploadBusy = false;
  var dragSourcePath = '';

  if (fileInput) fileInput.accept = USER_FILE_ACCEPT;

  function isUserFile(p) {
    var ext = p.slice(p.lastIndexOf('.') + 1).toLowerCase();
    return USER_EXTS.indexOf(ext) >= 0;
  }

  function getDefaultDirForConfig() {
    return (Module && Module.nqPerModConfig) ? '__all__' : '/id1/';
  }

  // Sync toggle state from Module on open; push changes back on click
  function syncConfigToggle() {
    configToggle.checked = !Module.nqPerModConfig;
  }
  configToggle.addEventListener('change', function() {
    Module.nqPerModConfig = !configToggle.checked;
    nqSavePerModConfig(Module.nqPerModConfig);
    currentDir = getDefaultDirForConfig();
    if (panel.classList.contains('open')) refresh();
  });
  currentDir = getDefaultDirForConfig();

  function setTabsOpen(open) {
    tabsWrap.classList.toggle('open', open);
    tabsToggle.classList.toggle('open', open);
    tabsToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
  }

  function setTabsOpenWidth(labels) {
    if (!labels || !labels.length) labels = ['all', 'GAME'];
    var maxWidth = 0;
    if (tabsMeasureCtx) {
      tabsMeasureCtx.font = '11px monospace';
      labels.forEach(function(label) {
        var w = tabsMeasureCtx.measureText(label).width;
        if (w > maxWidth) maxWidth = w;
      });
    } else {
      labels.forEach(function(label) {
        var w = label.length * 7;
        if (w > maxWidth) maxWidth = w;
      });
    }
    var targetWidth = Math.ceil(maxWidth + 26);
    tabsWrap.style.setProperty('--nq-tabs-open-width', targetWidth + 'px');
  }

  tabsToggle.addEventListener('click', function(ev) {
    ev.stopPropagation();
    setTabsOpen(!tabsWrap.classList.contains('open'));
  });

  function isCfg(p) { return p.toLowerCase().endsWith('.cfg'); }

  // Input isolation: stop events from reaching Emscripten
  function isOverlayEl(el) {
    return panel.contains(el) || editor.contains(el) || el === toggle;
  }
  ['mousedown','mouseup','mousemove','click','dblclick','wheel','contextmenu','keydown','keyup','keypress'].forEach(function(e) {
    panel.addEventListener(e, function(ev) { ev.stopPropagation(); });
    toggle.addEventListener(e, function(ev) { ev.stopPropagation(); });
    editor.addEventListener(e, function(ev) { ev.stopPropagation(); });
  });
  ['keydown','keyup','keypress'].forEach(function(e) {
    document.addEventListener(e, function(ev) {
      if (!panel.classList.contains('open') && !editor.classList.contains('open')) return;
      if (e === 'keydown' && ev.key === 'Escape') { closeEditor() || setPanelOpen(false); }
      ev.stopImmediatePropagation();
    }, true);
  });
  document.addEventListener('mousedown', function(ev) {
    if (isOverlayEl(ev.target)) ev.stopImmediatePropagation();
  }, true);
  document.addEventListener('click', function(ev) {
    if (ev.target === toggle || toggle.contains(ev.target)) {
      ev.stopImmediatePropagation();
      ev.preventDefault();
      setPanelOpen(!panel.classList.contains('open'));
    }
  }, true);
  canvasElement.addEventListener('mousedown', function() {
    dismissOverlayForGameplay();
  }, true);
  document.addEventListener('pointerlockchange', function() {
    if (document.pointerLockElement === canvasElement) {
      dismissOverlayForGameplay();
    }
  });

  function setPanelOpen(open) {
    if (open) {
      panel.classList.add('open');
      if (document.pointerLockElement) document.exitPointerLock();
      refresh();
    } else {
      panel.classList.remove('open');
      setTabsOpen(false);
      closeEditor();
    }
  }

  function dismissOverlayForGameplay() {
    if (panel.classList.contains('open') || editor.classList.contains('open')) {
      setPanelOpen(false);
    }
  }

  var safeReadDir = nqSafeReadDir;
  var safeStat = nqSafeStat;
  var safeMkdirTree = nqSafeMkdirTree;
  var safeUnlink = nqSafeUnlink;
  var safeSyncFS = nqSafeSyncFS;

  function maybePersistUserFiles() {
    try { if (Module.nqPersistUserFiles) Module.nqPersistUserFiles(); } catch (e) {}
  }

  function forEachFile(files, fn) {
    if (!files) return;
    for (var i = 0; i < files.length; i++) fn(files[i]);
  }

  function clearUploadMessage() {
    if (uploadMessageTimer) {
      clearTimeout(uploadMessageTimer);
      uploadMessageTimer = null;
    }
    if (!uploadError) return;
    uploadError.textContent = '';
    uploadError.classList.remove('active', 'error');
  }

  function showUploadMessage(msg, kind, clearAfterMs) {
    if (!uploadError) return;
    clearUploadMessage();
    uploadError.textContent = msg || '';
    if (kind === 'active') uploadError.classList.add('active');
    if (kind === 'error') uploadError.classList.add('error');
    if (clearAfterMs && msg) {
      uploadMessageTimer = setTimeout(function() {
        clearUploadMessage();
      }, clearAfterMs);
    }
  }

  function clearUploadError() {
    clearUploadMessage();
  }

  function showUploadError(msg) {
    showUploadMessage(msg, 'error', 3000);
  }

  function clearUploadStatus() {
    clearUploadMessage();
  }

  function showUploadStatus(msg, kind, clearAfterMs) {
    showUploadMessage(msg, kind, clearAfterMs);
  }

  function setUploadBusyState(busy) {
    uploadBusy = !!busy;
    if (upload) upload.disabled = uploadBusy;
  }

  function formatBytes(bytes) {
    var n = Number(bytes || 0);
    if (n < 1024) return n + ' B';
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
    return (n / (1024 * 1024)).toFixed(1) + ' MB';
  }

  function syncFSAsync() {
    return new Promise(function(resolve) {
      try {
        FS.syncfs(false, function(err) { resolve(err || null); });
      } catch (e) {
        resolve(e || null);
      }
    });
  }

  function getDirs() {
    var dirs = ['/id1/'];
    var seen = {'/id1/': true};
    function add(d) {
      if (!d || d === USERFS + '/' || seen[d]) return;
      seen[d] = true;
      dirs.push(d);
    }
    var remotes = Module.nexquakeRemoteFiles || {};
    Object.keys(remotes).forEach(function(p) {
      var m = p.match(/^\/([^\/]+)\//);
      if (m) add('/' + m[1] + '/');
    });
    if (typeof FS !== 'undefined') {
      safeReadDir(USERFS).forEach(function(n) {
        if (n === '.' || n === '..') return;
        var st = safeStat(USERFS + '/' + n);
        if (st && FS.isDir(st.mode)) add('/' + n + '/');
      });
    }
    dirs.sort(function(a,b) { return a === '/id1/' ? -1 : b === '/id1/' ? 1 : a.localeCompare(b); });
    return dirs;
  }

  function clearTabDropTargets() {
    if (!tabs) return;
    tabs.querySelectorAll('.nq-drop-target').forEach(function(btn) {
      btn.classList.remove('nq-drop-target');
    });
  }

  function moveFileToDir(displayPath, targetDir) {
    var srcPath = String(displayPath || '').trim();
    var dstDir = String(targetDir || '').trim();
    if (!srcPath || !dstDir || dstDir === '__all__') return;
    if (!srcPath.startsWith('/') || !dstDir.startsWith('/')) return;
    var name = srcPath.slice(srcPath.lastIndexOf('/') + 1).toLowerCase();
    if (!name || !isUserFile(name)) return;

    var dstPath = dstDir + name;
    if (dstPath === srcPath) return;

    var srcBackup = USERFS + srcPath;
    var dstDirPath = dstDir.replace(/\/$/, '');
    var dstBackupDir = USERFS + dstDirPath;
    var dstBackupPath = dstBackupDir + '/' + name;
    if ((safeStat(dstPath) || safeStat(dstBackupPath)) && !confirm('Overwrite ' + dstPath + '?')) {
      showUploadStatus('Move skipped for ' + name, '', 2500);
      return;
    }

    try {
      var data;
      try {
        data = FS.readFile(srcBackup);
      } catch (e1) {
        data = FS.readFile(srcPath);
      }

      safeMkdirTree(dstDirPath);
      safeMkdirTree(dstBackupDir);

      safeUnlink(dstPath);
      safeUnlink(dstBackupPath);
      FS.writeFile(dstPath, data);
      FS.writeFile(dstBackupPath, data);

      safeUnlink(srcPath);
      safeUnlink(srcBackup);
      safeSyncFS();
      refresh();
    } catch (e) {
      showUploadError('Move failed');
      showUploadStatus('Move failed', 'error', 3000);
      console.error('Move failed:', e);
    }
  }

  function attachTabDropHandlers(btn, dir) {
    btn.addEventListener('dragover', function(ev) {
      if (!dragSourcePath || !dir || dir === '__all__') return;
      ev.preventDefault();
      btn.classList.add('nq-drop-target');
    });
    btn.addEventListener('dragleave', function(ev) {
      var rt = ev.relatedTarget;
      if (rt && btn.contains(rt)) return;
      btn.classList.remove('nq-drop-target');
    });
    btn.addEventListener('drop', function(ev) {
      if (!dir || dir === '__all__') return;
      ev.preventDefault();
      btn.classList.remove('nq-drop-target');
      var src = (ev.dataTransfer && ev.dataTransfer.getData('text/nq-file-path')) || dragSourcePath;
      if (src) moveFileToDir(src, dir);
    });
  }

  function buildTabs() {
    var dirs = getDirs();
    var labels = ['all', 'GAME'];
    tabs.innerHTML = '';
    var allBtn = document.createElement('button');
    allBtn.className = 'nq-tab' + (currentDir === '__all__' ? ' active' : '');
    allBtn.textContent = 'all';
    allBtn.dataset.dir = '__all__';
    allBtn.onclick = function() { currentDir = '__all__'; refresh(); };
    attachTabDropHandlers(allBtn, '__all__');
    tabs.appendChild(allBtn);
    dirs.forEach(function(d) {
      var btn = document.createElement('button');
      var name = d.replace(/^\/|\/$/g, '');
      btn.className = 'nq-tab' + (currentDir === d ? ' active' : '');
      btn.textContent = name;
      btn.dataset.dir = d;
      btn.onclick = function() { currentDir = d; refresh(); };
      attachTabDropHandlers(btn, d);
      tabs.appendChild(btn);
      labels.push(name);
    });
    setTabsOpenWidth(labels);
  }

  function collectFiles(dir) {
    var out = [];
    safeReadDir(dir).forEach(function(n) {
      if (n === '.' || n === '..') return;
      var p = dir + (dir === '/' ? '' : '/') + n;
      var st = safeStat(p);
      if (!st) return;
      if (FS.isDir(st.mode)) out = out.concat(collectFiles(p));
      else if (FS.isFile(st.mode) && isUserFile(p)) out.push(p);
    });
    return out;
  }

  function refresh() {
    syncConfigToggle();
    buildTabs();
    var dirName = currentDir === '__all__' ? 'all' : currentDir.replace(/^\/|\/$/g, '');
    dirLabel.textContent = dirName;
    list.innerHTML = '';
    closeEditor();
    if (typeof FS === 'undefined') { list.innerHTML = '<li style="color:#888">FS not ready</li>'; return; }
    maybePersistUserFiles();

    var files = collectFiles(USERFS)
      .map(function(p) { return p.indexOf(USERFS + '/') === 0 ? p.slice(USERFS.length) : p; })
      .filter(function(p) { return p.startsWith('/'); });
    if (currentDir !== '__all__') files = files.filter(function(p) { return p.indexOf(currentDir) === 0; });
    files.sort();

    if (!files.length) { list.innerHTML = '<li style="color:#888">No user files</li>'; return; }

    files.forEach(function(displayPath) {
      var backupPath = USERFS + displayPath;
      var shownName = displayPath;
      if (currentDir !== '__all__' && displayPath.startsWith(currentDir)) {
        shownName = displayPath.slice(currentDir.length);
      }
      var li = document.createElement('li');
      li.draggable = true;
      li.addEventListener('dragstart', function(ev) {
        dragSourcePath = displayPath;
        li.classList.add('nq-dragging');
        if (ev.dataTransfer) {
          ev.dataTransfer.effectAllowed = 'move';
          ev.dataTransfer.setData('text/nq-file-path', displayPath);
        }
        setTabsOpen(true);
      });
      li.addEventListener('dragend', function() {
        li.classList.remove('nq-dragging');
        dragSourcePath = '';
        clearTabDropTargets();
      });

      var span = document.createElement('span');
      span.className = 'nq-fname';
      span.textContent = shownName;
      li.appendChild(span);

      // Edit button for .cfg files only
      if (isCfg(displayPath)) {
        var editBtn = document.createElement('button');
        editBtn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>';
        editBtn.title = 'Edit';
        editBtn.onclick = function() { openEditor(displayPath, backupPath); };
        li.appendChild(editBtn);
      }

      // Download
      var dlBtn = document.createElement('button');
      dlBtn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>';
      dlBtn.title = 'Download';
      dlBtn.onclick = function() { Module.exportFile(backupPath); };
      li.appendChild(dlBtn);

      // Delete
      var delBtn = document.createElement('button');
      delBtn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>';
      delBtn.className = 'nq-del';
      delBtn.title = 'Delete';
      delBtn.onclick = function() {
        if (!confirm('Delete ' + displayPath + '?')) return;
        safeUnlink(backupPath);
        safeUnlink(displayPath);
        safeSyncFS();
        refresh();
      };
      li.appendChild(delBtn);

      list.appendChild(li);
    });
  }

  // Inline editor
  function openEditor(displayPath, backupPath) {
    try {
      var data = FS.readFile(backupPath, {encoding: 'utf8'});
      editingFile = {display: displayPath, backup: backupPath};
      editorPath.textContent = displayPath;
      editorText.value = data;
      editor.classList.add('open');
      editorText.setSelectionRange(0, 0);
      editorText.scrollTop = 0;
      editorText.focus();
    } catch(e) {
      console.error('Failed to read file for editor:', e);
    }
  }

  function closeEditor() {
    if (!editor.classList.contains('open')) return false;
    editor.classList.remove('open');
    editingFile = null;
    editorText.value = '';
    return true;
  }

  editorSave.onclick = function() {
    if (!editingFile) return;
    var content = editorText.value;
    var data = new TextEncoder().encode(content);
    try {
      FS.writeFile(editingFile.backup, data);
      FS.writeFile(editingFile.display, data);
      safeSyncFS();
      closeEditor();
      refresh();
    } catch(e) {
      console.error('Save failed:', e);
    }
  };

  editorCancel.onclick = closeEditor;

  // Upload
  function getUploadDir() {
    return (currentDir === '__all__') ? '/id1/' : currentDir;
  }

  function isValidUpload(name) {
    var ext = name.slice(name.lastIndexOf('.') + 1).toLowerCase();
    return USER_EXTS.indexOf(ext) >= 0;
  }

  function readFileAsUint8(file, onProgress) {
    return new Promise(function(resolve, reject) {
      var reader = new FileReader();
      reader.onerror = function() {
        reject(reader.error || new Error('read failed'));
      };
      reader.onprogress = function(e) {
        if (onProgress) onProgress(e);
      };
      reader.onload = function(e) {
        resolve(new Uint8Array(e.target.result));
      };
      reader.readAsArrayBuffer(file);
    });
  }

  async function uploadFile(file, index, total) {
    if (!file || !isValidUpload(file.name)) {
      showUploadError('Quake ' + USER_FILE_DESC + ' only');
      return false;
    }
    clearUploadError();

    var dir = getUploadDir();
    var dirPath = dir.replace(/\/$/, '');
    var backupDir = USERFS + dirPath;
    var name = file.name.toLowerCase();
    var dstPath = dir + name;
    var dstBackupPath = backupDir + '/' + name;
    var label = 'Uploading ' + name + ' (' + index + '/' + total + ')';
    var data;
    if ((safeStat(dstPath) || safeStat(dstBackupPath)) && !confirm('Overwrite ' + dstPath + '?')) {
      showUploadStatus('Upload skipped for ' + name, '', 2500);
      return null;
    }

    try {
      showUploadStatus(label + ' 0%', 'active');
      data = await readFileAsUint8(file, function(e) {
        if (!e || !e.lengthComputable || e.total <= 0) {
          showUploadStatus(label + '...', 'active');
          return;
        }
        var pct = Math.max(0, Math.min(100, Math.round((e.loaded * 100) / e.total)));
        showUploadStatus(label + ' ' + pct + '%', 'active');
      });
    } catch (err) {
      showUploadError('Upload failed');
      showUploadStatus('Upload failed for ' + name, 'error', 3500);
      console.error('Upload read failed:', err);
      return false;
    }

    try {
      safeMkdirTree(dirPath);
      safeMkdirTree(backupDir);
      safeUnlink(dstPath);
      safeUnlink(dstBackupPath);
      FS.createDataFile(dirPath, name, data, true, true, true);
      FS.writeFile(dstBackupPath, data);
      return true;
    } catch (err2) {
      showUploadError('Upload failed');
      showUploadStatus('Upload failed for ' + name, 'error', 3500);
      console.error('Upload write failed:', err2);
      return false;
    }
  }

  async function processUploads(files) {
    if (uploadBusy) return;
    var queue = [];
    forEachFile(files, function(file) { queue.push(file); });
    if (!queue.length) return;

    setUploadBusyState(true);
    clearUploadError();
    clearUploadStatus();

    var uploaded = 0;
    var skipped = 0;
    var failed = 0;
    try {
      for (var i = 0; i < queue.length; i++) {
        var ok = await uploadFile(queue[i], i + 1, queue.length);
        if (ok === true) uploaded++;
        else if (ok === null) skipped++;
        else failed++;
      }

      if (uploaded > 0) {
        showUploadStatus('Syncing ' + uploaded + ' file(s) to storage...', 'active');
        var syncErr = await syncFSAsync();
        if (syncErr) {
          failed++;
          showUploadError('Storage sync failed');
          console.error('Upload sync failed:', syncErr);
        }
      }

      if (uploaded > 0) refresh();
      if (failed > 0) {
        showUploadStatus('Uploaded ' + uploaded + ', skipped ' + skipped + ', failed ' + failed, 'error', 4000);
      } else if (skipped > 0) {
        showUploadStatus('Uploaded ' + uploaded + ', skipped ' + skipped, '', 3500);
      } else {
        var totalBytes = queue.reduce(function(acc, file) { return acc + Number(file.size || 0); }, 0);
        showUploadStatus('Uploaded ' + uploaded + ' file(s) (' + formatBytes(totalBytes) + ')', '', 2500);
      }
    } finally {
      setUploadBusyState(false);
    }
  }

  upload.onclick = function() {
    if (uploadBusy) return;
    fileInput.click();
  };
  fileInput.onchange = function(e) {
    processUploads(e.target.files).catch(function(err) {
      showUploadError('Upload failed');
      showUploadStatus('Upload failed', 'error', 3000);
      console.error('Upload queue failed:', err);
    });
    fileInput.value = '';
  };

  // Drag-drop
  list.addEventListener('dragover', function(e) { e.preventDefault(); list.style.background = 'rgba(80,80,80,0.4)'; });
  list.addEventListener('dragleave', function(e) {
    e.preventDefault();
    list.style.background = '';
  });
  list.addEventListener('drop', function(e) {
    e.preventDefault();
    list.style.background = '';
    clearTabDropTargets();
    dragSourcePath = '';
    processUploads(e.dataTransfer && e.dataTransfer.files).catch(function(err) {
      showUploadError('Upload failed');
      showUploadStatus('Upload failed', 'error', 3000);
      console.error('Drop upload failed:', err);
    });
  });

  // Init
  var origInit = Module.onRuntimeInitialized;
  Module.onRuntimeInitialized = function() {
    if (origInit) origInit.call(Module);
    refresh();
  };
  Module.nqOverlayUpdateDirs = function() { refresh(); };
})();
