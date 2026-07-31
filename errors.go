package llmkit

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/siguago/llmkit/provider"
)

// ErrNoAPIKey is returned by New when no credential was supplied.
var ErrNoAPIKey = errors.New("llmkit: no API key (pass WithAPIKey, or set the provider's env var)")

// ErrUnknownProvider is returned by New for an unregistered provider name.
var ErrUnknownProvider = errors.New("llmkit: unknown provider")

// ErrUnsupported reports that the selected provider does not implement the
// requested capability — e.g. asking Zhipu for video generation, or asking
// Anthropic to cancel a job. Test with errors.Is(err, llmkit.ErrUnsupported).
var ErrUnsupported = errors.New("llmkit: capability not supported by this provider")

// unsupportedf builds an ErrUnsupported-wrapping error naming the provider and
// capability, so both errors.Is and the message stay useful.
func unsupportedf(providerName, capability string) error {
	return &unsupportedError{provider: providerName, capability: capability}
}

type unsupportedError struct {
	provider   string
	capability string
}

func (e *unsupportedError) Error() string {
	return "llmkit: provider " + e.provider + " does not support " + e.capability
}

func (e *unsupportedError) Is(target error) bool { return target == ErrUnsupported }

// StatusCode returns the upstream HTTP status carried by err, or 0 when err did
// not originate from an API response (network failures, context cancellation,
// local validation).
func StatusCode(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

func errorCategory(err error) ErrorCategory {
	return provider.ErrorCategoryOf(err)
}

// ProviderCode returns the vendor's own error code, or "" when the error only
// carries an HTTP status.
func ProviderCode(err error) string { return provider.ProviderCode(err) }

// ErrorCategoryOf returns a provider-independent classification attached to an
// in-body vendor error, or "" when HTTP status is the classification source.
func ErrorCategoryOf(err error) ErrorCategory { return provider.ErrorCategoryOf(err) }

// IsRateLimited reports whether the upstream rejected the request for exceeding
// a quota or rate limit (HTTP 429 or a provider body error with the same
// meaning). Pair it with RetryAfter to back off.
func IsRateLimited(err error) bool {
	if category := errorCategory(err); category != "" {
		return category == ErrorCategoryRateLimit
	}
	return StatusCode(err) == http.StatusTooManyRequests
}

// IsAuthError reports an invalid, expired, or insufficiently privileged
// credential (HTTP 401 / 403 or an equivalent provider body error). Retrying
// will not help.
func IsAuthError(err error) bool {
	if category := errorCategory(err); category != "" {
		return category == ErrorCategoryAuth
	}
	s := StatusCode(err)
	return s == http.StatusUnauthorized || s == http.StatusForbidden
}

// IsNotFound reports that the model or resource does not exist (HTTP 404).
func IsNotFound(err error) bool {
	if category := errorCategory(err); category != "" {
		return category == ErrorCategoryNotFound
	}
	return StatusCode(err) == http.StatusNotFound
}

// IsInvalidRequest reports a malformed or rejected request (HTTP 400 / 422) —
// e.g. an unsupported parameter for the chosen model. Retrying will not help.
func IsInvalidRequest(err error) bool {
	if category := errorCategory(err); category != "" {
		return category == ErrorCategoryInvalidRequest
	}
	s := StatusCode(err)
	return s == http.StatusBadRequest || s == http.StatusUnprocessableEntity
}

// IsServerError reports an upstream-side failure (HTTP 5xx).
func IsServerError(err error) bool {
	if category := errorCategory(err); category != "" {
		return category == ErrorCategoryServer
	}
	s := StatusCode(err)
	return s >= 500 && s <= 599
}

// IsUnsupportedCapability reports that the provider lacks the requested
// capability. Equivalent to errors.Is(err, ErrUnsupported).
func IsUnsupportedCapability(err error) bool { return errors.Is(err, ErrUnsupported) }

// RetryAfter returns the backoff duration the upstream asked for via the
// Retry-After header, or 0 when it did not specify one. Both the delta-seconds
// and HTTP-date forms are understood.
func RetryAfter(err error) time.Duration {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RetryAfter == "" {
		return 0
	}
	return parseRetryAfter(apiErr.RetryAfter, time.Now())
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	// delta-seconds form
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	// HTTP-date form
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// IsRetryable reports whether retrying err has a realistic chance of a
// different outcome: rate limits, upstream 5xx (including Anthropic's 529
// "overloaded"), request timeouts, and transient network failures.
//
// Client errors (4xx other than 408/429) and context cancellation are not
// retryable — the same request will fail the same way.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	// A cancelled or expired caller context is a decision, not a transient
	// fault. Check this first: a request aborted mid-flight can surface as a
	// net.Error timeout that would otherwise look retryable.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if category := errorCategory(err); category != "" {
		switch category {
		case ErrorCategoryRateLimit, ErrorCategoryServer:
			return true
		default:
			return false
		}
	}
	if status := StatusCode(err); status != 0 {
		switch {
		case status == http.StatusTooManyRequests, // 429 rate limited
			status == http.StatusRequestTimeout, // 408
			status == 529,                       // Anthropic "overloaded"
			status >= 500 && status <= 599:
			return true
		default:
			return false
		}
	}
	// Not an API error — network layer. Only timeouts are worth another attempt.
	// A refused connection or a DNS NXDOMAIN almost always means a misconfigured
	// base URL, which no number of retries will fix.
	//
	// Note that *net.OpError and *net.DNSError both satisfy net.Error, so this
	// one check covers them: they reach it and report Timeout() == false.
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// IsSafeToReplay reports whether err proves the upstream never took the work,
// so replaying the request cannot produce a second billable operation.
//
// This asks a different question from IsRetryable, and the two are orthogonal.
// IsRetryable asks "is another attempt likely to succeed"; this asks "is another
// attempt certain not to charge twice". Both must hold before a billable call is
// replayed:
//
//   - A read timeout is retryable but NOT replay-safe — the vendor may have
//     accepted the job and only the response was lost.
//   - A refused connection is replay-safe but NOT retryable — nothing was
//     charged, but a misconfigured base URL won't fix itself on attempt two.
//   - A 429 is both, so it is the case that actually gets replayed.
//
// This is the default retry gate for billable, non-idempotent creation calls,
// including Responses, Anthropic Messages, images, and videos. WithMediaRetry
// can explicitly override it for image and video creation.
func IsSafeToReplay(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if provider.IsMarkedUnsafeToReplay(err) {
		// Stream errors can still be categorized as rate limits for diagnostics
		// and retryability after output may have been delivered. The explicit
		// marker wins because replay could duplicate billable work.
		return false
	}
	if category := errorCategory(err); category != "" {
		// A rate-limit decision explicitly refused the work; other categorized
		// failures may have happened after the operation was accepted. Adapters
		// must only attach ErrorCategoryRateLimit when nothing billable ran —
		// that requirement is documented on the constant, because this is the
		// line where getting it wrong bills the caller twice.
		return category == ErrorCategoryRateLimit
	}
	if status := StatusCode(err); status != 0 {
		// A status code means the request was delivered and the vendor answered.
		// Only an explicit refusal to take the work is safe: 429 is a quota
		// decision made before generation starts. 5xx is not — an overloaded
		// backend may have started the job and then failed to answer.
		return status == http.StatusTooManyRequests
	}
	// Network layer: only a connection that was never established proves the
	// request bytes never left. Anything after dial — a write, a read timeout,
	// a peer reset — may have arrived and been acted on.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// translateUnsupported converts an adapter-level provider.ErrUnsupported into
// the SDK's ErrUnsupported so callers have one thing to test for.
func translateUnsupported(err error) error {
	var u *provider.ErrUnsupported
	if errors.As(err, &u) {
		return &unsupportedError{provider: u.Provider, capability: u.Op}
	}
	return err
}
