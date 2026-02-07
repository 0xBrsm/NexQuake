/*
 * cmd_rcon_token.js
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * This module is part of NexQuake and includes derivative work from
 * upstream websocket networking implementations by initialed85.
 * See ../ATTRIBUTIONS.md for upstream repositories, paths, and pinned commits.
 */

mergeInto(LibraryManager.library, {
  $NQRconToken: {
    defaultUrl: 'ws://localhost:1337/ws',

    getGlobal: function() {
      if (typeof globalThis !== 'undefined') return globalThis;
      if (typeof self !== 'undefined') return self;
      if (typeof window !== 'undefined') return window;
      return {};
    },

    resolveOverride: function(g) {
      try {
        var search = (g.location && g.location.search) ? g.location.search : '';
        var params = new URLSearchParams(search || '');
        var module = g.Module || {};
        return params.get('ws') || g.WEBSOCKET_URL || module.websocketUrl || module.WEBSOCKET_URL || '';
      } catch (e) {
        return '';
      }
    },

    appendRconToken: function(g, url) {
      try {
        if (!g.localStorage) return url;
        var tok = g.localStorage.getItem('nq_rcon_token');
        if (!tok) return url;
        var sep = (url.indexOf('?') === -1) ? '?' : '&';
        return url + sep + 'token=' + encodeURIComponent(tok);
      } catch (e) {
        return url;
      }
    },

    base64EncodeUtf8: function(input) {
      input = String(input || '');
      if (typeof Buffer !== 'undefined' && Buffer.from) {
        return Buffer.from(input, 'utf8').toString('base64');
      }
      if (typeof btoa !== 'undefined') {
        if (typeof TextEncoder !== 'undefined') {
          var bytes = new TextEncoder().encode(input);
          var bin = '';
          for (var i = 0; i < bytes.length; i++) {
            bin += String.fromCharCode(bytes[i]);
          }
          return btoa(bin);
        }
        return btoa(input);
      }
      return '';
    },
  },

  NQ_ConnectUrl__deps: ['$NQRconToken', '$stringToNewUTF8'],
  NQ_ConnectUrl: function() {
    try {
      var g = NQRconToken.getGlobal();
      var override = NQRconToken.resolveOverride(g);
      if (override) {
        return stringToNewUTF8(NQRconToken.appendRconToken(g, override));
      }

      var loc = g.location;
      if (!loc || !loc.host) {
        return stringToNewUTF8(NQRconToken.defaultUrl);
      }

      var wsProto = (loc.protocol === 'https:') ? 'wss:' : 'ws:';
      var wsUrl = wsProto + '//' + loc.host + '/ws';
      return stringToNewUTF8(NQRconToken.appendRconToken(g, wsUrl));
    } catch (e) {
      return stringToNewUTF8(NQRconToken.defaultUrl);
    }
  },

  RconToken_SetFromPassword__deps: ['$NQRconToken', '$UTF8ToString'],
  RconToken_SetFromPassword: function(password) {
    try {
      var pw = UTF8ToString(password);
      if (!pw) return;

      var g = NQRconToken.getGlobal();
      var tok = NQRconToken.base64EncodeUtf8(pw);
      if (!tok || !g.localStorage) return;
      g.localStorage.setItem('nq_rcon_token', tok);
    } catch (e) {
    }
  },
});
