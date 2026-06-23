/*
 * net_wt.c — WebTransport transport for net_wasm.c.
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * Fills out wasm_wt_transport. Non-blocking start(); send_raw() assumes
 * is_ready() per the contract in net_wasm.h. No sleeps here — all waits
 * live in WASM_SendPacket. Received datagrams get drained via the tick()
 * hook and handed to WASM_OnPacket.
 */

#include "quakedef.h"

#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include <emscripten/emscripten.h>

#include "net_wasm.h"

// The WebTransport URL comes from the /gamedir manifest, which advertises it
// only when the server offers WebTransport (EXTERNAL_URL set). It carries
// the same authority the page was served from, so the public CA cert always
// covers it.
EM_JS (char *, WT_ConnectUrl, (), {
	var cfg = (Module.nqTransportConfig && Module.nqTransportConfig.webtransport) || null;
	var url = cfg && cfg.url || "";
	return url ? stringToNewUTF8(String(url)) : 0;
});

EM_JS (int, WT_Supported, (), {
	var cfg = (Module.nqTransportConfig && Module.nqTransportConfig.webtransport) || null;
	return (cfg && cfg.url && typeof WebTransport === 'function') ? 1 : 0;
});

// Start a WT session (no-op if the shell already pre-started one).
EM_JS (int, WT_JsStart, (const char *url), {
	var urlStr = UTF8ToString(url);
	if (typeof WebTransport !== 'function') return -1;
	if (Module.nqWt && Module.nqWt.session) return 0;
	Module.nqWt = {
		session: null, writer: null,
		recvQueue: [], recvHead: 0, recvCap: 512,
		sendBuf: null, dropTooBig: 0,
		opened: false, everOpened: false, closed: false,
		closeCode: null, errDetail: ""
	};
	var state = Module.nqWt;
	// Format an error for diagnostics: a WebTransportError carries source
	// ("session" vs "stream") and an optional stream code; a plain value does
	// not. Falls back to the message or the value's string form.
	state.fmtErr = function (e) {
		if (e && e.source)
			return "[" + e.source + "] " + (e.message || e.name || "error") +
				(e.streamErrorCode != null ? " code=" + e.streamErrorCode : "");
		return String((e && e.message) || e);
	};
	try {
		// The cert chains to a public CA (ACME), so the browser validates it
		// normally — no serverCertificateHashes pinning.
		state.session = new WebTransport(urlStr);
	} catch (e) {
		state.errDetail = state.fmtErr(e);
		state.closed = true;
		return -1;
	}
	state.session.ready.then(function() {
		state.writer = state.session.datagrams.writable.getWriter();
		state.opened = true;
		state.everOpened = true;
		(async function() {
			var reader = state.session.datagrams.readable.getReader();
			try {
				for (;;) {
					var r = await reader.read();
					if (r.done) break;
					// Drop oldest on overflow so a stalled C tick can't OOM.
					if (state.recvQueue.length - state.recvHead >= state.recvCap)
						state.recvQueue[state.recvHead++] = null;
					state.recvQueue.push(new Uint8Array(r.value));
				}
			} catch (e) { state.errDetail = state.fmtErr(e); }
			state.opened = false;
			state.closed = true;
		})();
	}).catch(function(e) {
		state.errDetail = state.fmtErr(e);
		state.closed = true;
	});
	// .then(resolve, reject) rather than .finally(): finally() would re-throw a
	// connect-time rejection as an uncaught promise error on the browser
	// console. Record the close code / reason; the human-readable report is
	// composed at read time in WT_JsLastErrorDup from this structured state.
	state.session.closed.then(function (info) {
		state.opened = false;
		state.closed = true;
		if (info && typeof info.closeCode === "number") state.closeCode = info.closeCode;
		if (!state.errDetail && info && info.reason) state.errDetail = info.reason;
	}, function (e) {
		state.opened = false;
		state.closed = true;
		if (!state.errDetail) state.errDetail = state.fmtErr(e);
	});
	return 0;
});

EM_JS (int, WT_JsReady,  (), { return (Module.nqWt && Module.nqWt.opened) ? 1 : 0; });
EM_JS (int, WT_JsClosed, (), { return (!Module.nqWt || Module.nqWt.closed) ? 1 : 0; });

