package provider_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// These targets all take attacker-shaped input: bytes that arrived over the
// wire from a vendor. Nothing here asserts a particular parse result — the
// contract being fuzzed is that untrusted input cannot panic the SDK, and that
// the documented invariants hold whatever comes back.

func FuzzParseDataURI(f *testing.F) {
	f.Add("data:image/png;base64,iVBORw0KGgo=")
	f.Add("data:;base64,")
	f.Add("data:image/png;base64,")
	f.Add("data:image/png,notbase64")
	f.Add("data:")
	f.Add("https://example.com/a.png")
	f.Add("data:image/png;base64")
	f.Add("data:a;b;base64,x")

	f.Fuzz(func(t *testing.T, uri string) {
		mediaType, data, ok := provider.ParseDataURI(uri)
		if !ok {
			// A rejected URI must not hand back values a caller might use.
			if mediaType != "" || data != "" {
				t.Errorf("ParseDataURI(%q) rejected but returned (%q, %q)", uri, mediaType, data)
			}
			return
		}
		// An accepted URI must be reconstructible from its parts: this is what
		// callers rely on when they re-emit the URI to a different vendor.
		if got := "data:" + mediaType + ";base64," + data; got != uri {
			t.Errorf("ParseDataURI(%q) round trip = %q", uri, got)
		}
	})
}

func FuzzContentToString(f *testing.F) {
	f.Add(`"hello"`)
	f.Add(`[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"x"}}]`)
	f.Add(`[{"type":"text"}]`)
	f.Add(`[null,1,"x",{}]`)
	f.Add(`{}`)
	f.Add(`null`)
	f.Add(`[]`)

	f.Fuzz(func(t *testing.T, raw string) {
		var content any
		if err := json.Unmarshal([]byte(raw), &content); err != nil {
			t.Skip()
		}
		// Whatever the shape, these must agree with each other: text extracted
		// by ContentToString has to be present in the parts ContentToParts
		// produces, or the two views of the same content have diverged.
		text := provider.ContentToString(content)
		parts := provider.ContentToParts(content)

		if text == "" {
			return
		}
		var fromParts strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				fromParts.WriteString(p.Text)
			}
		}
		if fromParts.String() != text {
			t.Errorf("ContentToString = %q but parts yield %q (input %q)", text, fromParts.String(), raw)
		}
	})
}

func FuzzContentToParts(f *testing.F) {
	f.Add(`[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]`)
	f.Add(`[{"type":"image_url","image_url":{}}]`)
	f.Add(`[{"type":"input_audio","input_audio":{"data":"x","format":"wav"}}]`)
	f.Add(`"plain"`)
	f.Add(`""`)

	f.Fuzz(func(t *testing.T, raw string) {
		var content any
		if err := json.Unmarshal([]byte(raw), &content); err != nil {
			t.Skip()
		}
		parts := provider.ContentToParts(content)
		// HasImageParts must agree with what ContentToParts actually produced;
		// adapters branch on it to pick a multimodal wire format.
		wantImage := false
		for _, p := range parts {
			if p.Type == "image_url" {
				wantImage = true
			}
		}
		if got := provider.HasImageParts(content); got != wantImage {
			t.Errorf("HasImageParts = %v but parts contain image = %v (input %q)", got, wantImage, raw)
		}
	})
}

func FuzzDecodeToolCalls(f *testing.F) {
	f.Add(`[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]`)
	f.Add(`[{"id":"c1"}]`)
	f.Add(`[{"function":{"arguments":123}}]`)
	f.Add(`[[]]`)
	f.Add(`"not an array"`)
	f.Add(`null`)

	f.Fuzz(func(t *testing.T, raw string) {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Skip()
		}
		calls := provider.DecodeToolCalls(v)
		// Round-tripping decoded calls back out must not panic and must
		// preserve the count — adapters echo tool calls back upstream on the
		// next turn, so a call lost here silently breaks multi-turn tool use.
		out := provider.MarshalToolCallsForJSON(calls)
		if len(calls) > 0 && len(out) != len(calls) {
			t.Errorf("%d calls in, %d out (input %q)", len(calls), len(out), raw)
		}
	})
}

