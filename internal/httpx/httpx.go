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
// context-supplied extra headers and then strips client IP disclosure headers.
// This is what providers should use.
//
// Order matters: extras go on first, IP stripping runs last, so a caller cannot
// re-introduce a forwarding header that would disclose an end user's address.
func NewOutbound() http.RoundTripper {
	return headerTransport{base: ipprivacy.NewTransport(NewTransport())}
}

type headerTransport struct {
	base http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || len(extraHeaders(req.Context())) == 0 {
		return t.base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	applyExtraHeaders(req.Context(), clone.Header)
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
