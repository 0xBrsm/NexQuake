(function() {
  var args = [];
  var tokens = window.location.search.length > 1 ? window.location.search.substr(1).split('&') : [];
  for (var i = 0; i < tokens.length; i++) {
    var t = tokens[i];
    if (!t) continue;
    // Keep legacy behavior for flags like ?-nosound but avoid passing key=value params to Quake.
    if (t.indexOf('=') !== -1) continue;
    try { t = decodeURIComponent(t); } catch (e) {}
    args.push(t);
  }

  Module['arguments'] = args;
})();