EM_JS (int, WT_JsSend, (int ptr, int len), {
	var state = Module.nqWt;
	if (!state || !state.writer) return -1;
	// An oversized datagram is UDP-equivalent loss, not a dead transport: the
	// QUIC limit floats with the path MTU and can dip below a max-size game
	// frame mid-session. Drop it like the network would — the spec rejects an
	// oversized write() with TypeError, and treating that as a session error
	// would turn one fat packet into a full disconnect.
	function dropTooBig(max) {
		state.dropTooBig = (state.dropTooBig | 0) + 1;
		if (state.dropTooBig === 1 && console.warn)
			console.warn('WebTransport: dropped ' + len + 'B datagram (max ' + max + 'B)');
	}
	try {
		var dgrams = state.session && state.session.datagrams;
		var max = dgrams && dgrams.maxDatagramSize;
		if (typeof max === 'number' && max > 0 && len > max) {
			dropTooBig(max);
			return len;
		}
		// Reusable scratch avoids a fresh allocation per datagram. The
		// browser's WT sink copies the chunk synchronously during write(),
		// so the buffer is safe to overwrite on the next call.
		if (!state.sendBuf || state.sendBuf.length < len) {
			var cap = 1024;
			while (cap < len) cap <<= 1;
			state.sendBuf = new Uint8Array(cap);
		}
		state.sendBuf.set(HEAPU8.subarray(ptr, ptr + len));
		state.writer.write(state.sendBuf.subarray(0, len)).catch(function(e) {
			if (e instanceof TypeError) { dropTooBig('?'); return; }
			state.errDetail = state.fmtErr(e);
			// Async write rejection: stream errored — close the session now so
			// the server tears down its half of the QUIC connection promptly
			// (rather than holding a ghost route until the idle timeout), then
			// mark closed so WT_Tick reports the unexpected close next tick.
			if (state.session) { try { state.session.close(); } catch (_) {} }
			state.opened = false;
			state.closed = true;
		});
		return len;
	} catch (e) {
		// Synchronous throw: stream already errored/closed before we got here.
		state.errDetail = state.fmtErr(e);
		if (state.session) { try { state.session.close(); } catch (_) {} }
		state.opened = false;
		state.closed = true;
		return -1;
	}
});

// Returns length on success, 0 when empty, -1 when oversized (dropped).
// Uses a head index instead of Array.shift() so per-tick drain is O(1)
// per packet rather than O(n).
EM_JS (int, WT_JsPoll, (int ptr, int max_len), {
	var state = Module.nqWt;
	if (!state || state.recvHead >= state.recvQueue.length) return 0;
	var msg = state.recvQueue[state.recvHead];
	state.recvQueue[state.recvHead++] = null;
	if (state.recvHead === state.recvQueue.length) {
		state.recvQueue.length = 0;
		state.recvHead = 0;
	}
	if (msg.length > max_len) return -1;
	HEAPU8.set(msg, ptr);
	return msg.length;
});

EM_JS (void, WT_JsClose, (), {
	var state = Module.nqWt;
	if (!state) return;
	if (state.session) { try { state.session.close(); } catch (e) {} }
	state.opened = false;
	state.closed = true;
	state.session = null;
	state.writer = null;
	state.recvQueue = [];
	state.recvHead = 0;
	state.sendBuf = null;
});

EM_JS (char *, WT_JsLastErrorDup, (), {
	var s = Module.nqWt;
	if (!s) return stringToNewUTF8("");
	// Compose at read time from structured state so the report never depends on
	// which lifecycle event fired first. Lead with whether the handshake ever
	// completed — the key signal for "WebTransport unreachable" vs "reached the
	// server, then the session ended".
	var parts = [];
	if (s.closed)
		parts.push(s.everOpened ? "session closed" : "closed before handshake completed");
	if (typeof s.closeCode === "number") parts.push("code " + s.closeCode);
	if (s.errDetail) parts.push(s.errDetail);
	return stringToNewUTF8(parts.join("; "));
});

//----------------------------------------------------------------------------
// wasm_transport_t.

static const char  wt_name[] = "WebTransport";
static qboolean wt_started = false;
static qboolean wt_was_ready = false;
static qboolean wt_was_closed = false;
static const char *wt_last_error = "";
// Warm-failure dedupe: the last background-session failure reason already
// reported. Cleared on a successful connect so a recurrence after a healthy
// stretch is reported again rather than swallowed forever.
static char wt_warm_err_reported[128];

