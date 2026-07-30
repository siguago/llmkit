package vercel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/siguago/llmkit/provider"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// withTransport swaps the provider's HTTP client so a test can inspect the
// outgoing request and script the response.
func withTransport(p *Provider, fn roundTripFunc) *Provider {
	p.client = &http.Client{Transport: fn}
	return p
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func ptrInt(v int) *int    { return &v }
func ptrBool(v bool) *bool { return &v }

// Every field the adapter claims to forward must land in the body under its
// OpenAI-compatible name, at the images endpoint, with the key attached.
func TestGenerateImage_RequestShape(t *testing.T) {
	var got map[string]any
	var path, auth string

	p := withTransport(New(""), func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		return jsonResponse(http.StatusOK, `{"created":1,"data":[{"b64_json":"AAAA"}]}`), nil
	})

	_, err := p.GenerateImage(context.Background(), "vk-test", "openai/gpt-image-2", &provider.ImageGenerationRequest{
		Prompt:            "a cat",
		N:                 ptrInt(2),
		Size:              "1024x1024",
		Background:        "transparent",
		Moderation:        "low",
		Quality:           "high",
		Style:             "vivid",
		OutputFormat:      "webp",
		OutputCompression: ptrInt(80),
		User:              "u-1",
		ResponseFormat:    "b64_json",
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	if path != "/v1/images/generations" {
		t.Errorf("path = %q, want /v1/images/generations", path)
	}
	if auth != "Bearer vk-test" {
		t.Errorf("Authorization = %q, want Bearer vk-test", auth)
	}

	want := map[string]any{
		"model":              "openai/gpt-image-2",
		"prompt":             "a cat",
		"n":                  float64(2),
		"size":               "1024x1024",
		"background":         "transparent",
		"moderation":         "low",
		"quality":            "high",
		"style":              "vivid",
		"output_format":      "webp",
		"output_compression": float64(80),
		"user":               "u-1",
		"response_format":    "b64_json",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("body[%q] = %v (%T), want %v", k, got[k], got[k], v)
		}
	}
}

// Unset optional fields must be omitted entirely rather than sent as zero
// values — "size": "" is a different request from no size at all.
func TestGenerateImage_OmitsUnsetFields(t *testing.T) {
	var got map[string]any
	p := withTransport(New(""), func(r *http.Request) (*http.Response, error) {
		json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(http.StatusOK, `{"data":[]}`), nil
	})

	if _, err := p.GenerateImage(context.Background(), "k", "m", &provider.ImageGenerationRequest{Prompt: "p"}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	if len(got) != 2 {
		t.Errorf("a minimal request should carry only model and prompt, got %v", got)
	}
	for _, k := range []string{"n", "size", "quality", "style", "user", "response_format", "stream"} {
		if _, present := got[k]; present {
			t.Errorf("unset field %q should be omitted, got %v", k, got[k])
		}
	}
}

// ResponseFormat is a passthrough with a whitelist: an unrecognised value is
// dropped rather than forwarded for the upstream to reject.
func TestGenerateImage_ResponseFormatWhitelist(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   bool
	}{
		{"url", true},
		{"b64_json", true},
		{"png", false},
		{"", false},
	} {
		t.Run(tc.format, func(t *testing.T) {
			var got map[string]any
			p := withTransport(New(""), func(r *http.Request) (*http.Response, error) {
				json.NewDecoder(r.Body).Decode(&got)
				return jsonResponse(http.StatusOK, `{"data":[]}`), nil
			})

			if _, err := p.GenerateImage(context.Background(), "k", "m", &provider.ImageGenerationRequest{
				Prompt: "p", ResponseFormat: tc.format,
			}); err != nil {
				// Without this the "not forwarded" cases would pass on a
				// request that was never built.
				t.Fatalf("GenerateImage: %v", err)
			}

			_, present := got["response_format"]
			if present != tc.want {
				t.Errorf("response_format=%q forwarded=%v, want %v", tc.format, present, tc.want)
			}
		})
	}
}

// The gateway has no streaming image endpoint, so stream=true is rejected
// locally. The assertion that matters is that no HTTP call is made: letting it
// through would bill the caller for a response whose chunks get dropped.
func TestGenerateImage_RejectsStreamWithoutCallingUpstream(t *testing.T) {
	p := withTransport(New(""), func(*http.Request) (*http.Response, error) {
		t.Error("stream=true must be rejected before any HTTP request")
		return nil, errors.New("unreachable")
	})

	_, err := p.GenerateImage(context.Background(), "k", "m", &provider.ImageGenerationRequest{
		Prompt: "p", Stream: ptrBool(true),
	})
	if err == nil {
		t.Fatal("expected stream=true to be rejected")
	}

	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected a *provider.ProviderError, got %T", err)
	}
	if pe.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", pe.StatusCode)
	}
}

