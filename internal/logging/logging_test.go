package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// newCapturing builds a logger writing into a buffer the test can read back, so
// assertions can check that a record actually reached the caller's handler
// rather than only that a pointer matched.
func newCapturing(buf *bytes.Buffer, level slog.Leveler) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level}))
}

// The whole point of From is that adapters never branch on nil. An unset
// context must still yield a usable logger.
func TestFrom_UnsetContextYieldsDiscard(t *testing.T) {
	got := From(context.Background())
	if got == nil {
		t.Fatal("From must never return nil")
	}
	if got != discard {
		t.Error("an unset context should yield the shared discard logger")
	}
}

// A zero-value context.Context reaches this package whenever an adapter is
// called from code that forgot to thread ctx. Logging must not be the thing
// that panics there.
func TestFrom_NilContext(t *testing.T) {
	var nilCtx context.Context
	if got := From(nilCtx); got != discard {
		t.Errorf("nil context should yield the discard logger, got %v", got)
	}
}

// discard is documented as "Built once" because From runs on per-stream-chunk
// paths. If it ever becomes a per-call slog.New, this fails.
func TestFrom_DiscardIsSharedNotRebuilt(t *testing.T) {
	if From(context.Background()) != From(context.TODO()) {
		t.Error("the discard logger should be a shared singleton, not constructed per call")
	}
}

func TestWithLogger_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := newCapturing(&buf, slog.LevelInfo)

	ctx := WithLogger(context.Background(), want)
	if got := From(ctx); got != want {
		t.Fatal("From did not return the logger installed by WithLogger")
	}

	From(ctx).Warn("hello", "k", "v")
	if out := buf.String(); !strings.Contains(out, "hello") || !strings.Contains(out, "k=v") {
		t.Errorf("record did not reach the caller's handler: %q", out)
	}
}

// A nil logger must be ignored rather than stored: storing it would make From's
// type assertion succeed with a nil *slog.Logger and hand back something that
// panics on first use. This is why WithLogger guards instead of the callers.
func TestWithLogger_NilIsIgnored(t *testing.T) {
	base := context.Background()
	if got := WithLogger(base, nil); got != base {
		t.Error("WithLogger(ctx, nil) should return ctx unchanged")
	}

	var buf bytes.Buffer
	set := newCapturing(&buf, slog.LevelInfo)
	ctx := WithLogger(base, set)
	if got := From(WithLogger(ctx, nil)); got != set {
		t.Error("a nil logger must not clear an already-set one")
	}
}

func TestWithLogger_InnermostWins(t *testing.T) {
	var outerBuf, innerBuf bytes.Buffer
	outer := newCapturing(&outerBuf, slog.LevelInfo)
	inner := newCapturing(&innerBuf, slog.LevelInfo)

	ctx := WithLogger(WithLogger(context.Background(), outer), inner)
	From(ctx).Info("scoped")

	if innerBuf.Len() == 0 {
		t.Error("the innermost logger should receive the record")
	}
	if outerBuf.Len() != 0 {
		t.Errorf("the shadowed outer logger should receive nothing, got %q", outerBuf.String())
	}
}

// Enabled must defer to the caller's own level so a caller running at Error
// does not pay for Warn-level argument construction.
func TestEnabled_RespectsCallerLevel(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), newCapturing(&buf, slog.LevelError))

	if Enabled(ctx, slog.LevelWarn) {
		t.Error("Warn should be disabled when the caller's handler is set to Error")
	}
	if !Enabled(ctx, slog.LevelError) {
		t.Error("Error should be enabled when the caller's handler is set to Error")
	}
}

// The default path must be silent at every level, not just below Info.
//
// This used to fail. discard was slog.NewTextHandler(io.Discard, nil), and a
// nil HandlerOptions defaults the level to Info — so Enabled answered true for
// Info, Warn and Error while throwing every record away. Any caller following
// Enabled's advice still built the expensive arguments; they were formatted and
// discarded.
//
// That was not hypothetical. provider.StreamDiagnostics.Malformed guards its
// warn with exactly this check, on a logger obtained from From:
//
//	// Building the preview costs a copy, so skip it when nothing is listening —
//	// which is the default.
//	if d.logger.Enabled(context.Background(), slog.LevelWarn) {
//	        d.logger.Warn(..., "payload_preview", TruncateForLog(payload, 200))
//	}
//
// With no caller logger installed d.logger is discard, so the guard was always
// true and TruncateForLog ran for every malformed frame in tolerant mode — the
// precise copy the comment claimed to avoid. Hence discardHandler.
func TestEnabled_DiscardPathIsSilentAtEveryLevel(t *testing.T) {
	ctx := context.Background()

	for _, lv := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		if Enabled(ctx, lv) {
			t.Errorf("Enabled(%v) on the discard path = true; guards that skip "+
				"expensive argument building are defeated whenever this is true", lv)
		}
	}
	// Above Error too — a caller logging at a custom high level gets the same
	// answer, since the handler ignores the level entirely.
	if Enabled(ctx, slog.LevelError+4) {
		t.Error("a level above Error should also be disabled on the discard path")
	}
}

// discardHandler has to satisfy slog.Handler honestly, not just compile.
//
// Handle is unreachable through the logger — Enabled short-circuits every call
// before slog gets there — but slog.Handler implementations are allowed to be
// invoked directly, and WithAttrs/WithGroup returning nil would panic the next
// caller. Asserting the contract here is cheaper than discovering it from a
// stack trace.
func TestDiscardHandler_SatisfiesContract(t *testing.T) {
	var h slog.Handler = discardHandler{}

	if err := h.Handle(context.Background(), slog.Record{}); err != nil {
		t.Errorf("Handle returned %v, want nil", err)
	}
	if got := h.WithAttrs([]slog.Attr{slog.String("k", "v")}); got == nil {
		t.Error("WithAttrs returned nil, which would panic the next call")
	}
	if got := h.WithGroup("g"); got == nil {
		t.Error("WithGroup returned nil, which would panic the next call")
	}
	// Derived handlers must stay silent too, or the guard leaks through
	// logger.With(...).
	if h.WithAttrs(nil).Enabled(context.Background(), slog.LevelError) {
		t.Error("a handler derived via WithAttrs must still report disabled")
	}
	if h.WithGroup("g").Enabled(context.Background(), slog.LevelError) {
		t.Error("a handler derived via WithGroup must still report disabled")
	}
}

// Silence must not come at the cost of usability: From still returns a logger
// that can be called without a nil check, it just does nothing.
func TestDiscardLoggerStillUsable(t *testing.T) {
	l := From(context.Background())
	l.Debug("d")
	l.Info("i")
	l.Warn("w", "k", "v")
	l.Error("e")
	l.With("a", 1).WithGroup("g").Warn("nested")
}

// Enabled forwards ctx to slog, which must tolerate the same zero-value context
// From already handles.
func TestEnabled_NilContext(t *testing.T) {
	var nilCtx context.Context
	if Enabled(nilCtx, slog.LevelDebug) {
		t.Error("Debug should be disabled on the discard path reached via a nil context")
	}
}
