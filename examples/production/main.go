// Command production assembles a client the way a long-running service should:
// an explicit timeout budget, a retry policy that treats billable calls
// differently, an instrumented transport, structured logs, per-request IDs,
// and error handling that captures what vendor support will ask for.
//
//	DEEPSEEK_API_KEY=sk-... go run ./examples/production
//
// Standard library only. llmkit takes no third-party dependencies and neither
// does this example — substitute your own UUID library for newRequestID and
// your real telemetry client for metrics.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/siguago/llmkit"
)

const (
	serviceName  = "my-service"
	providerName = llmkit.DeepSeek
	model        = "deepseek-chat"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := &metrics{}

	client, err := newClient(providerName, logger, m)
	if err != nil {
		log.Fatal(err)
	}

	// One context per unit of work. Cancelling it aborts the call and any
	// backoff wait still pending, so a shutting-down service doesn't sit out a
	// 20s retry sleep for a request nobody is waiting for.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	answer, err := client.Say(ctx, model, "用一句话解释 CAP 定理。")
	if err != nil {
		logger.Error("chat failed", report(err)...)
		os.Exit(1)
	}
	fmt.Println(answer)

	// The failure path, without waiting for a real outage: a deliberately bad
	// key returns 401 on the first attempt, and 401 is never retried.
	bad, err := newClient(providerName, logger, m, llmkit.WithAPIKey("sk-deliberately-invalid"))
	if err != nil {
		log.Fatal(err)
	}
	if _, err := bad.Say(ctx, model, "ping"); err != nil {
		logger.Warn("expected failure", report(err)...)
	}

	m.report(logger)
}

// newClient is the part worth copying. Every option below earns its place; the
// comments say what goes wrong without it. Trailing overrides let callers vary
// one setting without restating the rest.
func newClient(name string, logger *slog.Logger, m *metrics, overrides ...llmkit.Option) (*llmkit.Client, error) {
	opts := []llmkit.Option{
		// Credential. Omitting this option falls back to the provider's own env
		// var, which is fine for a CLI; a service usually wants the source to be
		// explicit and auditable. Passing "" also falls back, so wiring this to
		// config unconditionally is safe.
		llmkit.WithAPIKey(os.Getenv(llmkit.EnvVar(name))),

		// Relay, regional endpoint, or private deployment. "" keeps the vendor
		// default, so this too is safe to wire unconditionally. The variable
		// name is this example's own — the SDK reads no base-URL env var.
		llmkit.WithBaseURL(os.Getenv("LLM_BASE_URL")),

		// One deadline for the whole call, retries included — not per attempt.
		// Leave it off and you inherit the provider ceiling (300s, 900s for
		// streams), generous on purpose but rarely what a request-scoped service
		// wants. Size it above your slowest realistic generation: a reasoning
		// model can think for a minute before the first byte.
		llmkit.WithTimeout(90 * time.Second),

		// Ordinary calls: 429, 5xx, network timeouts. More attempts and more
		// jitter than the default, because a background service can afford to
		// wait where an interactive tool cannot, and because many instances
		// hitting one rate limit need to spread out rather than resynchronize.
		llmkit.WithRetry(llmkit.RetryConfig{
			MaxAttempts:       4,
			InitialBackoff:    500 * time.Millisecond,
			MaxBackoff:        20 * time.Second,
			Multiplier:        2.0,
			Jitter:            0.3,
			RespectRetryAfter: true,
		}),

		// GenerateImage and CreateVideo bill on success and have no idempotency
		// key, so they never inherit the policy above as-is: by default they
		// narrow it to errors proving the vendor never took the work. This goes
		// one step further and disables their retries entirely — with money
		// involved, surfacing the failure beats guessing. Drop this option to
		// keep the safe-replay default; pass DefaultRetry() to accept the risk
		// of paying twice.
		llmkit.WithMediaRetry(llmkit.NoRetry()),

		llmkit.WithTransport(newTransport(m)),

		// The SDK is silent unless you ask. What you get: vendor responses the
		// adapters worked around — a chunk that failed to parse without breaking
		// the stream, a tool definition dropped for being malformed. Anything
		// that actually breaks a call comes back as an error instead, so this is
		// strictly extra visibility, never the only signal.
		llmkit.WithLogger(logger),

		llmkit.WithRequestID(newRequestID),

		// Attribution headers. OpenRouter surfaces these on its leaderboards;
		// relays often route on them. Credential headers cannot be overridden
		// here, so this can never send the wrong key by accident.
		llmkit.WithHeaders(map[string]string{
			"X-Title":      serviceName,
			"HTTP-Referer": "https://example.com",
		}),

		// Strict is the default and worth keeping: a skipped frame yields a
		// reply that looks complete while having silently lost a sentence or a
		// tool call. Failing lets you retry or fall back.
		llmkit.WithStreamTolerance(llmkit.StrictStream),

		// Raise the per-frame ceiling (default 1 MiB) if your models emit very
		// large tool arguments or inline image data in a single frame.
		llmkit.WithMaxStreamFrameBytes(4 << 20),
	}

	return llmkit.New(name, append(opts, overrides...)...)
}