// Availability is fixed for the page lifetime (manifest config + constructor
// presence, both set before the engine boots) — cache it so the per-poll
// availability checks don't cross into JS every call.
static qboolean WT_IsAvailable (void)
{
	static int cached = -1;
	if (cached < 0) cached = WT_Supported ();
	return cached ? true : false;
}
static qboolean WT_IsReady (void)     { return wt_started && WT_JsReady (); }
static qboolean WT_IsClosed (void)    { return wt_started && WT_JsClosed (); }

static int WT_Start (void)
{
	char *url;

	if (wt_started) return 0;
	if (!WT_IsAvailable ()) return -1;

	url = WT_ConnectUrl ();
	if (!url || !url[0])
	{
		WASM_Log (WASM_LOG_ERROR, "%s: missing URL", wt_name);
		wt_last_error = "missing URL";
		free (url);
		return -1;
	}

	if (WT_JsStart (url) < 0)
	{
		char *err = WT_JsLastErrorDup ();
		WASM_Log (WASM_LOG_ERROR, "%s: start failed: %s", wt_name, err ? err : "");
		free (err); free (url);
		wt_last_error = "start failed";
		return -1;
	}

	wt_started = true;
	wt_was_ready = false;
	wt_was_closed = false;
	wt_last_error = "";
	free (url);
	return 0;
}

static int WT_SendRaw (const byte *frame, int len)
{
	if (WT_JsSend ((int)(uintptr_t)frame, len) < 0)
	{
		wt_last_error = "browser send failed";
		return -1;
	}
	wt_last_error = "";
	return len;
}

static void WT_Close (void)
{
	qboolean died;

	if (!wt_started) return;
	died = WT_JsClosed () ? true : false;
	wt_started = false;
	wt_last_error = "";
	WT_JsClose ();
	// Claim the close transition so tick won't double-fire — but only report
	// it if tick hasn't already: closing an already-dead transport during
	// fall-forward cleanup is teardown of the same disconnect, not a new one.
	if (wt_was_closed) return;
	wt_was_closed = true;

	if (!died)
	{
		WASM_OnClose (wt_name, true);
		return;
	}

	// The session died on its own before tick could report it — this is the
	// warm (background, never-adopted) session being reaped for a retry. It
	// was never the connection, so no connection-level close is announced;
	// surface the failure reason to the browser console for diagnostics,
	// and only when it changes — an endpoint that stays unreachable would
	// otherwise repeat the same line every retry cycle.
	{
		char *err = WT_JsLastErrorDup ();
		const char *reason = (err && err[0]) ? err : "no error detail";

		if (Q_strcmp (wt_warm_err_reported, (char *)reason) != 0)
		{
			snprintf (wt_warm_err_reported, sizeof(wt_warm_err_reported), "%s", reason);
			WASM_OnError (wt_name, reason);
		}
		free (err);
	}
}

static void WT_Tick (void)
{
	static byte scratch[WASM_MAX_FRAME_SIZE];
	qboolean is_ready, is_closed;

	for (;;)
	{
		int length = WT_JsPoll ((int)(uintptr_t)scratch, (int)sizeof(scratch));
		if (length == 0) break;
		if (length < 0) continue; // oversized; keep draining
		WASM_OnPacket (scratch, length);
	}

	// Edge-detect lifecycle transitions from the JS state machine and
	// mirror them through the shared hooks, so WT logs/behaves like WS.
	is_ready  = WT_JsReady ()  ? true : false;
	is_closed = WT_JsClosed () ? true : false;

	if (is_ready && !wt_was_ready)
	{
		wt_warm_err_reported[0] = 0; // healthy again — report future failures afresh
		WASM_OnOpen (wt_name);
	}
	wt_was_ready = is_ready;

	if (is_closed && !wt_was_closed)
	{
		char *err = WT_JsLastErrorDup ();
		if (err && err[0]) WASM_OnError (wt_name, err);
		free (err);
		WASM_OnClose (wt_name, false);
		wt_was_closed = true;
	}
}

static const char *WT_LastError (void)
{
	static char buf[128];
	char *err;
	// Prefer the JS-side detail (the actual stream/session error message);
	// the coarse C-side string is only a fallback for pre-session failures.
	err = WT_JsLastErrorDup ();
	if (err && err[0])
	{
		snprintf (buf, sizeof(buf), "%s", err);
		free (err);
		return buf;
	}
	free (err);
	return wt_last_error[0] ? wt_last_error : "unknown error";
}

const wasm_transport_t wasm_wt_transport = {
	WT_IsAvailable, WT_IsReady, WT_IsClosed,
	WT_Start, WT_SendRaw, WT_Close,
	WT_Tick,
	WT_LastError, wt_name
};
