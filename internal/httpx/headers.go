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
