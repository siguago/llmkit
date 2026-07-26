package provider

import (
	"fmt"
	"net/http"
)

// ProviderError represents an error returned by an upstream provider API.
// It carries the upstream HTTP status code for passthrough to the client.
type ProviderError struct {
	StatusCode int
	Message    string
	// RetryAfter is the upstream's Retry-After header value (seconds or HTTP
	// date). Set on rate-limit/overload responses so the caller can back off
	// on the vendor's own terms.
	RetryAfter string
	// RequestID is the vendor's identifier for the failed request, when it sent
	// one. This is what vendor support asks for first, and it is unrecoverable
	// after the fact — the response is gone by the time you notice a pattern in
	// your error rate. Empty when the vendor sent no recognizable header.
	RequestID string
}

func (e *ProviderError) Error() string {
	return e.Message
}

// NewProviderError creates a ProviderError with the given status code and formatted message.
func NewProviderError(statusCode int, name string, body []byte) *ProviderError {
	return &ProviderError{
		StatusCode: statusCode,
		Message:    fmt.Sprintf("%s api error (status %d): %s", name, statusCode, string(body)),
	}
}

// NewProviderErrorFromResponse creates a ProviderError preserving the upstream
// Retry-After header and request ID. Use this whenever the http.Response is in
// scope: Retry-After is what lets clients back off on the vendor's terms, and
// the request ID is what vendor support will ask for.
func NewProviderErrorFromResponse(resp *http.Response, name string, body []byte) *ProviderError {
	pe := NewProviderError(resp.StatusCode, name, body)
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		pe.RetryAfter = ra
	}
	pe.RequestID = RequestIDFromHeader(resp.Header)
	return pe
}

// requestIDHeaders are the response headers vendors use to return their own
// request identifier, in precedence order. They disagree: OpenAI and most
// OpenAI-compatible vendors send X-Request-Id, Anthropic sends request-id,
// Google sends x-guploader-uploadid on some endpoints, and Cloudflare-fronted
// gateways add cf-ray. Checking several costs nothing and means the field is
// populated for far more vendors than picking one name would allow.
var requestIDHeaders = []string{
	"X-Request-Id",
	"Request-Id",
	"X-Amzn-Requestid",
	"X-Ms-Request-Id",
	"Cf-Ray",
}

// RequestIDFromHeader extracts the vendor's request identifier, or "" if the
// response carried none.
func RequestIDFromHeader(h http.Header) string {
	for _, name := range requestIDHeaders {
		if v := h.Get(name); v != "" {
			return v
		}
	}
	return ""
}