func FuzzExtractUsageFields(f *testing.F) {
	f.Add(`{"usage":{"prompt_tokens_details":{"cached_tokens":5}}}`)
	f.Add(`{"usage":{"completion_tokens_details":{"reasoning_tokens":7}}}`)
	f.Add(`{"usage":{"prompt_tokens_details":{"cached_tokens":-1}}}`)
	f.Add(`{"usage":{"prompt_tokens_details":{"cached_tokens":"x"}}}`)
	f.Add(`{}`)
	f.Add(`[]`)

	f.Fuzz(func(t *testing.T, raw string) {
		// These read straight from vendor bytes without a schema, so they are
		// exactly where a hostile or merely sloppy response could misbehave.
		// Token counts are counts: negative is never a valid answer.
		if got := provider.ExtractCachedTokens([]byte(raw)); got < 0 {
			t.Errorf("ExtractCachedTokens(%q) = %d, want >= 0", raw, got)
		}
		if got := provider.ExtractReasoningTokens([]byte(raw)); got < 0 {
			t.Errorf("ExtractReasoningTokens(%q) = %d, want >= 0", raw, got)
		}
	})
}

func FuzzNormalizeUsage(f *testing.F) {
	f.Add(0, 0, 0, 0)
	f.Add(1, 2, 3, 1)
	f.Add(-5, -5, -5, -5)
	f.Add(1<<30, 1<<30, 0, 0)

	f.Fuzz(func(t *testing.T, prompt, completion, total, requests int) {
		u := &provider.Usage{
			PromptTokens:     prompt,
			CompletionTokens: completion,
			TotalTokens:      total,
			RequestCount:     requests,
		}
		provider.NormalizeUsage(u)
		// The documented postcondition: per-request billing needs at least 1.
		if u.RequestCount < 1 {
			t.Errorf("NormalizeUsage left RequestCount = %d, want >= 1", u.RequestCount)
		}
	})
}

func FuzzRequestIDFromHeader(f *testing.F) {
	f.Add("X-Request-Id", "abc")
	f.Add("Cf-Ray", "123-LHR")
	f.Add("X-Request-Id", "")
	f.Add("Unrelated", "value")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, name, value string) {
		h := http.Header{}
		// Reject inputs net/http itself would refuse; Header.Set panics on an
		// invalid name, which is the caller's bug, not ours.
		if !validHeaderName(name) || strings.ContainsAny(value, "\r\n") {
			t.Skip()
		}
		h.Set(name, value)
		got := provider.RequestIDFromHeader(h)
		// Whatever it returns must be a value actually present in the header.
		if got != "" && got != value {
			t.Errorf("RequestIDFromHeader returned %q, not in the header (%q: %q)", got, name, value)
		}
	})
}

func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	const tokenChars = "!#$%&'*+-.^_`|~0123456789" +
		"abcdefghijklmnopqrstuvwxyz" +
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	for _, r := range s {
		if !strings.ContainsRune(tokenChars, r) {
			return false
		}
	}
	return true
}

func FuzzTruncateForLog(f *testing.F) {
	f.Add("hello", 3)
	f.Add("", 0)
	f.Add("日本語テキスト", 4)
	f.Add("x", -1)

	f.Fuzz(func(t *testing.T, s string, n int) {
		got := provider.TruncateForLog(s, n)

		if n > 0 && len(s) <= n {
			// Short enough to keep whole.
			if got != s {
				t.Errorf("TruncateForLog(%q, %d) = %q, want it unchanged", s, n, got)
			}
			return
		}
		if s == "" {
			if got != "" {
				t.Errorf("TruncateForLog(\"\", %d) = %q, want empty", n, got)
			}
			return
		}
		// Otherwise it was cut: the retained prefix must respect the bound, and
		// the result must say it is partial. The marker itself is allowed to
		// push the total past n — the point is bounding the payload, not the
		// message.
		const marker = "…(truncated)"
		if !strings.HasSuffix(got, marker) {
			t.Fatalf("TruncateForLog(%q, %d) = %q, want a truncation marker", s, n, got)
		}
		kept := strings.TrimSuffix(got, marker)
		if n > 0 && len(kept) > n {
			t.Errorf("TruncateForLog(%q, %d) kept %d bytes, want <= %d", s, n, len(kept), n)
		}
		if !strings.HasPrefix(s, kept) {
			t.Errorf("TruncateForLog(%q, %d) kept %q, which is not a prefix", s, n, kept)
		}
	})
}
