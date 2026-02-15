var loaderElement = document.getElementById('nq-loader');
var loaderStatusElement = document.getElementById('nq-loader-status');
var loaderProgressBar = document.getElementById('nq-loader-progress-bar');
var canvasElement = document.getElementById('canvas');
var outputElement = document.getElementById('output');
var exportElement = document.getElementById('exportFile');
var NEXQUAKE_GAMENAME = '__NEXQUAKE_GAMENAME__';
var NQ_USER_FILE_EXTS = ['cfg', 'sav', 'dem', 'pcx', 'pak'];

function nqNormalizeGameName(name) {
  name = String(name || '').trim();
  return name || NEXQUAKE_GAMENAME;
}

function nqGetBaseGameName() {
  return nqNormalizeGameName(Module.nexquakeBaseGameName || NEXQUAKE_GAMENAME);
}

function nqGetUserFileExts() {
  return NQ_USER_FILE_EXTS.slice();
}

function nqSafeReadDir(path) {
  try { return FS.readdir(path); } catch (e) { return []; }
}

function nqSafeStat(path) {
  try { return FS.stat(path); } catch (e) { return null; }
}

function nqSafeMkdirTree(path) {
  try { FS.mkdirTree(path); } catch (e) {}
}

function nqSafeUnlink(path) {
  try { FS.unlink(path); } catch (e) {}
}

function nqSafeSyncFS() {
  try { FS.syncfs(false, function(){}); } catch (e) {}
}

var NQ_PER_MOD_CONFIG_STORAGE_KEY = 'nexquake.per_mod_config';

function nqLoadPerModConfig() {
  try {
    var raw = localStorage.getItem(NQ_PER_MOD_CONFIG_STORAGE_KEY);
    if (raw === '1' || raw === 'true') return true;
    if (raw === '0' || raw === 'false') return false;
  } catch (e) {}
  return null;
}

function nqSavePerModConfig(value) {
  try { localStorage.setItem(NQ_PER_MOD_CONFIG_STORAGE_KEY, value ? '1' : '0'); } catch (e) {}
}
