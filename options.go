package llmkit

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/siguago/llmkit/provider"
)

// Option configures a Client. Pass options to New.
type Option func(*clientConfig)

type clientConfig struct {
	apiKey  string
	baseURL string
	timeout time.Duration
	retry   RetryConfig
	// mediaRetry is nil unless WithMediaRetry was passed; nil means "derive the
	// replay-safe policy from retry". Storing the caller's intent rather than a
	// resolved value keeps WithRetry and WithMediaRetry order-independent.
	mediaRetry   *RetryConfig
	headers      map[string]string
	transport    http.RoundTripper
	logger       *slog.Logger
	requestIDFn  func() string
	requestIDHdr string
	streamPolicy provider.StreamPolicy
	// noAPIKey records that the caller declared this endpoint unauthenticated,
	// which is what separates "no credential needed" from "the credential went
	// missing" — the latter must stay an error.
	noAPIKey bool
}

// mediaRetryPolicy returns the retry policy for billable creation calls.
func (c clientConfig) mediaRetryPolicy() RetryConfig {
	if c.mediaRetry != nil {
		return *c.mediaRetry
	}
	return c.creationRetryPolicy()
}

// creationRetryPolicy narrows the general retry policy for billable creates
// that have no idempotency key. A failure after the request may have reached
// the model is surfaced instead of risking duplicate generation and billing.
func (c clientConfig) creationRetryPolicy() RetryConfig {
	return c.retry.replaySafeOnly()
}

// WithAPIKey sets the upstream credential. Required unless the provider's
// environment variable is set (see New).
func WithAPIKey(key string) Option {
	return func(c *clientConfig) { c.apiKey = key }
}

// WithoutAPIKey declares that the endpoint needs no credential, so New and Wrap
// accept an empty key instead of returning ErrNoAPIKey, and no credential header
// is sent on any route — not Authorization, and not a vendor-specific header like
// Anthropic's x-api-key (see provider.SetBearer).
// It suppresses both environment credentials and WithAPIKey, regardless of
// option order; do not combine them unless suppression is intentionally the
// final policy. Because that overrides the usual last-option-wins rule,
// suppressing an explicit WithAPIKey is reported through WithLogger — silently
// dropping a credential the caller just passed is too easy to misread.
//
// Use it for an unauthenticated endpoint you host yourself — a local runtime, an
// internal gateway that authenticates by network position. The local-runtime
// providers (Ollama, vLLM) already default to this, so passing it there is
// redundant; it exists for Wrap and for custom base URLs.
//
// It is deliberately explicit rather than inferred from an empty key: a
// credential that silently went missing (unset environment variable, typo in a
// config key) would otherwise turn into unauthenticated requests to a real
// vendor and a confusing 401 far from its cause.
func WithoutAPIKey() Option {
	return func(c *clientConfig) { c.noAPIKey = true }
}

// WithBaseURL points the client at a custom API root — a relay, a corporate
// proxy, a regional endpoint, or a test server. Pass the API base without a
// trailing slash, e.g. "https://api.deepseek.com/v1".
//
// Each provider documents the shape it expects on its NewWithBaseURL; the
// common case is the same URL you would give any OpenAI-compatible client.
//
// DashScope and Volcengine are the exceptions, because one adapter serves two
// unrelated upstream APIs: pass the host root, not an OpenAI base. The adapter
// appends the vendor's compat path itself (DashScope's /compatible-mode/v1;
// Volcengine's /api/v3 already is one) and keeps the native video endpoints on
// the root. A DashScope base that already contains /compatible-mode is passed
// through rather than doubled, so either form of the vendor's own URL works — but
// a plain OpenAI-compatible relay URL is not a valid DashScope base.
func WithBaseURL(url string) Option {
	return func(c *clientConfig) { c.baseURL = url }
}

// WithTimeout bounds each individual request, including all retries of it.
// Zero (the default) leaves the provider's own generous ceiling in place:
// 300s for regular calls, 900s for streams, which suits long image and
// reasoning generations.
//
// The deadline applies per Client call. A Chat that retries twice shares one
// budget across all three attempts.
func WithTimeout(d time.Duration) Option {
	return func(c *clientConfig) { c.timeout = d }
}

// WithRetry replaces the retry policy. Use NoRetry() to disable retries, or
// build a RetryConfig for custom backoff.
//
// This does not fully govern GenerateImage and CreateVideo, which narrow it for
// safety — see WithMediaRetry.
func WithRetry(rc RetryConfig) Option {
	return func(c *clientConfig) { c.retry = rc }
}

// WithMediaRetry replaces the retry policy for the two billable, non-idempotent
// creation calls: GenerateImage and CreateVideo.
//
// By default those two do not use the general policy as-is. They narrow it with
// IsSafeToReplay, retrying only when the error proves the vendor never took the
// work — so a response lost in transit never becomes a second image, a second
// video job, and a second charge. Everything else surfaces to you on the first
// failure.
//
// Pass this option to make that decision yourself:
//
//	WithMediaRetry(llmkit.NoRetry())      // never retry media creation at all
//	WithMediaRetry(llmkit.DefaultRetry()) // retry like everything else — can double-charge
func WithMediaRetry(rc RetryConfig) Option {
	return func(c *clientConfig) { c.mediaRetry = &rc }
}

