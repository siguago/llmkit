// Package ipprivacy strips client IP disclosure headers from outbound requests.
package ipprivacy

import "net/http"

// ClientIPHeaders are request headers commonly used by reverse proxies and
// CDNs to carry the original client IP or proxy chain. They must never be sent
// from this gateway to upstream model providers.
var ClientIPHeaders = []string{
	"Forwarded",
	"Via",
	"X-Forwarded",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Port",
	"X-Forwarded-Proto",
	"X-Forwarded-Ssl",
	"X-Original-Forwarded-For",
	"X-Real-IP",
	"X-Client-IP",
	"Client-IP",
	"True-Client-IP",
	"CF-Connecting-IP",
	"Fastly-Client-IP",
	"Fly-Client-IP",
	"DO-Connecting-IP",
	"X-Appengine-User-IP",
	"X-Azure-ClientIP",
	"X-Cluster-Client-IP",
	"X-Envoy-External-Address",
	"X-Originating-IP",
	"X-ProxyUser-IP",
	"X-Remote-Addr",
	"X-Remote-IP",
}

// StripClientIPHeaders deletes all known client IP disclosure headers from h.
func StripClientIPHeaders(h http.Header) {
	for _, name := range ClientIPHeaders {
		h.Del(name)
	}
}

// NewTransport wraps base so every outbound request has client IP disclosure
// headers removed immediately before RoundTrip. Passing nil uses
// http.DefaultTransport.
func NewTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return stripTransport{base: base}
}

type stripTransport struct {
	base http.RoundTripper
}

func (t stripTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return t.base.RoundTrip(req)
	}
	clean := req.Clone(req.Context())
	StripClientIPHeaders(clean.Header)
	return t.base.RoundTrip(clean)
}
