package provider

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrorCategory is the provider-independent meaning of an API failure when the
// HTTP status cannot carry it. Some vendors return HTTP 200 and put the real
// error in a JSON envelope; adapters classify those errors here so the root
// helpers can still answer IsAuthError, IsRateLimited and IsRetryable without
// pretending the upstream sent a different HTTP status.
type ErrorCategory string

const (
	// ErrorCategoryAuth is an invalid, expired or unprivileged credential.
	ErrorCategoryAuth ErrorCategory = "auth"
	// ErrorCategoryRateLimit is a quota or rate-limit refusal.
	//
	// Attaching it carries a second, stronger claim than the other categories:
	// that the upstream refused the work *before* starting it, so nothing
	// billable happened. IsSafeToReplay trusts that claim and will replay
	// image and video creation on it, where a duplicate attempt costs real
	// money. An HTTP 429 earns the claim by itself; a vendor that answers 200
	// and reports the limit in the body does not, so only use this category
	// there when the vendor documents that such a response is never charged.
	// When in doubt leave the error uncategorized: the HTTP status then decides,
	// and a 200 is neither retryable nor replay-safe.
	ErrorCategoryRateLimit ErrorCategory = "rate_limit"
	// ErrorCategoryInvalidRequest is a malformed or rejected request.
	ErrorCategoryInvalidRequest ErrorCategory = "invalid_request"
	// ErrorCategoryNotFound is a missing model or resource.
	ErrorCategoryNotFound ErrorCategory = "not_found"
	// ErrorCategoryServer is an upstream-side failure.
	ErrorCategoryServer ErrorCategory = "server"
)

// ErrorMetadata is the optional provider-specific meaning attached to an error
// without changing ProviderError's stable struct shape.
//
// Use ProviderCode and ErrorCategoryOf rather than asserting this interface
// directly; the helpers walk wrapped errors for you.
type ErrorMetadata interface {
	error
	ProviderCode() string
	ErrorCategory() ErrorCategory
}

type errorWithMetadata struct {
	err      error
	code     string
	category ErrorCategory
}

func (e *errorWithMetadata) Error() string                { return e.err.Error() }
func (e *errorWithMetadata) Unwrap() error                { return e.err }
func (e *errorWithMetadata) ProviderCode() string         { return e.code }
func (e *errorWithMetadata) ErrorCategory() ErrorCategory { return e.category }

// valid reports whether c is one of the defined categories. ErrorCategory is a
// string type, so a typo is a compile-time no-op and would otherwise become a
// silent behavior change — see WithErrorMetadata.
func (c ErrorCategory) valid() bool {
	switch c {
	case ErrorCategoryAuth, ErrorCategoryRateLimit, ErrorCategoryInvalidRequest,
		ErrorCategoryNotFound, ErrorCategoryServer:
		return true
	}
	return false
}

// WithErrorMetadata annotates err with a vendor error code and normalized
// category while preserving errors.Is/errors.As access to the original error.
// A nil error stays nil; empty metadata returns err unchanged.
//
// The category overrides HTTP status for every classification helper, which is
// the point on a vendor that answers 200 — and the risk everywhere else. Read
// ErrorCategoryRateLimit before attaching it: it additionally asserts that
// nothing billable happened, and replay of paid endpoints depends on that.
// Passing "" for the category is always safe and leaves the status in charge.
//
// An unrecognized category is dropped rather than honored. Because the helpers
// short-circuit on any non-empty category, keeping a misspelled one would make
// an upstream 500 report as neither a server error nor retryable — a typo in a
// string constant silently disabling retries. The vendor code is still kept, so
// ProviderCode works and the HTTP status resumes deciding.
func WithErrorMetadata(err error, code string, category ErrorCategory) error {
	if err == nil {
		return err
	}
	if !category.valid() {
		category = ""
	}
	if code == "" && category == "" {
		return err
	}
	return &errorWithMetadata{err: err, code: code, category: category}
}

// ProviderCode returns the vendor's own error code, or "" when none was
// attached.
func ProviderCode(err error) string {
	var metadata ErrorMetadata
	if errors.As(err, &metadata) {
		return metadata.ProviderCode()
	}
	return ""
}

// ErrorCategoryOf returns the normalized meaning attached to err, or "" when
// ordinary HTTP status remains the only classification source.
func ErrorCategoryOf(err error) ErrorCategory {
	var metadata ErrorMetadata
	if errors.As(err, &metadata) {
		return metadata.ErrorCategory()
	}
	return ""
}

// ProviderError represents an error returned by an upstream provider API.
// It carries the upstream HTTP status code for passthrough to the client.
//
// Keep this shape stable: custom adapters and callers may construct it with an
// unkeyed literal. Provider-specific metadata belongs in WithErrorMetadata.
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
// Google sends x-guploader-uploadid on some endpoints, MiniMax documents
// trace_id (and deployments also use trace-id / minimax-request-id), OpenRouter
// sends x-generation-id, and Cloudflare-fronted gateways add cf-ray. Checking
// several costs nothing and means the field is populated for far more vendors
// than picking one name would allow.
var requestIDHeaders = []string{
	"X-Request-Id",
	"Request-Id",
	"X-Amzn-Requestid",
	"X-Ms-Request-Id",
	"X-Guploader-Uploadid",
	"X-Generation-Id",
	"Minimax-Request-Id",
	// Both spellings occur; these are the canonical forms Header.Get resolves
	// "trace_id" and "trace-id" to.
	"Trace_id",
	"Trace-Id",
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
