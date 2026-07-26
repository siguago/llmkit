package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/siguago/llmkit/internal/logging"
)

// StreamTolerance decides what a stream reader does with a frame it cannot
// parse.
type StreamTolerance int

const (
	// StreamStrict fails the stream on the first unparseable frame. This is the
	// default, because skipping a frame is silent data loss: the caller gets a
	// truncated answer, or one missing a tool call, and no indication that it
	// happened. An error is recoverable — the caller can retry or fall back —
	// while a quietly incomplete response is not even detectable.
	StreamStrict StreamTolerance = iota

	// StreamTolerateMalformed skips frames that fail to parse and reports each
	// one to the configured logger. Use it when partial output beats no output,
	// or to work around a vendor that intermittently emits garbage frames.
	StreamTolerateMalformed
)

// DefaultMaxFrameBytes bounds a single SSE frame. Frames are normally a few
// hundred bytes; the ceiling exists so a vendor sending an unterminated line
// cannot make the reader buffer without limit. It has to leave room for the one
// legitimately large case — a tool call whose arguments arrive in one frame —
// which is why it is a megabyte and not a kilobyte.
const DefaultMaxFrameBytes = 1 << 20

// StreamPolicy tunes stream frame handling. The zero value is the default
// policy: strict, with DefaultMaxFrameBytes.
type StreamPolicy struct {
	Tolerance StreamTolerance
	// MaxFrameBytes caps one SSE frame. Zero means DefaultMaxFrameBytes.
	MaxFrameBytes int
}

func (p StreamPolicy) maxFrameBytes() int {
	if p.MaxFrameBytes <= 0 {
		return DefaultMaxFrameBytes
	}
	return p.MaxFrameBytes
}

type streamPolicyKey struct{}

// WithStreamPolicy returns a context carrying p. Adapters read it when they
// build a stream reader, so the setting reaches every provider without each
// constructor having to accept it.
func WithStreamPolicy(ctx context.Context, p StreamPolicy) context.Context {
	return context.WithValue(ctx, streamPolicyKey{}, p)
}

// StreamPolicyFrom returns the context's policy, or the default when none was
// set.
func StreamPolicyFrom(ctx context.Context) StreamPolicy {
	if ctx == nil {
		return StreamPolicy{}
	}
	p, _ := ctx.Value(streamPolicyKey{}).(StreamPolicy)
	return p
}

// StreamDiagnostics is the shared malformed-frame handling every adapter's
// stream reader uses, so the strict/tolerant decision is made once rather than
// re-implemented per vendor.
type StreamDiagnostics struct {
	policy   StreamPolicy
	logger   *slog.Logger
	provider string
}

// NewStreamDiagnostics captures the policy and logger in effect for this stream.
// Call it where the stream is opened, while the request context is in scope —
// a StreamReader outlives the call that created it and has no context of its own.
func NewStreamDiagnostics(ctx context.Context, providerName string) StreamDiagnostics {
	return StreamDiagnostics{
		policy:   StreamPolicyFrom(ctx),
		logger:   logging.From(ctx),
		provider: providerName,
	}
}

// scannerStartBytes is the initial frame buffer. Frames are usually far smaller;
// starting here avoids regrowing on the common case without reserving the whole
// ceiling up front for every stream.
const scannerStartBytes = 64 * 1024

// NewScanner builds the frame scanner for this stream, honoring the policy's
// frame ceiling.
func (d StreamDiagnostics) NewScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	max := d.policy.maxFrameBytes()
	// bufio caps tokens at the LARGER of max and cap(buf), so an initial buffer
	// bigger than the ceiling would silently raise it — a caller asking for a
	// 1 KiB limit would get 64 KiB.
	start := min(scannerStartBytes, max)
	s.Buffer(make([]byte, 0, start), max)
	return s
}

// FrameKind classifies an SSE data payload before anyone tries to parse it.
type FrameKind int

const (
	// FrameJSON looks like a JSON object and should be unmarshalled. It is the
	// only kind that can be malformed.
	FrameJSON FrameKind = iota
	// FrameDone is the [DONE] sentinel: the stream is over.
	FrameDone
	// FrameSkip is a payload that was never meant to be parsed — an empty data
	// line, a keep-alive, a non-JSON scalar. Skip it and read on.
	FrameSkip
)

// ClassifyFrame decides what to do with an SSE data payload.
//
// Every adapter must run this before unmarshalling, so that "malformed" means
// what it says: a frame that claims to be JSON and isn't. Without it a relay
// appending OpenAI's [DONE] sentinel, or an empty `data:` line — both legal and
// both common — would be reported as vendor corruption and fail the stream
// under the strict policy.
//
// This lives here rather than in each reader because it was open-coded in four
// of them and two had drifted, which is exactly the bug it now prevents.
func ClassifyFrame(data string) FrameKind {
	switch {
	case data == "[DONE]":
		return FrameDone
	case strings.HasPrefix(data, "{"):
		return FrameJSON
	default:
		return FrameSkip
	}
}

// Malformed reports a frame that could not be parsed.
//
// It returns a non-nil error when the policy says to fail the stream, and nil
// when the caller should skip the frame and keep reading. Call it as:
//
//	if err := d.Malformed("chunk", err, data); err != nil {
//		return nil, err
//	}
//	continue
//
// what names the thing that failed to parse ("chunk", "message_start"), and
// payload is the raw frame — it is truncated before it reaches the log, and is
// never included in the returned error, which callers may forward to their own
// users.
func (d StreamDiagnostics) Malformed(what string, cause error, payload string) error {
	if d.policy.Tolerance == StreamStrict {
		return fmt.Errorf("%s stream: malformed %s: %w", d.provider, what, cause)
	}
	// Building the preview costs a copy, so skip it when nothing is listening —
	// which is the default.
	if d.logger.Enabled(context.Background(), slog.LevelWarn) {
		d.logger.Warn("llmkit: skipped malformed stream frame",
			"provider", d.provider,
			"what", what,
			"error", cause,
			"payload_preview", TruncateForLog(payload, 200),
		)
	}
	return nil
}

// ScanError converts a scanner failure into a stream error, naming the frame
// ceiling explicitly when that is what was hit — "token too long" on its own
// sends people looking in the wrong place.
func (d StreamDiagnostics) ScanError(err error) error {
	if err == nil {
		return nil
	}
	if err == bufio.ErrTooLong {
		// Name the option a caller can actually reach for. This package's
		// WithStreamPolicy is the adapter-facing knob; someone hitting this is
		// almost certainly using the llmkit façade.
		return fmt.Errorf("%s stream: frame exceeds %d bytes; raise it with llmkit.WithMaxStreamFrameBytes: %w",
			d.provider, d.policy.maxFrameBytes(), err)
	}
	return err
}

// TruncateForLog shortens s to at most n bytes, marking that it was cut. It
// slices on a byte boundary, which can split a multi-byte rune — acceptable for
// a diagnostic preview, and the marker makes clear the value is partial.
//
// A non-positive n yields just the marker: this runs on a diagnostic path, so a
// nonsensical bound must not take down the caller's stream.
func TruncateForLog(s string, n int) string {
	if n <= 0 {
		if s == "" {
			return ""
		}
		return truncationMarker
	}
	if len(s) <= n {
		return s
	}
	return s[:n] + truncationMarker
}

const truncationMarker = "…(truncated)"
