package compat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// The SSE reader consumes raw vendor bytes, which makes it the SDK's widest
// untrusted-input surface. These targets assert the two properties a caller
// depends on regardless of what a vendor sends: the reader always terminates,
// and it never panics.

const fuzzMaxRecv = 1000

// drainFuzz reads to termination, bounded so a pathological input that yields
// an endless run of skippable frames fails the test rather than hanging it.
func drainFuzz(t *testing.T, r *StreamReader) {
	t.Helper()
	for i := 0; ; i++ {
		if i > fuzzMaxRecv {
			t.Fatal("Recv did not terminate")
		}
		if _, err := r.Recv(); err != nil {
			return
		}
	}
}

func FuzzStreamReader_Strict(f *testing.F) {
	f.Add("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	f.Add("data: [DONE]\n\n")
	f.Add("data: {\n\n")
	f.Add("data:{\"choices\":[]}\n\n")
	f.Add(": keep-alive\n\n")
	f.Add("data: {\"error\":{\"message\":\"nope\",\"code\":429}}\n\n")
	f.Add("data: {\"usage\":{\"prompt_tokens\":1}}\n\n")
	f.Add("\n\n\n")
	f.Add("garbage without a data prefix")
	f.Add("data: null\n\n")
	f.Add("data: 123\n\n")
	f.Add("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0}]}}]}\n\n")

	f.Fuzz(func(t *testing.T, body string) {
		r := NewStreamReader(context.Background(), io.NopCloser(strings.NewReader(body)), "fuzz", true)
		defer r.Close()
		drainFuzz(t, r)
		// GetUsage is read after the stream ends; it must be safe whatever the
		// stream contained.
		_ = r.GetUsage()
	})
}

func FuzzStreamReader_Tolerant(f *testing.F) {
	f.Add("data: {bad}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n")
	f.Add("data: {bad}\n\n")
	f.Add("data: \n\n")

	f.Fuzz(func(t *testing.T, body string) {
		ctx := provider.WithStreamPolicy(context.Background(),
			provider.StreamPolicy{Tolerance: provider.StreamTolerateMalformed})
		r := NewStreamReader(ctx, io.NopCloser(strings.NewReader(body)), "fuzz", true)
		defer r.Close()
		drainFuzz(t, r)
	})
}

// Under tolerance, a malformed frame is skipped rather than surfaced — so a
// stream that terminates on anything other than EOF must have terminated for a
// reason the reader genuinely reports (an upstream error envelope, or the frame
// ceiling). It must never be a parse failure that leaked through.
func FuzzStreamReader_ToleranceSkipsParseErrors(f *testing.F) {
	f.Add("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: {trunc\n\ndata: [DONE]\n\n")
	f.Add("data: {trunc\n\n")

	f.Fuzz(func(t *testing.T, body string) {
		ctx := provider.WithStreamPolicy(context.Background(),
			provider.StreamPolicy{Tolerance: provider.StreamTolerateMalformed})
		r := NewStreamReader(ctx, io.NopCloser(strings.NewReader(body)), "fuzz", true)
		defer r.Close()

		for i := 0; ; i++ {
			if i > fuzzMaxRecv {
				t.Fatal("Recv did not terminate")
			}
			_, err := r.Recv()
			if err == nil {
				continue
			}
			if errors.Is(err, io.EOF) {
				return
			}
			// The only non-EOF terminations allowed under tolerance.
			var provErr *provider.ProviderError
			if errors.As(err, &provErr) {
				return
			}
			if strings.Contains(err.Error(), "frame exceeds") {
				return
			}
			t.Fatalf("tolerant stream failed with a parse error it should have skipped: %v", err)
		}
	})
}

// The frame ceiling has to hold for any input, including one long unterminated
// line — that is the case it exists to bound.
func FuzzStreamReader_FrameCeiling(f *testing.F) {
	f.Add("data: {\"a\":\"" + strings.Repeat("x", 100) + "\"}\n\n")
	f.Add(strings.Repeat("x", 5000))
	f.Add("")

	f.Fuzz(func(t *testing.T, body string) {
		ctx := provider.WithStreamPolicy(context.Background(),
			provider.StreamPolicy{MaxFrameBytes: 512})
		r := NewStreamReader(ctx, io.NopCloser(strings.NewReader(body)), "fuzz", true)
		defer r.Close()
		drainFuzz(t, r)
	})
}

func FuzzDetectStreamError(f *testing.F) {
	f.Add(`{"error":{"message":"x","code":429}}`)
	f.Add(`{"error":{}}`)
	f.Add(`{"error":{"code":"rate_limit_exceeded"}}`)
	f.Add(`{"error":{"retry_after":30}}`)
	f.Add(`{"error":{"retry_after":"30"}}`)
	f.Add(`{"error":{"code":99999}}`)
	f.Add(`{"error":{"code":-1}}`)
	f.Add(`{"choices":[],"error":{"message":"x"}}`)
	f.Add(`{}`)
	f.Add(`[]`)
	f.Add(`not json`)

	f.Fuzz(func(t *testing.T, data string) {
		err, ok := detectStreamError([]byte(data), "fuzz")
		if !ok {
			if err != nil {
				t.Errorf("detectStreamError(%q) returned false with a non-nil error", data)
			}
			return
		}
		if err == nil {
			t.Fatalf("detectStreamError(%q) returned true with a nil error", data)
		}
		// The status has to be usable as an HTTP status: it is fed to the
		// error-classification helpers, which switch on ranges.
		if err.StatusCode < 400 || err.StatusCode > 599 {
			t.Errorf("detectStreamError(%q) status = %d, want a 4xx/5xx", data, err.StatusCode)
		}
	})
}

func FuzzExtractRetryAfter(f *testing.F) {
	f.Add(`{"retry_after":30}`)
	f.Add(`{"retryAfter":"30"}`)
	f.Add(`{"retry_after":-1}`)
	f.Add(`{"retry_after":0}`)
	f.Add(`{"retry_after":1e308}`)
	f.Add(`{"retry_after":null}`)
	f.Add(`{}`)

	f.Fuzz(func(t *testing.T, raw string) {
		var obj map[string]any
		if err := unmarshalObject(raw, &obj); err != nil {
			t.Skip()
		}
		got := extractRetryAfter(obj)
		// The value goes into a Retry-After header, so it must never carry
		// header-breaking characters.
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("extractRetryAfter(%q) = %q, which would break the header", raw, got)
		}
	})
}

// unmarshalObject decodes raw into v, rejecting anything that isn't a JSON
// object — the fuzzer produces mostly non-objects and they aren't the input
// shape extractRetryAfter is ever handed.
func unmarshalObject(raw string, v *map[string]any) error {
	return json.Unmarshal([]byte(raw), v)
}
