package llmkit

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
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
