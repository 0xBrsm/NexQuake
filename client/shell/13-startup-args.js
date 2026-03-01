(function() {
  function parseURLArgs() {
    var out = [];
    var tokens = window.location.search.length > 1 ? window.location.search.slice(1).split('&') : [];
    var i;
    var token;

    for (i = 0; i < tokens.length; i++) {
      token = tokens[i];
      if (!token)
        continue;
      if (token.indexOf('=') !== -1)
        continue;
      try {
        token = decodeURIComponent(token);
      } catch (e) {}
      token = String(token || '').trim();
      if (!token)
        continue;
      out.push(token);
    }

    return out;
  }

  function buildMainArgs() {
    var out = [];
    var source = Array.isArray(Module.nexquakeSendArgs) ? Module.nexquakeSendArgs : [];
    var i;
    var token;
    var urlArgs;

    for (i = 0; i < source.length; i++) {
      token = String(source[i] || '').trim();
      if (!token)
        continue;
      out.push(token);
    }

    if (Module.nexquakeURLArgs === true) {
      urlArgs = parseURLArgs();
      for (i = 0; i < urlArgs.length; i++)
        out.push(urlArgs[i]);
    }

    return out;
  }

  Module.nexquakeBuildMainArgs = buildMainArgs;
})();