// newTransport returns the SDK's default transport tuning plus timing and
// status instrumentation.
//
// Two things to know about WithTransport:
//
// It REPLACES the SDK's transport, which sets Proxy: http.ProxyFromEnvironment
// for you. A hand-built &http.Transport{} does not — omit that line and
// HTTPS_PROXY silently stops working, a miserable thing to debug in an
// environment that only reaches the vendor through a proxy.
//
// It sits at the BOTTOM of the chain. Credential headers are already on the
// request and client-IP headers are already stripped by the time RoundTrip
// runs, so instrumentation cannot bypass either.
func newTransport(m *metrics) http.RoundTripper {
	return &instrumented{
		metrics: m,
		base: &http.Transport{
			Proxy: http.ProxyFromEnvironment, // do not omit — see above
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   20, // raise for heavy concurrency against one vendor
			IdleConnTimeout:       120 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			// Deliberately no ResponseHeaderTimeout: a reasoning model can take
			// minutes to produce its first byte, and cutting that off here would
			// fight the per-call budget WithTimeout owns.
		},
	}
}

type instrumented struct {
	base    http.RoundTripper
	metrics *metrics
}

func (t *instrumented) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(req)

	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	t.metrics.observe(req.Host, status, err, time.Since(start))

	return resp, err
}

// metrics stands in for whatever telemetry client you actually use.
//
// Mind what a RoundTripper can and cannot see. The duration below is time to
// response HEADERS, not to the last byte: a streaming call returns here in
// milliseconds and then streams for a minute. Measure end-to-end latency around
// the llmkit call instead. Each observation is one HTTP attempt, so a call that
// retried twice lands here three times — which is exactly what you want for a
// retry-rate metric, and wrong for a request-rate one.
type metrics struct {
	attempts atomic.Int64
	failures atomic.Int64
	nanos    atomic.Int64
}

func (m *metrics) observe(host string, status int, err error, d time.Duration) {
	m.attempts.Add(1)
	m.nanos.Add(int64(d))
	if err != nil || status >= 400 {
		m.failures.Add(1)
		return
	}
	_ = host // your telemetry client would use this as a label
}

func (m *metrics) report(logger *slog.Logger) {
	n := m.attempts.Load()
	if n == 0 {
		return
	}
	logger.Info("http attempts",
		"count", n,
		"failures", m.failures.Load(),
		"avg_to_headers", time.Duration(m.nanos.Load()/n).Round(time.Millisecond),
	)
}

// newRequestID generates the value sent as X-Request-Id — once per outbound
// HTTP request, so every retry attempt gets its own. It must be safe for
// concurrent use; crypto/rand is.
//
// The point of sending your own is that the ID exists on YOUR side even when
// the response never arrives, which is precisely the case (a timeout, a dropped
// connection) where the vendor's own ID never reaches you.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "" // returning "" just omits the header for this request
	}
	return serviceName + "-" + hex.EncodeToString(b[:])
}

// report turns an error into log attributes worth persisting. The key one is
// the vendor's request ID: it is the first thing their support asks for, and it
// is gone the moment the response is discarded — so capture it at the failure
// site, not when you later notice a pattern in your error rate.
func report(err error) []any {
	attrs := []any{"err", err, "action", action(err)}

	var apiErr *llmkit.APIError
	if errors.As(err, &apiErr) && apiErr.RequestID != "" {
		attrs = append(attrs, "vendor_request_id", apiErr.RequestID)
	}
	if code := llmkit.ProviderCode(err); code != "" {
		attrs = append(attrs, "vendor_error_code", code)
	}
	if category := llmkit.ErrorCategoryOf(err); category != "" {
		attrs = append(attrs, "error_category", category)
	}
	if code := llmkit.StatusCode(err); code != 0 {
		attrs = append(attrs, "status", code)
	}
	if d := llmkit.RetryAfter(err); d > 0 {
		attrs = append(attrs, "retry_after", d)
	}
	return attrs
}

// action is the decision the caller actually has to make. Note that by the time
// an error reaches you the retry policy has already given up, so "retryable"
// here means retry at a different layer — a queue, a fallback provider — not
// immediately.
func action(err error) string {
	switch {
	case llmkit.IsAuthError(err):
		return "fix credentials; never retried"
	case llmkit.IsRateLimited(err):
		return "shed load or fail over; retries already exhausted"
	case llmkit.IsNotFound(err):
		return "model name is wrong for this provider"
	case llmkit.IsInvalidRequest(err):
		return "fix the request; retrying cannot help"
	case llmkit.IsServerError(err):
		return "vendor-side; safe to queue for later"
	case llmkit.IsUnsupportedCapability(err):
		return "provider lacks this capability; probe with Supports* first"
	case errors.Is(err, context.DeadlineExceeded):
		return "raise WithTimeout or shorten the prompt"
	case errors.Is(err, context.Canceled):
		return "caller went away; nothing to do"
	default:
		return "network or transport failure"
	}
}
