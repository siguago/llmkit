package llmkit

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

// RetryConfig controls automatic retries. The zero value means no retries;
// DefaultRetry() is what a Client uses unless you pass WithRetry.
type RetryConfig struct {
	// MaxAttempts is the total number of tries including the first one.
	// 0 or 1 disables retrying.
	MaxAttempts int
	// InitialBackoff is the wait before the second attempt.
	InitialBackoff time.Duration
	// MaxBackoff caps the wait between attempts.
	MaxBackoff time.Duration
	// Multiplier grows the backoff each attempt (2.0 = double).
	Multiplier float64
	// Jitter randomizes each wait by ±Jitter (0.2 = ±20%) so concurrent
	// clients that hit the same rate limit don't retry in lockstep.
	Jitter float64
	// RespectRetryAfter honors the upstream's Retry-After header when present,
	// in place of the computed backoff.
	RespectRetryAfter bool
	// ShouldRetry decides whether an error is worth another attempt.
	// nil means IsRetryable.
	ShouldRetry func(error) bool
}

// DefaultRetry returns the policy a Client uses when none is configured:
// 3 attempts, 500ms → 1s → capped at 30s, ±20% jitter, honoring Retry-After.
func DefaultRetry() RetryConfig {
	return RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    500 * time.Millisecond,
		MaxBackoff:        30 * time.Second,
		Multiplier:        2.0,
		Jitter:            0.2,
		RespectRetryAfter: true,
	}
}

// NoRetry disables automatic retries.
func NoRetry() RetryConfig { return RetryConfig{MaxAttempts: 1} }

func (rc RetryConfig) attempts() int {
	if rc.MaxAttempts < 1 {
		return 1
	}
	return rc.MaxAttempts
}

func (rc RetryConfig) shouldRetry(err error) bool {
	if rc.ShouldRetry != nil {
		return rc.ShouldRetry(err)
	}
	return IsRetryable(err)
}

// backoff computes the wait before attempt n (1-based: n=1 is the wait after
// the first failure). The upstream's Retry-After hint wins when present and
// RespectRetryAfter is set, capped by MaxBackoff so a hostile or mistaken
// header can't park the caller for an hour.
func (rc RetryConfig) backoff(n int, err error, rng *rand.Rand) time.Duration {
	maxBackoff := rc.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}

	if rc.RespectRetryAfter {
		if d := RetryAfter(err); d > 0 {
			return min(d, maxBackoff)
		}
	}

	base := rc.InitialBackoff
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	mult := rc.Multiplier
	if mult <= 0 {
		mult = 2.0
	}

	d := float64(base) * math.Pow(mult, float64(n-1))
	if d > float64(maxBackoff) {
		d = float64(maxBackoff)
	}
	if rc.Jitter > 0 {
		// Symmetric jitter in [1-j, 1+j].
		factor := 1 + rc.Jitter*(2*rng.Float64()-1)
		d *= factor
	}
	if d < 0 {
		return 0
	}
	return time.Duration(d)
}

// do runs fn with retries. It returns the last error when every attempt fails.
//
// The caller's context governs the waits too: a cancelled context aborts the
// sleep immediately rather than finishing a 30s backoff on a request nobody is
// waiting for anymore.
func (rc RetryConfig) do(ctx context.Context, fn func() error) error {
	attempts := rc.attempts()
	// Seeded per call rather than from a shared source: the values only need to
	// decorrelate concurrent retriers, and a local generator avoids contention.
	rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))

	var err error
	for attempt := 1; ; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		if attempt >= attempts || !rc.shouldRetry(err) {
			return err
		}
		wait := rc.backoff(attempt, err, rng)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			// Surface the upstream failure, not the context error: the caller
			// wants to know why the request failed, and ctx.Err() is available
			// to them directly.
			return err
		case <-timer.C:
		}
	}
}

// doValue is do() for a function that also produces a value.
func doValue[T any](ctx context.Context, rc RetryConfig, fn func() (T, error)) (T, error) {
	var out T
	err := rc.do(ctx, func() error {
		v, err := fn()
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}
