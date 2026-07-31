// Package logging carries the caller's logger through the context so adapters
// can report non-fatal anomalies without writing to the process-global logger.
//
// A library has no business deciding where a program's logs go. Before this,
// stream parsers called slog.Warn directly, so a malformed chunk from a vendor
// showed up in the caller's log stream with no way to route, silence, or
// attribute it. The default here is a handler that discards everything: you get
// nothing unless you ask for it with llmkit.WithLogger.
package logging

import (
	"context"
	"log/slog"
)

// discardHandler drops every record and reports Enabled=false at every level.
//
// That second half is the whole reason this type exists instead of a
// slog.TextHandler pointed at io.Discard. A TextHandler built with nil options
// defaults to Info, so it answers Enabled(Warn) with true even though it throws
// the record away — which silently defeats every `if Enabled(...)` guard meant
// to skip expensive argument building. See Enabled below for the call site that
// was paying for it.
//
// Go 1.24 ships slog.DiscardHandler, which does exactly this. go.mod declares
// 1.22, so it is spelled out here; when the floor rises this can become
// slog.DiscardHandler unchanged in behaviour.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (discardHandler) WithAttrs([]slog.Attr) slog.Handler        { return discardHandler{} }
func (discardHandler) WithGroup(string) slog.Handler             { return discardHandler{} }

// discard drops every record. Built once — slog.New is cheap but not free, and
// this is consulted on paths that run per stream chunk.
var discard = slog.New(discardHandler{})

type loggerKey struct{}

// WithLogger returns a context carrying logger. A nil logger is ignored, so
// callers can pass through an unset field without a branch.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey{}, logger)
}

// From returns the context's logger, or a discarding one when none was set.
// It never returns nil, so callers can use the result unconditionally.
func From(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return discard
	}
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return discard
}

// Enabled reports whether anything would actually be recorded at level. Use it
// to skip building expensive log arguments — truncated payload previews, joined
// field lists — on the default silent path.
//
// The default path really is silent: with no caller logger installed this
// returns false at every level, so the guarded work is skipped rather than done
// and thrown away. provider.StreamDiagnostics.Malformed depends on that to
// avoid a TruncateForLog copy for every malformed frame in tolerant mode.
func Enabled(ctx context.Context, level slog.Level) bool {
	return From(ctx).Enabled(ctx, level)
}
