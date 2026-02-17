window.onerror = function() {
  Module.setStatus('exception thrown');
  loaderElement.classList.add('hidden');
  Module.setStatus = function(text) {
    if (text) console.error('[post-exception status] ' + text);
  };
};
