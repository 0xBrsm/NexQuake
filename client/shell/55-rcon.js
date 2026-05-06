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
//   login                            — drive an OIDC popup for edge-gated /rcon

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
      pad('#', 3) + ' ' + pad('server', 15) + ' ' + pad('candidate', 9) + ' ' +
      pad('game', 15) + ' ' + pad('users', 12) + ' ' + 'state\n' +
      '--- --------------- --------- --------------- ------------ --------\n';
    for (var i = 0; i < servers.length; i++) {
      var s = servers[i];
      var users = s.max_players > 0
        ? (s.players + '/' + s.max_players + (s.instances > 0 ? ' (' + s.instances + ')' : ''))
        : '--/--';
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
      out += '    #  port  map             users   state\n' +
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

  function formatClientList(result) {
    var clients = (result && result.clients) || [];
    if (clients.length === 0) return '\nno active clients.\n\n';
    var out = '\n' +
      pad('nqip', 15) + ' ' + pad('source', 15) + ' ' + pad('identity', 24) + ' ' +
      pad('server', 15) + ' ' + 'port\n' +
      '--------------- --------------- ------------------------ --------------- -----\n';
    for (var i = 0; i < clients.length; i++) {
      var s = clients[i];
      out += pad(s.nqip, 15) + ' ' + pad(s.source_ip || '-', 15) + ' ' +
             pad(s.identity || '(anonymous)', 24) + ' ' +
             pad(s.server_host || '-', 15) + ' ' +
             (s.server_port > 0 ? String(s.server_port) : '-') + '\n';
    }
    return out + '== end list ==\n\n';
  }

  function formatClientInfo(result) {
    if (!result || !result.client) return 'client not found\n';
    var s = result.client;
    var out = '\nclient ' + s.nqip + '\n' +
      '  source ip: ' + (s.source_ip || 'unknown') + '\n' +
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
    var out = '\nbanned ' + result.nqip + '\n' +
      '  disconnected: ' + result.disconnected + ' client(s)\n' +
      '  server kicks: ' + result.server_kicks + '\n';
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

  // Open the OIDC login pop-up. If the user is already authenticated the
  // popup just flashes through the auto-close landing page and Nexus
  // pushes "rcon: authenticated." back via the trunk control channel —
  // same flow either way, no client-side state checks.
  function runClientLogin() {
    var loginURL = resolveRPCURL();
    var popup = window.open(loginURL, 'nq_rcon_login', 'width=500,height=600');
    if (!popup) {
      return 'rcon: pop-up blocked. open ' + loginURL + ' in a new tab,\n' +
             '      log in to continue.\n';
    }
    return '';
  }

  // Exported entry point — called from cmd_rcon.c via EM_ASYNC_JS.
  // connectedPort is the currently-connected server's listen port (0 if none).
  Module.nqRcon = async function (password, argsLine, connectedPort) {
    var tokens = tokenize(String(argsLine || ''));
    var plan = planCall(tokens, connectedPort | 0);
    if (plan.error) return plan.error;
    if (plan.clientCmd === 'login') return runClientLogin();

    var r = await postRPC(plan.method, plan.params, String(password || ''));
    if (r.needsLogin) return 'rcon: not authenticated. run: rcon login.\n';
    if (r.error) return r.error;
    return formatResult(plan.method, r.result);
  };
})();
