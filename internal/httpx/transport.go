package httpx

import (
	"context"
	"net/http"
)

type baseTransportKey struct{}

// WithBaseTransport returns a context carrying a caller-supplied RoundTripper
// for the outbound transport to dial through.
//
// It goes through the context for the same reason extra headers do: adapters
// build their requests with http.NewRequestWithContext and construct their own
// *http.Client at New() time, so there is no constructor to thread a transport
// into without changing every adapter's signature. This also means a custom
// transport reaches internally derived contexts — the 30s bound ListModels puts
// on itself, the media clients' longer ceilings — for free.
//
// The supplied transport sits at the BOTTOM of the chain. Header injection and
// client-IP stripping still wrap it, so a caller instrumenting requests cannot
// accidentally drop the privacy guarantees, and cannot see a request before the
// credential headers are on it.
func WithBaseTransport(ctx context.Context, rt http.RoundTripper) context.Context {
	if rt == nil {
		return ctx
	}
	return context.WithValue(ctx, baseTransportKey{}, rt)
}

func baseTransport(ctx context.Context) http.RoundTripper {
	if ctx == nil {
		return nil
	}
	rt, _ := ctx.Value(baseTransportKey{}).(http.RoundTripper)
	return rt
}

// switchableTransport dispatches to the context-supplied RoundTripper when the
// caller provided one, and to the SDK's tuned default otherwise.
type switchableTransport struct {
	fallback http.RoundTripper
}

func (s switchableTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt := baseTransport(req.Context()); rt != nil {
		return rt.RoundTrip(req)
	}
	return s.fallback.RoundTrip(req)
}
