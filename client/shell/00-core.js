var loaderElement = document.getElementById('nq-loader');
var loaderSubElement = document.getElementById('nq-loader-sub');
var loaderStatusElement = document.getElementById('nq-loader-status');
var loaderProgressBar = document.getElementById('nq-loader-progress-bar');
var loaderReloadButton = document.getElementById('nq-loader-reload');
var overlayToggleElement = document.getElementById('nq-overlay-toggle');
/** @type {HTMLCanvasElement} */
var canvasElement = /** @type {HTMLCanvasElement} */ (document.getElementById('canvas'));
var outputElement = document.getElementById('output');
var exportElement = document.getElementById('exportFile');
var NEXQUAKE_GAMENAME = '__NEXQUAKE_GAMENAME__';
var NEXQUAKE_REMOTE_ROOT = '/__NEXQUAKE_REMOTE_ROOT_BASENAME__';
var NQ_USER_FILE_EXTS = ['cfg', 'sav', 'dem', 'pcx', 'pak'];
var NQ_CD_DIR = '/cd/';
var NQ_BOOTSTRAP_PHASE_TEXT = Object.freeze({
  1: 'instantiating wasm...',
  2: 'building vfs...',
  3: 'syncing saved data...'
});
var NQ_BOOTSTRAP_RUNNING_TEXT = 'running...';
var nqGameStarted = false;
var nqRuntimeReady = false;

if (loaderElement)
  loaderElement.classList.remove('enter-mode');
if (loaderProgressBar)
  loaderProgressBar.style.width = '0%';
if (loaderReloadButton) {
  loaderReloadButton.textContent = 'ENTER';
  loaderReloadButton.disabled = true;
  loaderReloadButton.classList.add('hidden');
}
if (overlayToggleElement)
  overlayToggleElement.style.display = 'none';

function nqSetOverlayToggleVisible(visible) {
  if (!overlayToggleElement)
    return;
  overlayToggleElement.style.display = visible ? '' : 'none';
}

function nqRequestFullscreen() {
  var request = null;
  try {
    var el = document.documentElement;
    var requestFullscreen = el.requestFullscreen || el.webkitRequestFullscreen;
    if (!requestFullscreen)
      return null;
    if (!document.fullscreenElement && !document.webkitFullscreenElement) {
      try {
        request = requestFullscreen.call(el, { navigationUI: 'hide' });
      } catch (optErr) {
        request = requestFullscreen.call(el);
      }
    }
  } catch (e) {}

  try {
    var orient = screen.orientation;
    if (orient && orient.lock) {
      var lockRequest = orient.lock('landscape');
      if (lockRequest && lockRequest.catch)
        lockRequest.catch(function() {});
    }
  } catch (e2) {}

  return request;
}

if (typeof Module === 'undefined' || !Module)
  Module = {};
Module.nqRequestFullscreen = nqRequestFullscreen;

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

function nqGetCdDir() {
  return NQ_CD_DIR;
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

function nqWasmCall(symbol, returnType, argTypes, args, fallbackValue) {
  if (typeof Module === 'undefined' || !Module || typeof Module.ccall !== 'function')
    return fallbackValue;
  try {
    return Module.ccall(symbol, returnType, argTypes || [], args || []);
  } catch (e) {
    return fallbackValue;
  }
}

function nqWasmExecCommand(command) {
  return nqWasmCall('NQWasm_ExecCommand', 'void', ['string'], [String(command || '')], null) !== null;
}

function nqWasmStartMainLoop() {
  return nqWasmCall('NQWasm_StartMainLoop', 'void', [], [], null) !== null;
}

function nqWasmGetKeyBinding(key) {
  var binding = nqWasmCall('NQWasm_GetKeyBinding', 'string', ['number'], [Number(key) | 0], '');
  return binding || '';
}

function nqWasmTextInputKey(key) {
  return nqWasmCall('NQWasm_TextInputKey', 'void', ['number'], [Number(key) | 0], null) !== null;
}

function nqWasmGetTextInputValue() {
  var value = nqWasmCall('NQWasm_GetTextInputValue', 'string', [], [], '');
  return value || '';
}

function nqWasmGetVideoWidth() {
  return Number(nqWasmCall('NQWasm_GetVideoWidth', 'number', [], [], 0)) | 0;
}

function nqWasmGetConnectedServerListenPort() {
  return Number(nqWasmCall('NQWasm_GetConnectedServerListenPort', 'number', [], [], 0)) | 0;
}

var NQ_PER_MOD_CONFIG_STORAGE_KEY = 'nexquake.per_mod_config';
var NQ_CD_ENABLED_STORAGE_KEY = 'nexquake.cd_enabled';

function nqLoadStoredBool(storageKey) {
  try {
    var raw = localStorage.getItem(storageKey);
    if (raw === '1' || raw === 'true') return true;
    if (raw === '0' || raw === 'false') return false;
  } catch (e) {}
  return null;
}

function nqSaveStoredBool(storageKey, value) {
  try { localStorage.setItem(storageKey, value ? '1' : '0'); } catch (e) {}
}

function nqLoadPerModConfig() {
  return nqLoadStoredBool(NQ_PER_MOD_CONFIG_STORAGE_KEY);
}

function nqSavePerModConfig(value) {
  nqSaveStoredBool(NQ_PER_MOD_CONFIG_STORAGE_KEY, value);
}

function nqLoadCdEnabled() {
  return nqLoadStoredBool(NQ_CD_ENABLED_STORAGE_KEY);
}

function nqSaveCdEnabled(value) {
  nqSaveStoredBool(NQ_CD_ENABLED_STORAGE_KEY, value);
}

function nqParseStartBundle(encoded) {
  var text = String(encoded || '').trim();
  var binary;

  try {
    if (!text)
      throw new Error('start bundle payload is empty');
    if (typeof atob !== 'function')
      throw new Error('base64 decode not supported in this runtime');
    if (typeof TextDecoder === 'undefined')
      throw new Error('TextDecoder is required for start bundle decode');
    binary = atob(text);
    return JSON.parse(new TextDecoder().decode(Uint8Array.from(binary, function(ch) {
      return ch.charCodeAt(0) & 255;
    })));
  } catch (err) {
    throw new Error('start bundle decode failed: ' + err);
  }
}

// Load /start before wasm instantiation so startup memory can follow client sendArgs.
function nqTryLoadStartBundleSync() {
  var moduleRef = (typeof Module !== 'undefined' && Module) ? Module : (window.Module = window.Module || {});
  var request;
  var assetRef;

  if (moduleRef.nexquakeStartBundle)
    return moduleRef.nexquakeStartBundle;

  try {
    request = new XMLHttpRequest();
    request.open('GET', '/start', false);
    request.send(null);
    if (request.status < 200 || request.status >= 300)
      throw new Error('start bundle fetch failed: ' + request.status);

    assetRef = String(request.getResponseHeader('X-NexQuake-Ref') || '');
    if (!assetRef)
      throw new Error('start bundle missing X-NexQuake-Ref header');

    moduleRef.nexquakeAssetRef = assetRef;
    moduleRef.nexquakeStartBundle = nqParseStartBundle(request.responseText);
    return moduleRef.nexquakeStartBundle;
  } catch (err) {
    console.warn('Failed to preload /start bundle before wasm instantiation:', err);
    return null;
  }
}