// stream=false is a legitimate explicit value and must reach the upstream.
func TestGenerateImage_ExplicitStreamFalseIsForwarded(t *testing.T) {
	var got map[string]any
	p := withTransport(New(""), func(r *http.Request) (*http.Response, error) {
		json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(http.StatusOK, `{"data":[]}`), nil
	})

	if _, err := p.GenerateImage(context.Background(), "k", "m", &provider.ImageGenerationRequest{
		Prompt: "p", Stream: ptrBool(false),
	}); err != nil {
		t.Fatalf("stream=false should be allowed: %v", err)
	}
	if got["stream"] != false {
		t.Errorf("body[stream] = %v, want false", got["stream"])
	}
}

// Image endpoints occasionally answer 201; treating it as an error would fail
// a request that actually succeeded and was billed.
func TestGenerateImage_Accepts201(t *testing.T) {
	p := withTransport(New(""), func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusCreated, `{"created":7,"data":[{"b64_json":"QQ=="}]}`), nil
	})

	resp, err := p.GenerateImage(context.Background(), "k", "m", &provider.ImageGenerationRequest{Prompt: "p"})
	if err != nil {
		t.Fatalf("201 should be treated as success: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Errorf("data not parsed from a 201 response: %+v", resp)
	}
}

func TestGenerateImage_UpstreamError(t *testing.T) {
	p := withTransport(New(""), func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`), nil
	})

	_, err := p.GenerateImage(context.Background(), "k", "m", &provider.ImageGenerationRequest{Prompt: "p"})

	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected a *provider.ProviderError, got %T (%v)", err, err)
	}
	if pe.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", pe.StatusCode)
	}
}

func TestGenerateImage_TransportError(t *testing.T) {
	p := withTransport(New(""), func(*http.Request) (*http.Response, error) {
		return nil, errors.New("gateway unreachable")
	})

	if _, err := p.GenerateImage(context.Background(), "k", "m", &provider.ImageGenerationRequest{Prompt: "p"}); err == nil {
		t.Fatal("expected the transport error to surface")
	}
}

// ProviderOptions is an opaque passthrough, so a caller can put something
// unserialisable in it. That has to surface as an error before the request is
// sent, not panic and not go out as a half-built body.
func TestGenerateImage_UnserialisableProviderOptions(t *testing.T) {
	p := withTransport(New(""), func(*http.Request) (*http.Response, error) {
		t.Error("a body that cannot be marshalled must never reach the transport")
		return nil, errors.New("unreachable")
	})

	_, err := p.GenerateImage(context.Background(), "k", "m", &provider.ImageGenerationRequest{
		Prompt:          "p",
		ProviderOptions: map[string]any{"vercel": map[string]any{"bad": make(chan int)}},
	})
	if err == nil {
		t.Fatal("expected a marshal error")
	}
}

func TestGenerateImage_MalformedResponseBody(t *testing.T) {
	p := withTransport(New(""), func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"data": [`), nil
	})

	if _, err := p.GenerateImage(context.Background(), "k", "m", &provider.ImageGenerationRequest{Prompt: "p"}); err == nil {
		t.Fatal("a truncated body should error, not yield a half-built response")
	}
}

