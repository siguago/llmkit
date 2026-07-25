package llmkit

import "time"

// Option configures a Client. Pass options to New.
type Option func(*clientConfig)

type clientConfig struct {
	apiKey  string
	baseURL string
	timeout time.Duration
	retry   RetryConfig
	headers map[string]string
}

// WithAPIKey sets the upstream credential. Required unless the provider's
// environment variable is set (see New).
func WithAPIKey(key string) Option {
	return func(c *clientConfig) { c.apiKey = key }
}

// WithBaseURL points the client at a custom API root — a relay, a corporate
// proxy, a regional endpoint, or a test server. Pass the API base without a
// trailing slash, e.g. "https://api.deepseek.com/v1".
//
// Each provider documents the shape it expects on its NewWithBaseURL; the
// common case is the same URL you would give any OpenAI-compatible client.
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
func WithRetry(rc RetryConfig) Option {
	return func(c *clientConfig) { c.retry = rc }
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
