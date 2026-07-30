package llmkit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/siguago/llmkit/provider"
)

func apiErr(status int) error {
	return &APIError{StatusCode: status, Message: "boom"}
}

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429 rate limit", apiErr(http.StatusTooManyRequests), true},
		{"408 timeout", apiErr(http.StatusRequestTimeout), true},
		{"500", apiErr(http.StatusInternalServerError), true},
		{"502", apiErr(http.StatusBadGateway), true},
		{"503", apiErr(http.StatusServiceUnavailable), true},
		{"529 anthropic overloaded", apiErr(529), true},
		{"400 bad request", apiErr(http.StatusBadRequest), false},
		{"401 auth", apiErr(http.StatusUnauthorized), false},
		{"404 not found", apiErr(http.StatusNotFound), false},
		{"422 unprocessable", apiErr(http.StatusUnprocessableEntity), false},
		{"context canceled", context.Canceled, false},
		{"context deadline", context.DeadlineExceeded, false},
		{"plain error", errors.New("nope"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRetryable(tc.err); got != tc.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A cancelled context must not be treated as retryable even when it arrives
// wrapped in a timeout-shaped error — otherwise an aborted request would keep
// being replayed after the caller walked away.
func TestIsRetryable_WrappedContextError(t *testing.T) {
	wrapped := errors.Join(errors.New("dial failed"), context.DeadlineExceeded)
	if IsRetryable(wrapped) {
		t.Error("wrapped context.DeadlineExceeded should not be retryable")
	}
}

func TestErrorClassification(t *testing.T) {
	if !IsRateLimited(apiErr(429)) || IsRateLimited(apiErr(500)) {
		t.Error("IsRateLimited misclassified")
	}
	if !IsAuthError(apiErr(401)) || !IsAuthError(apiErr(403)) || IsAuthError(apiErr(404)) {
		t.Error("IsAuthError misclassified")
	}
	if !IsNotFound(apiErr(404)) || IsNotFound(apiErr(400)) {
		t.Error("IsNotFound misclassified")
	}
	if !IsInvalidRequest(apiErr(400)) || !IsInvalidRequest(apiErr(422)) || IsInvalidRequest(apiErr(429)) {
		t.Error("IsInvalidRequest misclassified")
	}
	if !IsServerError(apiErr(503)) || IsServerError(apiErr(429)) {
		t.Error("IsServerError misclassified")
	}
	if StatusCode(errors.New("plain")) != 0 {
		t.Error("StatusCode should be 0 for non-API errors")
	}
}

func TestBodyErrorClassificationPreservesHTTPStatus(t *testing.T) {
	cases := []struct {
		name       string
		category   ErrorCategory
		rateLimit  bool
		auth       bool
		notFound   bool
		invalid    bool
		server     bool
		retryable  bool
		replaySafe bool
	}{
		{"rate limit", ErrorCategoryRateLimit, true, false, false, false, false, true, true},
		{"auth", ErrorCategoryAuth, false, true, false, false, false, false, false},
		{"not found", ErrorCategoryNotFound, false, false, true, false, false, false, false},
		{"invalid", ErrorCategoryInvalidRequest, false, false, false, true, false, false, false},
		{"server", ErrorCategoryServer, false, false, false, false, true, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := provider.WithErrorMetadata(
				&APIError{StatusCode: http.StatusOK, Message: "body error"},
				"vendor-code",
				tc.category,
			)
			if got := StatusCode(err); got != http.StatusOK {
				t.Errorf("StatusCode = %d, want the real HTTP 200", got)
			}
			if got := ProviderCode(err); got != "vendor-code" {
				t.Errorf("ProviderCode = %q, want vendor-code", got)
			}
			if got := ErrorCategoryOf(err); got != tc.category {
				t.Errorf("ErrorCategoryOf = %q, want %q", got, tc.category)
			}
			if got := IsRateLimited(err); got != tc.rateLimit {
				t.Errorf("IsRateLimited = %v, want %v", got, tc.rateLimit)
			}
			if got := IsAuthError(err); got != tc.auth {
				t.Errorf("IsAuthError = %v, want %v", got, tc.auth)
			}
			if got := IsNotFound(err); got != tc.notFound {
				t.Errorf("IsNotFound = %v, want %v", got, tc.notFound)
			}
			if got := IsInvalidRequest(err); got != tc.invalid {
				t.Errorf("IsInvalidRequest = %v, want %v", got, tc.invalid)
			}
			if got := IsServerError(err); got != tc.server {
				t.Errorf("IsServerError = %v, want %v", got, tc.server)
			}
			if got := IsRetryable(err); got != tc.retryable {
				t.Errorf("IsRetryable = %v, want %v", got, tc.retryable)
			}
			if got := IsSafeToReplay(err); got != tc.replaySafe {
				t.Errorf("IsSafeToReplay = %v, want %v", got, tc.replaySafe)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"30", 30 * time.Second},
		{"  5 ", 5 * time.Second},
		{"0", 0},
		{"-3", 0},
		{"Sat, 25 Jul 2026 12:00:20 GMT", 20 * time.Second},
		{"Sat, 25 Jul 2026 11:59:00 GMT", 0}, // already past
		{"garbage", 0},
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.in, now); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestRetryAfterFromError(t *testing.T) {
	err := &APIError{StatusCode: 429, Message: "slow down", RetryAfter: "7"}
	if got := RetryAfter(err); got != 7*time.Second {
		t.Errorf("RetryAfter = %v, want 7s", got)
	}
	if got := RetryAfter(apiErr(429)); got != 0 {
		t.Errorf("RetryAfter with no header = %v, want 0", got)
	}
}

func TestRetryDo_SucceedsAfterTransientFailures(t *testing.T) {
	rc := RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	calls := 0
	err := rc.do(context.Background(), func() error {
		calls++
		if calls < 3 {
			return apiErr(503)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestRetryDo_StopsAtMaxAttempts(t *testing.T) {
	rc := RetryConfig{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	calls := 0
	err := rc.do(context.Background(), func() error {
		calls++
		return apiErr(500)
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestRetryDo_DoesNotRetryClientErrors(t *testing.T) {
	rc := DefaultRetry()
	rc.InitialBackoff = time.Millisecond
	calls := 0
	err := rc.do(context.Background(), func() error {
		calls++
		return apiErr(400)
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("400 should not be retried; calls = %d", calls)
	}
}

func TestNoRetry(t *testing.T) {
	calls := 0
	err := NoRetry().do(context.Background(), func() error {
		calls++
		return apiErr(503)
	})
	if err == nil || calls != 1 {
		t.Errorf("NoRetry should try exactly once; calls = %d err = %v", calls, err)
	}
}

func TestRetryDo_AbortsOnContextCancel(t *testing.T) {
	rc := RetryConfig{MaxAttempts: 5, InitialBackoff: time.Hour, MaxBackoff: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- rc.do(ctx, func() error {
			calls++
			cancel() // cancel while the first backoff is pending
			return apiErr(503)
		})
	}()
	select {
	case err := <-done:
		// The upstream failure is surfaced, not ctx.Err() — the caller wants to
		// know why the request failed.
		if !IsServerError(err) {
			t.Errorf("expected the upstream 503, got %v", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("do did not abort on context cancellation")
	}
}

func TestRetryDo_CustomShouldRetry(t *testing.T) {
	sentinel := errors.New("retry me")
	rc := RetryConfig{
		MaxAttempts:    3,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
		ShouldRetry:    func(err error) bool { return errors.Is(err, sentinel) },
	}
	calls := 0
	err := rc.do(context.Background(), func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) || calls != 3 {
		t.Errorf("custom ShouldRetry not honored: calls = %d err = %v", calls, err)
	}
}

func TestBackoff_RespectsRetryAfterAndCap(t *testing.T) {
	rc := DefaultRetry()
	rc.Jitter = 0 // deterministic
	rng := newTestRand()

	// Retry-After wins over the computed backoff.
	err := &APIError{StatusCode: 429, RetryAfter: "3"}
	if got := rc.backoff(1, err, rng); got != 3*time.Second {
		t.Errorf("backoff with Retry-After = %v, want 3s", got)
	}

	// ...but is still capped, so a hostile header can't park the caller.
	long := &APIError{StatusCode: 429, RetryAfter: "99999"}
	if got := rc.backoff(1, long, rng); got != rc.MaxBackoff {
		t.Errorf("oversized Retry-After = %v, want cap %v", got, rc.MaxBackoff)
	}

	// Exponential growth, capped at MaxBackoff.
	plain := apiErr(500)
	first := rc.backoff(1, plain, rng)
	second := rc.backoff(2, plain, rng)
	if second <= first {
		t.Errorf("backoff should grow: %v then %v", first, second)
	}
	if got := rc.backoff(20, plain, rng); got != rc.MaxBackoff {
		t.Errorf("backoff(20) = %v, want cap %v", got, rc.MaxBackoff)
	}
}

func TestBackoff_JitterStaysInBand(t *testing.T) {
	rc := DefaultRetry()
	rng := newTestRand()
	base := rc.InitialBackoff
	lo := time.Duration(float64(base) * (1 - rc.Jitter))
	hi := time.Duration(float64(base) * (1 + rc.Jitter))
	for range 200 {
		got := rc.backoff(1, apiErr(500), rng)
		if got < lo || got > hi {
			t.Fatalf("jittered backoff %v outside [%v, %v]", got, lo, hi)
		}
	}
}

func TestIsSafeToReplay(t *testing.T) {
	dialErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	readErr := &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		// Safe: the vendor demonstrably never started work.
		{"rate limited", apiErr(http.StatusTooManyRequests), true},
		{"dial failure", dialErr, true},
		{"wrapped dial failure", fmt.Errorf("post: %w", dialErr), true},
		{"dns failure", &net.DNSError{Err: "no such host"}, true},

		// Unsafe: the request may have been accepted and billed before the
		// failure, so a replay could produce a second charge.
		{"server error", apiErr(http.StatusInternalServerError), false},
		{"bad gateway", apiErr(http.StatusBadGateway), false},
		{"service unavailable", apiErr(http.StatusServiceUnavailable), false},
		{"anthropic overloaded", apiErr(529), false},
		{"request timeout", apiErr(http.StatusRequestTimeout), false},
		{"reset mid-response", readErr, false},

		// Not retryable at all, so not replay-safe either.
		{"nil", nil, false},
		{"bad request", apiErr(http.StatusBadRequest), false},
		{"unauthorized", apiErr(http.StatusUnauthorized), false},
		{"context canceled", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSafeToReplay(tc.err); got != tc.want {
				t.Errorf("IsSafeToReplay(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The two classifications are orthogonal, and the narrowed policy ANDs them.
// Pinning all four quadrants documents why neither one alone is enough.
func TestIsSafeToReplay_OrthogonalToIsRetryable(t *testing.T) {
	cases := []struct {
		name              string
		err               error
		retryable, replay bool
	}{
		{"429: both, so it replays", apiErr(http.StatusTooManyRequests), true, true},
		{"dns timeout: nothing resolved, so nothing was sent",
			&net.DNSError{Err: "timeout", IsTimeout: true}, true, true},
		{"5xx: worth retrying, but may already be billed", apiErr(http.StatusInternalServerError), true, false},
		{"read timeout: the request was already on the wire",
			&net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, true, false},
		{"refused: nothing billed, but retrying won't help",
			&net.OpError{Op: "dial", Err: errors.New("refused")}, false, true},
		{"400: neither", apiErr(http.StatusBadRequest), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRetryable(tc.err); got != tc.retryable {
				t.Errorf("IsRetryable = %v, want %v", got, tc.retryable)
			}
			if got := IsSafeToReplay(tc.err); got != tc.replay {
				t.Errorf("IsSafeToReplay = %v, want %v", got, tc.replay)
			}
			want := tc.retryable && tc.replay
			if got := DefaultRetry().replaySafeOnly().shouldRetry(tc.err); got != want {
				t.Errorf("narrowed shouldRetry = %v, want %v", got, want)
			}
		})
	}
}

func TestReplaySafeOnly_NarrowsButKeepsShape(t *testing.T) {
	base := DefaultRetry()
	narrowed := base.replaySafeOnly()

	if narrowed.MaxAttempts != base.MaxAttempts || narrowed.InitialBackoff != base.InitialBackoff {
		t.Errorf("replaySafeOnly changed backoff shape: %+v vs %+v", narrowed, base)
	}
	if narrowed.shouldRetry(apiErr(http.StatusInternalServerError)) {
		t.Error("narrowed policy retries a 5xx")
	}
	if !narrowed.shouldRetry(apiErr(http.StatusTooManyRequests)) {
		t.Error("narrowed policy refuses a 429")
	}
}

// A caller-supplied ShouldRetry keeps its veto: narrowing intersects with it
// rather than replacing it.
func TestReplaySafeOnly_RespectsCustomShouldRetry(t *testing.T) {
	rc := DefaultRetry()
	rc.ShouldRetry = func(error) bool { return false }

	if rc.replaySafeOnly().shouldRetry(apiErr(http.StatusTooManyRequests)) {
		t.Error("narrowed policy overrode a custom ShouldRetry that said no")
	}
}

// The default Client must not replay a billable creation call on a 5xx.
func TestDefaultMediaRetryPolicy_IsReplaySafe(t *testing.T) {
	cfg := clientConfig{retry: DefaultRetry()}
	if cfg.mediaRetryPolicy().shouldRetry(apiErr(http.StatusInternalServerError)) {
		t.Error("default media policy retries a 5xx — a lost response would bill twice")
	}
}
