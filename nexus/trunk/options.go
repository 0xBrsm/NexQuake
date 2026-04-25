package trunk

// logf is a printf-style logging function. Callers plumb their existing
// logger in via [WithLogger] by passing its printf-style method (e.g.
// log.Printf, zap's Sugared Infof, etc.). nil loggers are treated as no-ops.
type logf = func(format string, args ...any)

// Option configures a [Conn] at construction time. See [NewConn].
type Option func(*connOptions)

// connOptions holds the resolved constructor inputs. Populated by Option
// applicators in NewConn before the Conn is allocated.
type connOptions struct {
	dispatch FrameDispatch
	warnf    logf
	debugf   logf
}

// WithDispatch wires application-level callbacks for control-channel frames,
// connection close, and per-frame port gating.
func WithDispatch(d FrameDispatch) Option { return func(o *connOptions) { o.dispatch = d } }

// WithLogger plumbs printf-style loggers for warnings (recoverable issues
// worth surfacing) and debug output (chatty, protocol-tracing level). Passing
// nil for either yields a no-op.
func WithLogger(warnf, debugf logf) Option {
	return func(o *connOptions) { o.warnf, o.debugf = warnf, debugf }
}
