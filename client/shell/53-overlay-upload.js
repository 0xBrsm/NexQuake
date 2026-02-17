// nq-overlay: upload queue and file writes
(function() {
  if (!Module || !Module.nqOverlayInstall) return;

  Module.nqOverlayInstall(function(ctx) {
    function formatBytes(bytes) {
      var n = Number(bytes || 0);
      if (n < 1024) return n + ' B';
      if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
      return (n / (1024 * 1024)).toFixed(1) + ' MB';
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
          var result = e && e.target ? e.target.result : null;
          if (!(result instanceof ArrayBuffer)) {
            reject(new Error('Unexpected file reader result'));
            return;
          }
          resolve(new Uint8Array(result));
        };
        reader.readAsArrayBuffer(file);
      });
    }

    async function uploadFile(file, index, total, options) {
      var opts = options || {};
      var allowedExts = opts.extensions || ctx.USER_EXTS;
      var invalidText = opts.invalidText || ('Quake ' + ctx.USER_FILE_DESC + ' only');
      var dir = String(opts.dir || ctx.getUploadDir());
      var dirPath;
      var backupDir;
      var name;
      var dstPath;
      var dstBackupPath;
      var label;
      var data;

      if (!file || allowedExts.indexOf(file.name.slice(file.name.lastIndexOf('.') + 1).toLowerCase()) < 0) {
        ctx.showErrorMessage(invalidText, 3000);
        return false;
      }
      ctx.clearStatusMessage('upload-progress');

      dirPath = dir.replace(/\/$/, '');
      backupDir = ctx.USERFS + dirPath;
      name = file.name.toLowerCase();
      dstPath = dir + name;
      dstBackupPath = backupDir + '/' + name;
      label = 'Uploading ' + name + ' (' + index + '/' + total + ')';

      if (ctx.safeStat(dstBackupPath)) {
        if (!await ctx.confirmAsync('Overwrite ' + dstPath + '?', 'Overwrite'))
          return null;
      }

      try {
        ctx.showWarningMessage(label + ' 0%', 0, true, { key: 'upload-progress' });
        data = await readFileAsUint8(file, function(e) {
          var pct;
          if (!e || !e.lengthComputable || e.total <= 0) {
            ctx.showWarningMessage(label + '...', 0, true, { key: 'upload-progress' });
            return;
          }
          pct = Math.max(0, Math.min(100, Math.round((e.loaded * 100) / e.total)));
          ctx.showWarningMessage(label + ' ' + pct + '%', 0, true, { key: 'upload-progress' });
        });
      } catch (err) {
        ctx.showErrorMessage('Upload failed for ' + name, 3500);
        console.error('Upload read failed:', err);
        return false;
      }

      try {
        ctx.safeMkdirTree(backupDir);
        try { FS.symlink(backupDir, dirPath); } catch (e3) {}
        ctx.safeUnlink(dstBackupPath);
        FS.writeFile(dstBackupPath, data);
        return true;
      } catch (err2) {
        ctx.showErrorMessage('Upload failed for ' + name, 3500);
        console.error('Upload write failed:', err2);
        return false;
      }
    }

    async function processUploads(files, options) {
      var queue = [];
      var opts = options || {};
      var refreshOnSuccess = opts.refreshOnSuccess !== false;
      var uploaded = 0;
      var syncErr;
      var totalBytes;
      var i;

      if (ctx.uploadBusy) return;

      if (files) {
        for (i = 0; i < files.length; i++) queue.push(files[i]);
      }
      if (!queue.length) return;

      ctx.setUploadBusyState(true);
      ctx.clearStatusMessage('upload-progress');

      try {
        for (i = 0; i < queue.length; i++) {
          var ok = await uploadFile(queue[i], i + 1, queue.length, opts);
          if (ok === true) uploaded++;
        }

        if (uploaded > 0) {
          ctx.showWarningMessage('Syncing ' + uploaded + ' file(s) to storage...', 0, true, { key: 'upload-progress' });
          syncErr = await new Promise(function(resolve) {
            try {
              FS.syncfs(false, function(err) { resolve(err || null); });
            } catch (e) {
              resolve(e || null);
            }
          });
          if (syncErr) {
            ctx.showErrorMessage('Storage sync failed', 3500);
            console.error('Upload sync failed:', syncErr);
          }
        }

        if (uploaded > 0 && refreshOnSuccess) ctx.refresh();
        ctx.clearStatusMessage('upload-progress');

        if (uploaded > 0) {
          totalBytes = queue.reduce(function(acc, file) { return acc + Number(file.size || 0); }, 0);
          ctx.showInfoMessage('Uploaded ' + uploaded + ' file(s) (' + formatBytes(totalBytes) + ')', 2500);
        }
      } finally {
        ctx.clearStatusMessage('upload-progress');
        ctx.setUploadBusyState(false);
      }
    }

    ctx.processUploads = processUploads;
  });
})();
