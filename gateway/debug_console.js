(() => {
  try {
    if (window.__webquakeConsoleMirrored) return;
    window.__webquakeConsoleMirrored = true;

    const endpoint = "/debug/console";

    function safeStringify(value) {
      try {
        if (typeof value === "string") return value;
        return JSON.stringify(value);
      } catch {
        try {
          return String(value);
        } catch {
          return "[unprintable]";
        }
      }
    }

    function send(payload) {
      payload = payload || {};
      payload.ts = Date.now();
      payload.href = String(location.href);
      payload.ua = navigator.userAgent;

      const body = JSON.stringify(payload);

      try {
        if (navigator.sendBeacon) {
          const blob = new Blob([body], { type: "application/json" });
          navigator.sendBeacon(endpoint, blob);
          return;
        }
      } catch {
        // ignore beacon errors, fall back to fetch
      }

      try {
        fetch(endpoint, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body,
          keepalive: true,
          credentials: "same-origin",
        }).catch(() => {});
      } catch {
        // ignore fetch errors
      }
    }

    function wrapConsole(level) {
      const orig = console[level];
      console[level] = function (...args) {
        try {
          send({ level, args: args.map(safeStringify) });
        } catch {
          // ignore errors in the mirror
        }
        try {
          return orig.apply(console, args);
        } catch {
          // ignore console failures (should be rare)
        }
      };
    }

    ["log", "info", "warn", "error"].forEach(wrapConsole);

    window.addEventListener("error", (ev) => {
      try {
        send({
          level: "window.error",
          message: String(ev.message || ""),
          filename: String(ev.filename || ""),
          lineno: ev.lineno || 0,
          colno: ev.colno || 0,
          stack: ev.error && ev.error.stack ? String(ev.error.stack) : "",
        });
      } catch {
        // ignore
      }
    });

    window.addEventListener("unhandledrejection", (ev) => {
      try {
        const reason = ev.reason;
        send({
          level: "unhandledrejection",
          reason: reason && reason.stack ? String(reason.stack) : safeStringify(reason),
        });
      } catch {
        // ignore
      }
    });
  } catch {
    // ignore
  }
})();

