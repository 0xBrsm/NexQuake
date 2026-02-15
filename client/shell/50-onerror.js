window.onerror = function() {
  Module.setStatus('Exception thrown, see JavaScript console');
  loaderElement.classList.add('hidden');
  Module.setStatus = function(text) {
    if (text) console.error('[post-exception status] ' + text);
  };
};
