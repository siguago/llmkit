package httpx

import (
	"context"
	"net/http"
	"strings"
)

type extraHeadersKey struct{}

// WithExtraHeaders returns a context carrying headers that the outbound
// transport will add to every request made under it.
//
// Threading them through the context rather than through each provider's
// constructor is what lets the root Client offer WithHeader without every
// adapter having to accept and forward a header map: adapters already build
// their requests with http.NewRequestWithContext, so the values arrive for
// free — including on internally derived contexts such as the 30s bound
// ListModels puts on itself.
func WithExtraHeaders(ctx context.Context, h map[string]string) context.Context {
	if len(h) == 0 {
		return ctx
	}
	return context.WithValue(ctx, extraHeadersKey{}, h)
}

func extraHeaders(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(extraHeadersKey{}).(map[string]string)
	return h
}

// protectedHeaders must never be set from caller-supplied extras: they carry
// the credential the adapter negotiated, and silently replacing one would send
// the wrong key upstream — or leak a key to a provider it wasn't issued for.
var protectedHeaders = map[string]bool{
	"authorization":     true,
	"x-api-key":         true,
	"x-goog-api-key":    true,
	"api-key":           true,
	"anthropic-version": true,
}

// applyExtraHeaders sets context-supplied headers on h, skipping protected
// ones. Existing values are overwritten so a caller can, for example, replace
// the default Content-Type.
func applyExtraHeaders(ctx context.Context, h http.Header) {
	for name, value := range extraHeaders(ctx) {
		if protectedHeaders[strings.ToLower(name)] {
			continue
		}
		h.Set(name, value)
	}
}

type requestIDKey struct{}

type requestIDConfig struct {
	fn     func() string
	header string
}

// WithRequestIDFunc returns a context whose requests each carry a freshly
// generated identifier under the given header name.
//
// This is separate from WithExtraHeaders because the value must differ per
// request, and the extras map is resolved once per Client call. Generating it
// down here — at RoundTrip time — is what makes each retry attempt, and each
// internal follow-up request an adapter makes, individually identifiable.
func WithRequestIDFunc(ctx context.Context, fn func() string, header string) context.Context {
	if fn == nil {
		return ctx
	}
	if header == "" {
		header = "X-Request-Id"
	}
	return context.WithValue(ctx, requestIDKey{}, requestIDConfig{fn: fn, header: header})
}

// applyRequestID stamps a generated identifier on h, if one was configured. An
// empty generated value is treated as "skip this one" rather than written as an
// empty header.
func applyRequestID(ctx context.Context, h http.Header) {
	if ctx == nil {
		return
	}
	cfg, ok := ctx.Value(requestIDKey{}).(requestIDConfig)
	if !ok || cfg.fn == nil {
		return
	}
	if protectedHeaders[strings.ToLower(cfg.header)] {
		return
	}
	if id := cfg.fn(); id != "" {
		h.Set(cfg.header, id)
	}
}

// hasRequestID reports whether a generator is installed, so RoundTrip can skip
// cloning the request when there is nothing to add.
func hasRequestID(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	cfg, ok := ctx.Value(requestIDKey{}).(requestIDConfig)
	return ok && cfg.fn != nil
}
