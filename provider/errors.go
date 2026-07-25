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
	// date). Set on rate-limit/overload responses so the gateway can echo it
	// to the client for proper backoff.
	RetryAfter string
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
// Retry-After header. Use this when the http.Response is in scope; the
// retry-after is critical for clients to back off on 429/503.
func NewProviderErrorFromResponse(resp *http.Response, name string, body []byte) *ProviderError {
	pe := NewProviderError(resp.StatusCode, name, body)
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		pe.RetryAfter = ra
	}
	return pe
}
