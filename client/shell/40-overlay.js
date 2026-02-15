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
  var uploadErrorTimer = null;

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

  function clearUploadError() {
    if (uploadErrorTimer) {
      clearTimeout(uploadErrorTimer);
      uploadErrorTimer = null;
    }
    if (uploadError) uploadError.textContent = '';
  }

  function showUploadError(msg) {
    if (!uploadError) return;
    clearUploadError();
    uploadError.textContent = msg || '';
    if (msg) {
      uploadErrorTimer = setTimeout(function() {
        uploadError.textContent = '';
        uploadErrorTimer = null;
      }, 3000);
    }
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

  function buildTabs() {
    var dirs = getDirs();
    var labels = ['all', 'GAME'];
    tabs.innerHTML = '';
    var allBtn = document.createElement('button');
    allBtn.className = 'nq-tab' + (currentDir === '__all__' ? ' active' : '');
    allBtn.textContent = 'all';
    allBtn.onclick = function() { currentDir = '__all__'; refresh(); };
    tabs.appendChild(allBtn);
    dirs.forEach(function(d) {
      var btn = document.createElement('button');
      var name = d.replace(/^\/|\/$/g, '');
      btn.className = 'nq-tab' + (currentDir === d ? ' active' : '');
      btn.textContent = name;
      btn.onclick = function() { currentDir = d; refresh(); };
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

  function uploadFile(file) {
    if (!file || !isValidUpload(file.name)) {
      showUploadError('Quake ' + USER_FILE_DESC + ' only');
      return;
    }
    clearUploadError();
    var dir = getUploadDir();
    var dirPath = dir.replace(/\/$/, '');
    var backupDir = USERFS + dirPath;
    var reader = new FileReader();
    reader.onload = function(e) {
      var data = new Uint8Array(e.target.result);
      var name = file.name.toLowerCase();
      try {
        safeMkdirTree(dirPath);
        safeMkdirTree(backupDir);
        safeUnlink(dir + name);
        safeUnlink(backupDir + '/' + name);
        FS.createDataFile(dirPath, name, data, true, true, true);
        FS.writeFile(backupDir + '/' + name, data);
        safeSyncFS();
        refresh();
      } catch(err) {
        showUploadError('Upload failed');
        console.error('Upload failed:', err);
      }
    };
    reader.readAsArrayBuffer(file);
  }

  function processUploads(files) {
    forEachFile(files, uploadFile);
  }

  upload.onclick = function() { fileInput.click(); };
  fileInput.onchange = function(e) {
    processUploads(e.target.files);
    fileInput.value = '';
  };

  // Drag-drop
  list.addEventListener('dragover', function(e) { e.preventDefault(); list.style.background = 'rgba(80,80,80,0.4)'; });
  list.addEventListener('dragleave', function(e) { e.preventDefault(); list.style.background = ''; });
  list.addEventListener('drop', function(e) {
    e.preventDefault();
    list.style.background = '';
    processUploads(e.dataTransfer && e.dataTransfer.files);
  });

  // Init
  var origInit = Module.onRuntimeInitialized;
  Module.onRuntimeInitialized = function() {
    if (origInit) origInit.call(Module);
    refresh();
  };
  Module.nqOverlayUpdateDirs = function() { refresh(); };
})();
