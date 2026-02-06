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

    canonicalHost: function(host) {
      return (host || '').toString().trim().toLowerCase();
    },

    authorityFromUrl: function(g, url) {
      try {
        var base = (g.location && g.location.href) ? g.location.href : undefined;
        return NQRconToken.canonicalHost((new URL(url, base)).host);
      } catch (e) {
        return '';
      }
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

    resolveAuthority: function(g) {
      var authority = '';
      var override = NQRconToken.resolveOverride(g);
      if (override) authority = NQRconToken.authorityFromUrl(g, override);
      if (!authority) {
        authority = NQRconToken.canonicalHost(g.location && g.location.host ? g.location.host : '');
      }
      if (!authority) authority = 'unknown-host';
      return authority;
    },

    appendRconToken: function(g, url) {
      try {
        var authority = NQRconToken.authorityFromUrl(g, url);
        if (!authority || !g.localStorage) return url;
        var tok = g.localStorage.getItem('nq_rcon_token:' + authority);
        if (!tok) return url;
        var sep = (url.indexOf('?') === -1) ? '?' : '&';
        return url + sep + 'token=' + encodeURIComponent(tok);
      } catch (e) {
        return url;
      }
    },

    utf8Encode: function(input) {
      if (typeof TextEncoder !== 'undefined') return new TextEncoder().encode(input);

      var out = [];
      for (var i = 0; i < input.length; i++) {
        var c = input.charCodeAt(i);
        if (c < 0x80) {
          out.push(c);
        } else if (c < 0x800) {
          out.push(0xc0 | (c >> 6), 0x80 | (c & 0x3f));
        } else if (c >= 0xd800 && c <= 0xdbff && i + 1 < input.length) {
          var c2 = input.charCodeAt(++i);
          var u = 0x10000 + ((c - 0xd800) << 10) + (c2 - 0xdc00);
          out.push(
            0xf0 | (u >> 18),
            0x80 | ((u >> 12) & 0x3f),
            0x80 | ((u >> 6) & 0x3f),
            0x80 | (u & 0x3f)
          );
        } else {
          out.push(
            0xe0 | (c >> 12),
            0x80 | ((c >> 6) & 0x3f),
            0x80 | (c & 0x3f)
          );
        }
      }
      return new Uint8Array(out);
    },

    sha256: function(data) {
      function rotr(x, n) {
        return (x >>> n) | (x << (32 - n));
      }

      var K = [
        0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
        0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
        0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
        0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
        0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
        0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
        0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
        0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
      ];

      var H0 = 0x6a09e667 | 0;
      var H1 = 0xbb67ae85 | 0;
      var H2 = 0x3c6ef372 | 0;
      var H3 = 0xa54ff53a | 0;
      var H4 = 0x510e527f | 0;
      var H5 = 0x9b05688c | 0;
      var H6 = 0x1f83d9ab | 0;
      var H7 = 0x5be0cd19 | 0;

      var l = data.length;
      var withOne = l + 1;
      var rem = withOne % 64;
      var pad = rem <= 56 ? (56 - rem) : (56 + 64 - rem);
      var total = withOne + pad + 8;
      var buf = new Uint8Array(total);
      buf.set(data, 0);
      buf[l] = 0x80;

      var bitLenHi = Math.floor(l / 536870912);
      var bitLenLo = (l << 3) >>> 0;
      var o = total - 8;
      buf[o + 0] = (bitLenHi >>> 24) & 0xff;
      buf[o + 1] = (bitLenHi >>> 16) & 0xff;
      buf[o + 2] = (bitLenHi >>> 8) & 0xff;
      buf[o + 3] = bitLenHi & 0xff;
      buf[o + 4] = (bitLenLo >>> 24) & 0xff;
      buf[o + 5] = (bitLenLo >>> 16) & 0xff;
      buf[o + 6] = (bitLenLo >>> 8) & 0xff;
      buf[o + 7] = bitLenLo & 0xff;

      var w = new Int32Array(64);
      for (var i = 0; i < total; i += 64) {
        var t = 0;
        for (t = 0; t < 16; t++) {
          var p = i + (t * 4);
          w[t] = ((buf[p] << 24) | (buf[p + 1] << 16) | (buf[p + 2] << 8) | buf[p + 3]) | 0;
        }
        for (t = 16; t < 64; t++) {
          var x = w[t - 15];
          var y = w[t - 2];
          var s0 = (rotr(x, 7) ^ rotr(x, 18) ^ (x >>> 3)) | 0;
          var s1 = (rotr(y, 17) ^ rotr(y, 19) ^ (y >>> 10)) | 0;
          w[t] = (w[t - 16] + s0 + w[t - 7] + s1) | 0;
        }

        var a = H0;
        var b = H1;
        var c = H2;
        var d = H3;
        var e = H4;
        var f = H5;
        var g = H6;
        var h = H7;
        for (t = 0; t < 64; t++) {
          var S1 = (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) | 0;
          var ch = ((e & f) ^ (~e & g)) | 0;
          var t1 = (h + S1 + ch + K[t] + w[t]) | 0;
          var S0 = (rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) | 0;
          var maj = ((a & b) ^ (a & c) ^ (b & c)) | 0;
          var t2 = (S0 + maj) | 0;
          h = g;
          g = f;
          f = e;
          e = (d + t1) | 0;
          d = c;
          c = b;
          b = a;
          a = (t1 + t2) | 0;
        }

        H0 = (H0 + a) | 0;
        H1 = (H1 + b) | 0;
        H2 = (H2 + c) | 0;
        H3 = (H3 + d) | 0;
        H4 = (H4 + e) | 0;
        H5 = (H5 + f) | 0;
        H6 = (H6 + g) | 0;
        H7 = (H7 + h) | 0;
      }

      var hash = new Uint8Array(32);
      var hs = [H0, H1, H2, H3, H4, H5, H6, H7];
      for (var j = 0; j < 8; j++) {
        var v = hs[j] >>> 0;
        hash[j * 4 + 0] = (v >>> 24) & 0xff;
        hash[j * 4 + 1] = (v >>> 16) & 0xff;
        hash[j * 4 + 2] = (v >>> 8) & 0xff;
        hash[j * 4 + 3] = v & 0xff;
      }
      return hash;
    },

    base64UrlNoPad: function(bytes) {
      var b64 = '';
      if (typeof Buffer !== 'undefined' && Buffer.from) {
        b64 = Buffer.from(bytes).toString('base64');
      } else if (typeof btoa !== 'undefined') {
        var bin = '';
        for (var j = 0; j < bytes.length; j++) {
          bin += String.fromCharCode(bytes[j]);
        }
        b64 = btoa(bin);
      } else {
        return '';
      }

      var tok = b64.split('+').join('-').split('/').join('_');
      while (tok.length > 0 && tok.charCodeAt(tok.length - 1) === 61) {
        tok = tok.slice(0, -1);
      }
      return tok;
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
      var authority = NQRconToken.resolveAuthority(g);
      var input = 'NQ:rcon:v2|' + authority + '|' + pw;
      var data = NQRconToken.utf8Encode(input);
      var hash = NQRconToken.sha256(data);
      var tok = NQRconToken.base64UrlNoPad(hash);
      if (!tok || !g.localStorage) return;
      g.localStorage.setItem('nq_rcon_token:' + authority, tok);
    } catch (e) {
    }
  },
});
