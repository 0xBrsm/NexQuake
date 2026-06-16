// NexQuake in-game rcon client.
//
// Parses the `rcon <args>` text from cmd_rcon.c, builds a JSON-RPC 2.0 request
// against POST /rcon, authenticates via Authorization: Rcon <password>, and
// formats the structured response for Con_Printf display.
//
// Method catalog (see src/nexus/internal/admin/cmds.go):
//   server.list, server.instances, server.start, server.stop,
//   server.restart, server.remove, server.launch,
//   server.instance.command,
//   client.list, client.info, client.ban,
//   logs.tail
//
// Addressing rules:
//   - `rcon nexus <cmd...>` — explicit Nexus admin dispatch (always).
//   - `rcon <cmd...>` when connected — forwarded to that server's console via
//     server.instance.command (use `rcon nexus ...` to reach admin).
//   - `rcon <cmd...>` when not connected — implicit admin dispatch.
//
// Instance-console forms both resolve to server.instance.command:
//   - `rcon <port> <cmd...>`         — target that instance's console.
//   - `rcon <cmd...>` when connected — target the currently-connected listen port.
//
// Admin commands (preceded by "nexus" when connected, implicit otherwise):
//   help                             — rcon.help (server-rendered, auth-gated)
//   tail [N]                         — logs.tail
//   server list                      — server.list
//   server list all                  — server.instances (all servers, grouped)
//   server list <idx>                — server.instances {index: idx}
//   server start <idx|all>           — server.start
//   server stop <idx|all> [secs]     — server.stop
//   server restart <idx|all> [secs]  — server.restart
//   server remove <idx>              — server.remove
//   server launch <binary> [args...] — server.launch
//   client list                      — client.list
//   client info <nqip>               — client.info
//   client ban <nqip>                — client.ban
//
// Client-only commands (handled here, never sent to Nexus):
//   login                            — OIDC login: client-side Authorization
//                                      Code + PKCE when Nexus advertises OIDC
//                                      config (direct exposure), else an
//                                      edge-gated popup (fronting access gate).

