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
	"io"
	"log/slog"
)

// discard drops every record. Built once — slog.New is cheap but not free, and
// this is consulted on paths that run per stream chunk.
var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

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
func Enabled(ctx context.Context, level slog.Level) bool {
	return From(ctx).Enabled(ctx, level)
}
