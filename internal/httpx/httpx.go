// Package httpx centralizes outbound HTTP transport construction for every
// provider in this SDK.
//
// Having a single constructor keeps three properties consistent across all
// providers:
//
//   - Proxy support. Transports built by hand with &http.Transport{} do NOT
//     honor HTTP_PROXY/HTTPS_PROXY/NO_PROXY — only http.DefaultTransport does,
//     because it sets Proxy explicitly. Library users frequently reach upstream
//     vendors through a proxy, so every transport here sets
//     http.ProxyFromEnvironment.
//   - Client IP privacy. Outbound requests are stripped of forwarding headers
//     that would disclose an end user's address to the model vendor.
//   - Connection pooling and streaming behavior tuned for SSE workloads.
package httpx

import (
	"net/http"
	"time"

	"github.com/siguago/llmkit/internal/ipprivacy"
)

// Default timeouts. Streaming gets a much larger ceiling because a long
// generation legitimately holds the connection open; the non-streaming ceiling
// still has to accommodate image models that routinely take 30-90s.
const (
	DefaultTimeout       = 300 * time.Second
	DefaultStreamTimeout = 900 * time.Second
)

// NewTransport returns a tuned *http.Transport for talking to model vendors.
//
// DisableCompression is on so SSE frames aren't gzipped — decompression adds
// latency to every chunk and the payloads are already small.
func NewTransport() *http.Transport {
	return &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     120 * time.Second,
		ForceAttemptHTTP2:   true,
		DisableCompression:  true,
	}
}

// NewOutbound returns a transport that, on every RoundTrip, applies any
// context-supplied extra headers, strips client IP disclosure headers, and then
// dials through either a caller-supplied transport (see WithBaseTransport) or
// the tuned default. This is what providers should use.
//
// Order matters: extras go on first, IP stripping runs next, and the swappable
// base runs last. A caller therefore cannot re-introduce a forwarding header
// that would disclose an end user's address — not through extra headers, and
// not by installing their own transport.
func NewOutbound() http.RoundTripper {
	return headerTransport{
		base: ipprivacy.NewTransport(switchableTransport{fallback: NewTransport()}),
	}
}

type headerTransport struct {
	base http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return t.base.RoundTrip(req)
	}
	ctx := req.Context()
	if len(extraHeaders(ctx)) == 0 && !hasRequestID(ctx) {
		return t.base.RoundTrip(req)
	}
	// RoundTrip must not mutate the request it is handed, and the request ID is
	// regenerated here rather than upstream so that each attempt of a retried
	// call gets its own.
	clone := req.Clone(ctx)
	applyExtraHeaders(ctx, clone.Header)
	applyRequestID(ctx, clone.Header)
	return t.base.RoundTrip(clone)
}

// NewClient returns a client with the given timeout over NewOutbound.
// Pass 0 for DefaultTimeout.
func NewClient(timeout time.Duration) *http.Client {
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return &http.Client{Timeout: timeout, Transport: NewOutbound()}
}

// NewClientPair returns the (non-streaming, streaming) client pair every
// provider needs. Both share transport settings but differ in overall timeout.
func NewClientPair() (client, streamClient *http.Client) {
	shared := NewOutbound()
	return &http.Client{Timeout: DefaultTimeout, Transport: shared},
		&http.Client{Timeout: DefaultStreamTimeout, Transport: shared}
}