// The adapter stamps its own name so callers can attribute a response that
// travelled through the aggregator.
func TestGenerateImage_StampsProviderName(t *testing.T) {
	p := withTransport(New(""), func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"data":[{"b64_json":"QQ=="}]}`), nil
	})

	resp, err := p.GenerateImage(context.Background(), "k", "m", &provider.ImageGenerationRequest{Prompt: "p"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if resp.Provider != name {
		t.Errorf("Provider = %q, want %q", resp.Provider, name)
	}
}

// ProviderOptions["vercel"] is merged into the body so vendor-specific knobs
// ride along without new struct fields.
func TestGenerateImage_ProviderOptionsMerged(t *testing.T) {
	var got map[string]any
	p := withTransport(New(""), func(r *http.Request) (*http.Response, error) {
		json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(http.StatusOK, `{"data":[]}`), nil
	})

	if _, err := p.GenerateImage(context.Background(), "k", "m", &provider.ImageGenerationRequest{
		Prompt: "p",
		ProviderOptions: map[string]any{
			"vercel": map[string]any{"providerOptions": "ladder"},
			"openai": map[string]any{"ignored": true},
		},
	}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	if got["providerOptions"] != "ladder" {
		t.Errorf("vercel extras not merged: %v", got)
	}
	if _, leaked := got["ignored"]; leaked {
		t.Errorf("another vendor's block leaked into the body: %v", got)
	}
}

// AspectRatio has no OpenAI-compatible equivalent on this endpoint and is
// silently dropped. Pinning it here so the omission is a decision on record
// rather than something rediscovered from a confusing upstream response.
func TestGenerateImage_AspectRatioIsNotForwarded(t *testing.T) {
	var got map[string]any
	p := withTransport(New(""), func(r *http.Request) (*http.Response, error) {
		json.NewDecoder(r.Body).Decode(&got)
		return jsonResponse(http.StatusOK, `{"data":[]}`), nil
	})

	if _, err := p.GenerateImage(context.Background(), "k", "m", &provider.ImageGenerationRequest{
		Prompt: "p", AspectRatio: "16:9",
	}); err != nil {
		// A purely negative assertion needs proof the request was actually
		// built, or an early failure reads as "the field was omitted".
		t.Fatalf("GenerateImage: %v", err)
	}

	if _, present := got["aspect_ratio"]; present {
		t.Errorf("aspect_ratio is not part of this endpoint's contract; "+
			"if it were added, update this test and the README: %v", got)
	}
}

func TestParseImagesResponse_B64(t *testing.T) {
	raw := []byte(`{
		"created": 1234567890,
		"data": [{"b64_json": "iVBORw0K"}, {"b64_json": "AAAAAA"}],
		"usage": {"input_tokens": 12, "output_tokens": 3, "total_tokens": 15}
	}`)

	resp, err := parseImagesResponse("openai/gpt-image-2", raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Created != 1234567890 || resp.Model != "openai/gpt-image-2" {
		t.Errorf("header fields wrong: %+v", resp)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("data count = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].B64JSON != "iVBORw0K" {
		t.Errorf("b64_json not preserved: %+v", resp.Data[0])
	}
	if resp.Data[0].MimeType != "image/png" {
		t.Errorf("b64 assets should default to image/png, got %q", resp.Data[0].MimeType)
	}
	if resp.Data[0].Type != "image" {
		t.Errorf("asset type = %q, want image", resp.Data[0].Type)
	}
	if resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 3 || resp.Usage.TotalTokens != 15 {
		t.Errorf("token usage mismapped: %+v", resp.Usage)
	}
	if resp.Usage.ImageCount != 2 || resp.Usage.MediaCount != 2 || resp.Usage.RequestCount != 1 {
		t.Errorf("billing dimensions wrong: %+v", resp.Usage)
	}
}

func TestParseImagesResponse_URLAndMetadata(t *testing.T) {
	raw := []byte(`{
		"created": 99,
		"data": [{"url": "https://cdn.example/a.png", "revised_prompt": "polished", "size": "1024x1024"}]
	}`)

	resp, err := parseImagesResponse("m", raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	asset := resp.Data[0]
	if asset.URL != "https://cdn.example/a.png" {
		t.Errorf("URL not preserved: %+v", asset)
	}
	// URL-delivered assets carry no bytes, so nothing can be sniffed and no
	// mime should be invented.
	if asset.MimeType != "" {
		t.Errorf("a URL asset should not claim a mime type, got %q", asset.MimeType)
	}
	if asset.Metadata["revised_prompt"] != "polished" || asset.Metadata["size"] != "1024x1024" {
		t.Errorf("metadata lost: %+v", asset.Metadata)
	}
}

// No metadata fields means no map at all, rather than an empty one callers
// would have to distinguish from absent.
func TestParseImagesResponse_NoMetadataMeansNilMap(t *testing.T) {
	resp, err := parseImagesResponse("m", []byte(`{"data":[{"b64_json":"QQ=="}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Data[0].Metadata != nil {
		t.Errorf("expected nil metadata, got %+v", resp.Data[0].Metadata)
	}
}

// A vendor that omits `created` must not yield a zero timestamp, which reads
// as 1970 in any downstream log or record.
func TestParseImagesResponse_CreatedFallsBackToNow(t *testing.T) {
	before := time.Now().Unix()
	resp, err := parseImagesResponse("m", []byte(`{"data":[{"b64_json":"QQ=="}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Created < before {
		t.Errorf("Created = %d, want a timestamp at or after %d", resp.Created, before)
	}
}

// Per-image-billed models report input/output but no total; deriving it keeps
// the field meaningful for every caller.
func TestParseImagesResponse_TotalTokensDerived(t *testing.T) {
	resp, err := parseImagesResponse("m", []byte(`{
		"data":[{"b64_json":"QQ=="}],
		"usage":{"input_tokens":10,"output_tokens":5}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("TotalTokens = %d, want 10+5=15", resp.Usage.TotalTokens)
	}
}

// Per-image-billed models send no usage block at all. Usage must still exist
// with the per-call dimensions filled, or billing has nothing to multiply.
func TestParseImagesResponse_NoUsageBlock(t *testing.T) {
	resp, err := parseImagesResponse("m", []byte(`{"data":[{"b64_json":"QQ=="},{"b64_json":"Qg=="}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("Usage must never be nil — per-image billing depends on ImageCount")
	}
	if resp.Usage.ImageCount != 2 || resp.Usage.RequestCount != 1 {
		t.Errorf("per-call dimensions wrong: %+v", resp.Usage)
	}
	if resp.Usage.TotalTokens != 0 {
		t.Errorf("no usage block should mean zero tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestParseImagesResponse_EmptyData(t *testing.T) {
	resp, err := parseImagesResponse("m", []byte(`{"created":1,"data":[]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Data) != 0 || resp.Usage.ImageCount != 0 {
		t.Errorf("empty data should stay empty: %+v", resp)
	}
}

func TestParseImagesResponse_Malformed(t *testing.T) {
	if _, err := parseImagesResponse("m", []byte(`not json`)); err == nil {
		t.Fatal("expected a JSON error")
	}
}

func TestVercelExtras(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{"nil options", nil, nil},
		{"no vercel block", map[string]any{"openai": map[string]any{"a": 1}}, nil},
		{"vercel block not a map", map[string]any{"vercel": "oops"}, nil},
		{"vercel block empty", map[string]any{"vercel": map[string]any{}}, map[string]any{}},
		{"vercel block copied", map[string]any{"vercel": map[string]any{"a": 1}}, map[string]any{"a": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := vercelExtras(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Errorf("want nil, got %v", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("got[%q] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

// The returned map must be a copy: merging it into the request body must not
// mutate the caller's ProviderOptions.
func TestVercelExtras_ReturnsCopy(t *testing.T) {
	inner := map[string]any{"a": 1}
	got := vercelExtras(map[string]any{"vercel": inner})

	got["b"] = 2
	if _, leaked := inner["b"]; leaked {
		t.Error("vercelExtras returned the caller's map, not a copy")
	}
}

func TestIsTrue(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{true, true},
		{false, false},
		{nil, false},
		{"true", false}, // a string must not be coerced
		{1, false},
	}
	for _, tc := range cases {
		if got := isTrue(tc.in); got != tc.want {
			t.Errorf("isTrue(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The capability contract this adapter is built around: Vercel's gateway has
// no image-editing endpoint, so the type must NOT satisfy provider.ImageEditor.
// An EditImage method returning ErrUnsupported would satisfy the interface and
// make Client.SupportsImageEditing report true for a call that always fails.
//
// The embedded *compat.Provider makes this worth asserting at runtime rather
// than trusting by inspection: a method added to compat would be promoted here
// automatically and silently flip the answer.
func TestVercel_CapabilitySurface(t *testing.T) {
	var p any = New("")

	if _, ok := p.(provider.ImageEditor); ok {
		t.Error("vercel must not implement provider.ImageEditor — the gateway has " +
			"no /v1/images/edits endpoint, and satisfying the interface would make " +
			"SupportsImageEditing lie")
	}
	if _, ok := p.(provider.ImageGenerator); !ok {
		t.Error("vercel should implement provider.ImageGenerator")
	}
	if _, ok := p.(provider.Provider); !ok {
		t.Error("vercel should implement provider.Provider")
	}
	if _, ok := p.(provider.Embedder); !ok {
		t.Error("vercel rides on compat.Provider, which supplies Embeddings")
	}
}
