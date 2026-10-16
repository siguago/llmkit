package llmkit

import (
	"math/rand/v2"
	"net/http"
	"testing"
	"time"
)

// This benchmark is a *baseline*, not a gate: nothing in CI fails on it.
//
// backoff is the only pure computation on the retry path — everything else
// (the attempt loop, the timer) is dominated by runtime scheduling. It runs
// once per failed attempt per request, so it is worth knowing what it costs,
// and it is the piece most likely to grow accidentally (a time.Parse, a
// formatter, a log line).
//
// Recorded values live in docs/benchmarks.md.
//
// Reproduce with:
//
//	go test . -run '^$' -bench . -benchmem

// benchBackoffSink keeps the computed wait observable. Without a package-level
// sink the compiler is free to drop the call entirely, and the benchmark would
// report near-zero cost.
var benchBackoffSink time.Duration

func BenchmarkRetryConfigBackoff(b *testing.B) {
	// The HTTP-date form is accepted by Retry-After, and parsing it needs a
	// clock read on every call — which the delta-seconds path also pays, since
	// RetryAfter passes time.Now() before it knows which form it has.
	// Keep the fixture valid across calibration and earlier sub-benchmarks.
	// A short deadline would silently switch this case to computed backoff.
	httpDate := time.Now().Add(24 * time.Hour).UTC().Format(http.TimeFormat)

	cases := []struct {
		name    string
		config  RetryConfig
		err     error
		attempt int
	}{
		{
			name:    "computed/attempt-3",
			config:  DefaultRetry(),
			err:     &APIError{StatusCode: http.StatusTooManyRequests},
			attempt: 3,
		},
		{
			name:    "computed/capped-at-max",
			config:  DefaultRetry(),
			err:     &APIError{StatusCode: http.StatusTooManyRequests},
			attempt: 24,
		},
		{
			name:    "retry-after/delta-seconds",
			config:  DefaultRetry(),
			err:     &APIError{StatusCode: http.StatusTooManyRequests, RetryAfter: "7"},
			attempt: 3,
		},
		{
			name:    "retry-after/http-date",
			config:  DefaultRetry(),
			err:     &APIError{StatusCode: http.StatusTooManyRequests, RetryAfter: httpDate},
			attempt: 3,
		},
		{
			name: "retry-after/not-respected",
			config: RetryConfig{
				MaxAttempts:    3,
				InitialBackoff: 500 * time.Millisecond,
				MaxBackoff:     30 * time.Second,
				Multiplier:     2,
				Jitter:         0.2,
			},
			err:     &APIError{StatusCode: http.StatusTooManyRequests, RetryAfter: "7"},
			attempt: 3,
		},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			// Seeded per sub-benchmark rather than shared: backoff mutates the
			// generator, and a jitter draw is part of the measured work.
			rng := rand.New(rand.NewPCG(1, 2))
			b.ReportAllocs()
			b.ResetTimer()
			var wait time.Duration
			for i := 0; i < b.N; i++ {
				wait = tc.config.backoff(tc.attempt, tc.err, rng)
			}
			b.StopTimer()
			benchBackoffSink = wait
			if wait < 0 {
				b.Fatalf("backoff = %v, want non-negative", wait)
			}
			if tc.name == "retry-after/http-date" && wait != tc.config.MaxBackoff {
				b.Fatalf("HTTP-date backoff = %v, want %v; fixture may have expired", wait, tc.config.MaxBackoff)
			}
		})
	}
}