(function () {
  'use strict';

  var RPC_URL = '/rcon';
  var REQUEST_TIMEOUT_MS = 10000;

  function resolveRPCURL() {
    if (/^[a-z][a-z0-9+.-]*:/i.test(RPC_URL)) return RPC_URL;
    if (typeof location !== 'undefined' && location && location.href) {
      return new URL(RPC_URL, location.href).toString();
    }
    if (typeof Module !== 'undefined' && Module && Module.nexusBaseUrl) {
      return new URL(RPC_URL, String(Module.nexusBaseUrl)).toString();
    }
    return RPC_URL;
  }

  // Tokenize a command line with simple quote handling (matches shlex enough
  // for typical rcon use: words separated by whitespace, "..." quotes).
  function tokenize(line) {
    var out = [];
    var i = 0, n = line.length;
    while (i < n) {
      while (i < n && /\s/.test(line[i])) i++;
      if (i >= n) break;
      var tok = '';
      if (line[i] === '"') {
        i++;
        while (i < n && line[i] !== '"') { tok += line[i++]; }
        if (i < n) i++; // closing quote
      } else {
        while (i < n && !/\s/.test(line[i])) { tok += line[i++]; }
      }
      out.push(tok);
    }
    return out;
  }

  function isIntegerString(s) {
    return /^[0-9]+$/.test(s);
  }

  // Dispatch an in-game rcon line to the right JSON-RPC method.
  // Returns one of:
  //   { method, params } — JSON-RPC call to make
  //   { clientCmd }      — client-only command (no server call)
  //   { error }          — client-side validation error
  // connectedPort is the currently-connected game-server listen port (or 0),
  // used as a fallback target for bare `rcon <cmd...>` while in-game.
  function planCall(tokens, connectedPort) {
    if (tokens.length === 0) return { method: 'rcon.help', params: {} };

    // Explicit numeric port always targets that server's console.
    var head0 = tokens[0];
    if (isIntegerString(head0) && tokens.length > 1) {
      var port = parseInt(head0, 10);
      if (port >= 1 && port <= 65535) {
        return { method: 'server.instance.command', params: { port: port, cmd: tokens.slice(1).join(' ') } };
      }
    }

    // Explicit "nexus" prefix forces admin dispatch. When connected, this is
    // required to reach admin (bare `rcon <cmd...>` otherwise forwards to the
    // connected server's console).
    var explicitAdmin = false;
    if (tokens[0].toLowerCase() === 'nexus') {
      if (tokens.length < 2) return { error: 'usage: rcon nexus <cmd...>\n' };
      tokens = tokens.slice(1);
      explicitAdmin = true;
    }

    // Client-only commands always run locally — never forwarded to a connected
    // server's console, never sent to Nexus.
    if (tokens[0].toLowerCase() === 'login') {
      return { clientCmd: 'login' };
    }

    // When connected without an explicit admin prefix, forward everything to
    // the connected server's console.
    if (!explicitAdmin && connectedPort > 0 && connectedPort <= 65535) {
      return { method: 'server.instance.command', params: { port: connectedPort, cmd: tokens.join(' ') } };
    }

    // Admin dispatch (explicit "nexus" or disconnected implicit).
    var head = tokens[0].toLowerCase();
    var rest = tokens.slice(1);

    if (head === 'help') return { method: 'rcon.help', params: {} };

    if (head === 'tail') {
      var lines = 0;
      if (rest.length >= 1) {
        var n = parseInt(rest[0], 10);
        if (!isNaN(n) && n > 0) lines = n;
      }
      var params = lines > 0 ? { lines: lines } : {};
      return { method: 'logs.tail', params: params };
    }

    if (head === 'server') {
      var sVerb = rest.length > 0 ? rest[0].toLowerCase() : '';
      var sArgs = rest.slice(1);
      var serverUsage = 'usage: rcon server list|start|stop|restart|remove|launch ...\n';

      if (sVerb === 'list') {
        if (sArgs.length === 0) return { method: 'server.list', params: {} };
        if (sArgs.length === 1) {
          if (sArgs[0].toLowerCase() === 'all') {
            return { method: 'server.instances', params: {} };
          }
          if (isIntegerString(sArgs[0])) {
            return { method: 'server.instances', params: { index: parseInt(sArgs[0], 10) } };
          }
        }
        return { error: 'usage: rcon server list [<idx>|all]\n' };
      }

      if (sVerb === 'start' || sVerb === 'stop' || sVerb === 'restart') {
        if (sArgs.length < 1) return { error: 'usage: rcon server ' + sVerb + ' <idx|all> [grace-seconds]\n' };
        var sp = { target: sArgs[0] };
        if ((sVerb === 'stop' || sVerb === 'restart') && sArgs.length >= 2) {
          var sg = parseInt(sArgs[1], 10);
          if (!isNaN(sg) && sg > 0) sp.grace_seconds = sg;
        }
        return { method: 'server.' + sVerb, params: sp };
      }

      if (sVerb === 'remove') {
        if (sArgs.length < 1 || !isIntegerString(sArgs[0])) {
          return { error: 'usage: rcon server remove <idx>\n' };
        }
        return { method: 'server.remove', params: { index: parseInt(sArgs[0], 10) } };
      }

      if (sVerb === 'launch') {
        if (sArgs.length < 1) return { error: 'usage: rcon server launch <binary> [args...]\n' };
        return { method: 'server.launch', params: { binary: sArgs[0], args: sArgs.slice(1) } };
      }

      return { error: serverUsage };
    }

    if (head === 'client') {
      if (rest.length < 1) return { error: 'usage: rcon client list | info <nqip> | ban <nqip>\n' };
      var sub = rest[0].toLowerCase();
      if (sub === 'list') return { method: 'client.list', params: {} };
      if (sub === 'info' && rest.length >= 2) return { method: 'client.info', params: { nqip: rest[1] } };
      if (sub === 'ban' && rest.length >= 2) return { method: 'client.ban', params: { nqip: rest[1] } };
      return { error: 'usage: rcon client list | info <nqip> | ban <nqip>\n' };
    }

    // Unknown admin verb.
    return { error: 'rcon: unknown command ' + JSON.stringify(head) + ' (try: rcon help)\n' };
  }

  // --- Response formatters (one per method) ---

  function pad(s, w) {
    return String(s ?? '').padEnd(w);
  }

  function formatServerList(result) {
    var servers = (result && result.servers) || [];
    if (servers.length === 0) return '\nno quake servers found.\n\n';
    var out = '\n' +
      pad('#', 3) + ' ' + pad('Server', 15) + ' ' + pad('Candidate', 9) + ' ' +
      pad('Game', 15) + ' ' + pad('Users', 12) + ' ' + 'State\n' +
      '--- --------------- --------- --------------- ------------ --------\n';
    for (var i = 0; i < servers.length; i++) {
      var s = servers[i];
      var users = formatServerUsers(s);
      out += pad(String(i + 1), 3) + ' ' + pad(s.hostname || 'unnamed', 15) + ' ' +
             pad(String(s.candidate_port || ''), 9) + ' ' +
             pad(s.game_dir || '?', 15) + ' ' + pad(users, 12) + ' ' + (s.state || '') + '\n';
    }
    return out + '== end list ==\n\n';
  }

  function formatServerUsers(p) {
    if (p.max_players > 0) {
      var base = p.players + '/' + p.max_players;
      return p.instances > 0 ? base + ' (' + p.instances + ')' : base;
    }
    return '--/--';
  }

  function formatServerInstances(result) {
    var servers = (result && result.servers) || [];
    if (servers.length === 0) return '\nno quake servers found.\n\n';
    var out = '';
    var any = false;
    for (var i = 0; i < servers.length; i++) {
      var s = servers[i];
      out += '\n[' + s.index + '] ' + (s.hostname || 'unnamed') +
             '  game=' + (s.game_dir || '?') +
             '  users=' + formatServerUsers(s) +
             (s.candidate_port ? '  candidate=' + s.candidate_port : '') +
             '  state=' + (s.state || '') + '\n';
      if (!s.instances || s.instances.length === 0) {
        out += '    (no instances)\n';
        continue;
      }
      any = true;
      out += '    #  Port  Map             Users   State\n' +
             '    -- ----- --------------- ------- --------\n';
      for (var j = 0; j < s.instances.length; j++) {
        var inst = s.instances[j];
        var users = inst.max_players > 0 ? (inst.players + '/' + inst.max_players) : '--/--';
        out += '    ' + pad(String(j + 1), 2) + ' ' + pad(String(inst.listen_port || ''), 5) + ' ' +
               pad(inst.map_name || '?', 15) + ' ' + pad(users, 7) + ' ' + (inst.state || '') + '\n';
      }
    }
    if (!any) return '\nno running server instances found.\n\n';
    return out + '== end list ==\n\n';
  }

  // Transport tag shown in parens after the source IP, mirroring the nexus
  // console's "<id> connected (WebTransport)" style. The list table uses the
  // short form to stay inside the 78-column budget; unknown transports fall
  // back to the full name.
  function transportTag(t) {
    if (!t) return '';
    if (t === 'WebTransport') return ' (wt)';
    if (t === 'WebSocket') return ' (ws)';
    return ' (' + t.toLowerCase() + ')';
  }

  function formatClientList(result) {
    var clients = (result && result.clients) || [];
    if (clients.length === 0) return '\nno active clients.\n\n';
    var out = '\n' +
      pad('NQIP', 15) + ' ' + pad('Source', 20) + ' ' + pad('Identity', 19) + ' ' +
      pad('Server', 15) + ' ' + 'Port\n' +
      '--------------- -------------------- ------------------- --------------- -----\n';
    for (var i = 0; i < clients.length; i++) {
      var s = clients[i];
      out += pad(s.nqip, 15) + ' ' + pad((s.source_ip || '-') + transportTag(s.transport), 20) + ' ' +
             pad(s.identity || '(anonymous)', 19) + ' ' +
             pad(s.server_host || '-', 15) + ' ' +
             (s.server_port > 0 ? String(s.server_port) : '-') + '\n';
    }
    return out + '== end list ==\n\n';
  }

  function formatClientInfo(result) {
    if (!result || !result.client) return 'client not found\n';
    var s = result.client;
    var out = '\nclient ' + s.nqip + '\n' +
      '  source ip: ' + (s.source_ip || 'unknown') +
        (s.transport ? ' (' + s.transport.toLowerCase() + ')' : '') + '\n' +
      '  identity:  ' + (s.identity || '(anonymous)') + '\n' +
      '  server:    ' + (s.server_host || '-') +
        (s.server_port > 0 ? ' (:' + s.server_port + ')' : '') + '\n';
    if (result.status_slot) {
      out += '  slot:      #' + result.status_slot + '\n' +
             '  line:      ' + (result.status_line || '') + '\n' +
             '  addr:      ' + (result.status_addr || '') + '\n';
    } else if (result.status_note) {
      out += '  status:    ' + result.status_note + '\n';
    }
    return out + '\n';
  }

  function formatClientBan(result) {
    if (!result || !result.nqip) return 'ban failed: empty reply\n';
    var out = '\nbanned ' + result.nqip + '\n' +
      '  disconnected: ' + result.disconnected + ' client(s)\n';
    if (result.source_ips && result.source_ips.length > 0) {
      out += '  source ip(s): ' + result.source_ips.join(', ') + '\n';
    }
    if (result.warnings && result.warnings.length > 0) {
      for (var i = 0; i < result.warnings.length; i++) {
        out += '  warning: ' + result.warnings[i] + '\n';
      }
    }
    return out + '\n';
  }

  function formatLogsTail(result) {
    var lines = (result && result.lines) || [];
    if (lines.length === 0) return 'no buffered Nexus logs.\n';
    return lines.join('\n') + '\n';
  }

  function okFormatter(flag, successMsg) {
    return function (r) { return r && r[flag] ? successMsg : 'failed\n'; };
  }

  var FORMATTERS = {
    'rcon.help':               function (r) {
                                 var text = (r && r.text) ? r.text : '';
                                 // login is a WASM-client-only command (drives an OIDC popup
                                 // the API can't reach) so it lives in the client's help layout,
                                 // not in Nexus's server-rendered text.
                                 return text +
                                   'client-only commands (in-game shell):\n' +
                                   '  rcon login                            authenticate via OIDC popup\n\n';
                               },
    'server.list':             formatServerList,
    'server.instances':        formatServerInstances,
    'server.start':            okFormatter('ok', 'complete\n'),
    'server.stop':             okFormatter('ok', 'complete\n'),
    'server.restart':          okFormatter('ok', 'complete\n'),
    'server.remove':           okFormatter('removed', 'server removed\n'),
    'server.launch':           okFormatter('ok', 'server launched\n'),
    'server.instance.command': function (r) { return (r && r.reply) ? r.reply : ''; },
    'client.list':             formatClientList,
    'client.info':             formatClientInfo,
    'client.ban':              formatClientBan,
    'logs.tail':               formatLogsTail,
  };

  function formatResult(method, result) {
    var fn = FORMATTERS[method];
    return fn ? fn(result) : JSON.stringify(result) + '\n';
  }

  // --- HTTP transport ---

  async function postRPC(method, params, password) {
    var headers = { 'Content-Type': 'application/json' };
    var rpcURL = resolveRPCURL();
    // The PKCE login stores its verified id_token in an httpOnly nq_session
    // cookie that rides this same-origin request automatically (see credentials
    // below) and carries per-admin identity for the audit log. The rcon
    // password, when set, is sent as an explicit fallback header for
    // deployments without OIDC.
    if (password) headers['Authorization'] = 'Rcon ' + password;

    var controller = new AbortController();
    var timer = setTimeout(function () { controller.abort(); }, REQUEST_TIMEOUT_MS);
    try {
      var envelope = { jsonrpc: '2.0', method: method, params: params, id: 1 };
      // redirect: 'manual' so a CF-Access-style cross-origin login redirect
      // surfaces as response.type === 'opaqueredirect' instead of throwing
      // CORS. The caller pops a top-level login window in that case.
      var resp = await fetch(rpcURL, {
        method: 'POST',
        headers: headers,
        body: JSON.stringify(envelope),
        credentials: 'same-origin',
        redirect: 'manual',
        signal: controller.signal,
      });
      if (resp.type === 'opaqueredirect') {
        return { needsLogin: true };
      }
      var parsed;
      try { parsed = await resp.json(); }
      catch (e) { return { error: 'rcon: invalid response (' + resp.status + ')\n' }; }
      if (parsed.error) {
        var msg = 'rcon: ' + (parsed.error.message || 'error');
        var hint = parsed.error.data && parsed.error.data.hint;
        if (hint) msg += '. ' + hint;
        return { error: msg + '\n' };
      }
      return { result: parsed.result };
    } catch (e) {
      if (e && e.name === 'AbortError') return { error: 'rcon: request timed out\n' };
      return { error: 'rcon: ' + (e && e.message ? e.message : String(e)) + '\n' };
    } finally {
      clearTimeout(timer);
    }
  }

  // --- OIDC client-side login (Authorization Code + PKCE) ---
  //
  // Used when Nexus is exposed directly with OIDC configured (Module.nexquakeOIDC
  // present): the shell runs the whole flow itself and holds the resulting
  // id_token, sent as Bearer on POST /rcon. When OIDC config is absent we fall
  // back to runEdgeLogin (a fronting access gate does the IdP round-trip).
  //
  // COOP-same-origin on the WASM page severs window.opener, so the popup can't
  // message us back. Instead the GET /rcon callback page writes the code+state
  // to localStorage and we pick it up here via a storage event (with a poll as
  // a belt-and-braces fallback). We then exchange the code for tokens directly
  // against the IdP — a public-client, no-secret flow IdPs enable CORS for.

  var OIDC_CALLBACK_KEY = 'nq_rcon_oidc_cb';  // popup -> opener handoff (code/state)

  // base64url-encode a byte array (no padding), per RFC 7636.
  function b64url(bytes) {
    var s = '';
    for (var i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
    return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  }

  // A fresh high-entropy URL-safe string, used for both the PKCE verifier and
  // the CSRF state parameter.
  function randomToken() {
    var a = new Uint8Array(32);
    crypto.getRandomValues(a);
    return b64url(a);
  }

  async function pkceChallenge(verifier) {
    var digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier));
    return b64url(new Uint8Array(digest));
  }

  async function discoverOidc(issuer) {
    var url = issuer.replace(/\/+$/, '') + '/.well-known/openid-configuration';
    var resp = await fetch(url, { credentials: 'omit' });
    if (!resp.ok) throw new Error('OIDC discovery failed (' + resp.status + ')');
    var meta = await resp.json();
    if (!meta || !meta.authorization_endpoint || !meta.token_endpoint) {
      throw new Error('OIDC discovery missing endpoints');
    }
    return meta;
  }

  // Resolve once the callback page hands back {code} for our state, or reject on
  // IdP error / state mismatch / timeout. Listens for the storage event and also
  // polls, since the event only fires in *other* documents and timing varies.
  function waitForOidcCallback(expectedState) {
    return new Promise(function (resolve, reject) {
      var settled = false, poll, timer;
      function settle(fn, arg) {
        if (settled) return;
        settled = true;
        window.removeEventListener('storage', onStorage);
        clearInterval(poll);
        clearTimeout(timer);
        fn(arg);
      }
      function tryRead() {
        var raw;
        try { raw = localStorage.getItem(OIDC_CALLBACK_KEY); } catch (e) { return; }
        if (!raw) return;
        try { localStorage.removeItem(OIDC_CALLBACK_KEY); } catch (e) { /* ignore */ }
        var o;
        try { o = JSON.parse(raw); } catch (e) { return; }
        if (!o) return;
        if (o.error) return settle(reject, new Error(o.error));
        if (o.state !== expectedState) return settle(reject, new Error('login state mismatch'));
        if (!o.code) return settle(reject, new Error('login returned no code'));
        settle(resolve, o.code);
      }
      function onStorage(e) { if (e.key === OIDC_CALLBACK_KEY) tryRead(); }
      window.addEventListener('storage', onStorage);
      poll = setInterval(tryRead, 300);
      timer = setTimeout(function () { settle(reject, new Error('login timed out')); }, 180000);
      tryRead(); // a fast popup may have written before we attached the listener
    });
  }

  async function runPkceLogin(oidc) {
    if (!(window.crypto && crypto.subtle)) {
      showAdminToast('rcon: this browser lacks WebCrypto; cannot run OIDC login.');
      return '';
    }
    var meta;
    try {
      meta = await discoverOidc(oidc.issuer);
    } catch (e) {
      showAdminToast('rcon: ' + (e && e.message ? e.message : String(e)));
      return '';
    }

    var verifier = randomToken();
    var state = randomToken();
    var challenge = await pkceChallenge(verifier);
    var redirectUri = new URL('/rcon', location.href).toString();
    var scope = oidc.scopes || 'openid profile email';

    var authURL = new URL(meta.authorization_endpoint);
    authURL.searchParams.set('response_type', 'code');
    authURL.searchParams.set('client_id', oidc.clientId);
    authURL.searchParams.set('redirect_uri', redirectUri);
    authURL.searchParams.set('scope', scope);
    authURL.searchParams.set('state', state);
    authURL.searchParams.set('code_challenge', challenge);
    authURL.searchParams.set('code_challenge_method', 'S256');

    try { localStorage.removeItem(OIDC_CALLBACK_KEY); } catch (e) { /* ignore stale */ }

    var pending = waitForOidcCallback(state);
    setRconLoginActive(true);
    var popup = window.open(authURL.toString(), 'nq_rcon_login', 'width=500,height=650');
    if (!popup) {
      setRconLoginActive(false);
      showAdminToast('rcon: pop-up blocked. allow pop-ups for this site, then run rcon login.');
      return '';
    }

    // Detach the long, interactive tail — waiting for the popup's callback, then
    // the server-side token exchange — from the caller. `rcon login` runs inside
    // a suspended Asyncify engine frame (cmd_rcon.c); awaiting the full flow here
    // would freeze the engine for the entire login and idle out the game
    // transport. The outcome is shown by the admin toast (whose OK click restores
    // fullscreen), not the console — async JS can't print to the engine console.
    completePkceLogin(pending, verifier);
    return '';
  }

  // nqRconLoginActive marks an in-flight login so the shell suppresses its
  // "tab regained focus -> open the overlay" reaction (see 21-touch-controls.js):
  // the IdP popup backgrounds the tab, and on return we want the admin toast, not
  // the overlay. Cleared once the toast is dismissed.
  function setRconLoginActive(active) {
    try {
      if (typeof Module !== 'undefined' && Module)
        Module.nqRconLoginActive = !!active;
    } catch (e) { /* ignore */ }
  }

  // showAdminToast surfaces the login outcome via the overlay's single-OK notice
  // (DOM, so it renders even when unfullscreened — the engine console can't be
  // printed to asynchronously). The OK click is the user gesture that re-enters
  // fullscreen; the engine state was never touched, so the user lands back
  // exactly where they launched `rcon login`. Resolves when dismissed.
  function showAdminToast(message) {
    var ctx = (typeof Module !== 'undefined' && Module && Module.nqOverlayCtx) || null;
    if (!ctx || typeof ctx.noticeAsync !== 'function')
      return Promise.resolve(false);
    return ctx.noticeAsync(message, 'OK', function () {
      try {
        if (typeof Module.nqRequestFullscreen === 'function')
          Module.nqRequestFullscreen();
      } catch (e) { /* ignore */ }
    });
  }

  // completePkceLogin runs the detached tail of runPkceLogin: await the code the
  // callback page relays, POST it with the PKCE verifier to Nexus's same-origin
  // /rcon/session (which exchanges it server-side — the IdP token endpoint isn't
  // browser-CORS-reachable — and sets the httpOnly nq_session cookie so the
  // id_token never enters JS), then show the admin toast with the outcome. Never
  // throws; it owns the nqRconLoginActive flag for the flow's lifetime.
  async function completePkceLogin(pending, verifier) {
    var message;
    try {
      var code = await pending;
      var resp = await fetch(new URL('/rcon/session', location.href).toString(), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ code: code, code_verifier: verifier }),
      });
      if (resp.ok) {
        var data = {};
        try { data = await resp.json(); } catch (e) { /* tolerate empty body */ }
        message = (data && data.authorized)
          ? 'rcon: authenticated.'
          : 'rcon: signed in, but not an admin.';
      } else {
        message = 'rcon: login failed (HTTP ' + resp.status + ').';
      }
    } catch (e) {
      message = 'rcon: login failed: ' + (e && e.message ? e.message : String(e));
    }
    try {
      await showAdminToast(message);
    } finally {
      setRconLoginActive(false);
    }
  }

  // Edge-gated login: open /rcon as a top-level popup so a fronting access gate
  // (e.g. Cloudflare Access) runs its IdP flow and sets its cookie. If already
  // authenticated the popup just flashes through the auto-close landing page;
  // either way Nexus pushes "rcon: authenticated." down the trunk control
  // channel once a GET /rcon hit carries admin-grade credentials. The shell
  // discovers success by polling POST /rcon — no client-side state checks.
  function runEdgeLogin() {
    var loginURL = resolveRPCURL();
    var popup = window.open(loginURL, 'nq_rcon_login', 'width=500,height=600');
    if (!popup) {
      return 'rcon: pop-up blocked. open ' + loginURL + ' in a new tab,\n' +
             '      log in to continue.\n';
    }
    return '';
  }

  // Route `rcon login` to the client-side PKCE flow when Nexus advertises OIDC
  // config, otherwise to the edge-gated popup. Returns a string or a Promise of
  // one; the caller awaits either.
  function runClientLogin() {
    var oidc = (typeof Module !== 'undefined' && Module && Module.nexquakeOIDC) || null;
    if (oidc && oidc.issuer && oidc.clientId) {
      return runPkceLogin(oidc);
    }
    return runEdgeLogin();
  }

  // Exported entry point — called from cmd_rcon.c via EM_ASYNC_JS.
  // connectedPort is the currently-connected server's listen port (0 if none).
  Module.nqRcon = async function (password, argsLine, connectedPort) {
    // Never throw: the caller is a suspended Asyncify frame in the engine
    // (cmd_rcon.c), and an escaped exception would hang it. Formatters can
    // trip on unexpected reply shapes — surface that as text.
    try {
      var tokens = tokenize(String(argsLine || ''));
      var plan = planCall(tokens, connectedPort | 0);
      if (plan.error) return plan.error;
      if (plan.clientCmd === 'login') return await runClientLogin();

      var r = await postRPC(plan.method, plan.params, String(password || ''));
      if (r.needsLogin) return 'rcon: not authenticated. run: rcon login.\n';
      if (r.error) return r.error;
      return formatResult(plan.method, r.result);
    } catch (e) {
      return 'rcon error: ' + String(e && e.message || e) + '\n';
    }
  };
})();