// WithHeader adds an HTTP header to every request this client makes. Useful for
// vendor attribution headers (OpenRouter's HTTP-Referer / X-Title), tenant
// routing on a relay, or tracing IDs.
//
// Authorization is managed by the provider adapter and cannot be overridden
// here; use WithAPIKey instead.
func WithHeader(name, value string) Option {
	return func(c *clientConfig) {
		if c.headers == nil {
			c.headers = make(map[string]string)
		}
		c.headers[name] = value
	}
}

// WithHeaders adds several headers at once. See WithHeader.
func WithHeaders(h map[string]string) Option {
	return func(c *clientConfig) {
		if c.headers == nil {
			c.headers = make(map[string]string, len(h))
		}
		for k, v := range h {
			c.headers[k] = v
		}
	}
}

// WithTransport replaces the RoundTripper the client dials through — for a
// custom TLS config, a tuned connection pool, a corporate proxy the environment
// variables don't cover, or an instrumenting wrapper (tracing, metrics, VCR-style
// recording in tests).
//
// Your transport sits at the bottom of the chain: the SDK still adds credential
// and caller-supplied headers, and still strips client-IP forwarding headers,
// before your RoundTrip sees the request. That ordering is deliberate — a
// transport installed for observability must not be able to silently undo the
// privacy guarantee.
//
// Note that http.Client.Timeout has no equivalent here. Per-call deadlines are
// WithTimeout's job, which applies one budget across a call and all its retries;
// a transport-level timeout would cut each attempt separately and interact badly
// with the retry policy.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *clientConfig) { c.transport = rt }
}

// WithLogger routes the SDK's non-fatal diagnostics to logger.
//
// The default discards everything. A library that writes to the process-global
// logger is making a decision that belongs to the program embedding it, so
// nothing is emitted unless you ask. What you get when you do ask: vendor
// responses the adapters had to work around — a chunk that failed to parse but
// did not break the stream, a tool definition dropped for being malformed.
// Anything that actually breaks a call comes back as an error instead.
func WithLogger(logger *slog.Logger) Option {
	return func(c *clientConfig) { c.logger = logger }
}

// WithRequestID sets a per-request identifier, generated by calling fn once per
// upstream request, and sent as the X-Request-Id header.
//
// This is for correlating your logs with a vendor's. It is separate from the ID
// the vendor generates: that one comes back on APIError.RequestID and is what
// their support will ask for. Sending your own additionally lets you find the
// request from your side when the vendor's ID was never received — which is
// exactly the case (a timeout, a dropped response) where you most need it.
//
// fn is called once per outbound HTTP request, not once per Client call: each
// attempt of a retried call gets its own ID, as does each follow-up request an
// adapter makes internally. It may be called concurrently, so it must be safe
// for concurrent use. Returning "" skips the header for that request. Pass a nil
// fn to disable.
func WithRequestID(fn func() string) Option {
	return func(c *clientConfig) { c.requestIDFn = fn }
}

// WithRequestIDHeader overrides the header name WithRequestID writes to, for a
// relay or gateway that expects something other than X-Request-Id.
func WithRequestIDHeader(name string) Option {
	return func(c *clientConfig) { c.requestIDHdr = name }
}

// StreamTolerance decides what a stream does with a frame it cannot parse.
type StreamTolerance = provider.StreamTolerance

const (
	// StrictStream fails the stream on the first unparseable frame. This is the
	// default: skipping a frame is silent data loss, and a truncated answer that
	// looks complete is worse than an error you can retry or fall back from.
	StrictStream = provider.StreamStrict

	// TolerateMalformedChunks skips unparseable frames and reports each one to
	// the logger set by WithLogger. Use it when partial output beats no output,
	// or to ride out a vendor intermittently emitting garbage frames.
	TolerateMalformedChunks = provider.StreamTolerateMalformed
)

// DefaultMaxFrameBytes is the default ceiling on a single SSE frame.
const DefaultMaxFrameBytes = provider.DefaultMaxFrameBytes

// WithStreamTolerance sets what streams do with frames they cannot parse.
//
// Before this was configurable the readers always skipped such frames and wrote
// a warning to the global logger, which meant a caller could receive a silently
// incomplete response — a missing tool call, a truncated answer — with no signal
// that anything went wrong. Strict is now the default; this option restores the
// old behavior when you want it.
func WithStreamTolerance(t StreamTolerance) Option {
	return func(c *clientConfig) { c.streamPolicy.Tolerance = t }
}

// WithMaxStreamFrameBytes raises or lowers the ceiling on a single SSE frame.
//
// The default is DefaultMaxFrameBytes (1 MiB), which covers ordinary deltas with
// room for the one legitimately large case: a tool call whose arguments arrive
// in a single frame. Raise it if you drive models that emit very large tool
// arguments or inline image data; a frame over the limit fails the stream with
// an error naming this option. Zero restores the default.
func WithMaxStreamFrameBytes(n int) Option {
	return func(c *clientConfig) { c.streamPolicy.MaxFrameBytes = n }
}
