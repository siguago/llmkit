package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// capturingTransport records the headers of every request it sees and returns a
// canned 200 without touching the network.
type capturingTransport struct {
	mu    sync.Mutex
	seen  []http.Header
	calls atomic.Int32
}

func (c *capturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	c.mu.Lock()
	c.seen = append(c.seen, req.Header.Clone())
	c.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func (c *capturingTransport) last() http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen) == 0 {
		return nil
	}
	return c.seen[len(c.seen)-1]
}

func doWith(t *testing.T, ctx context.Context, rt http.RoundTripper) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.invalid/v1/chat", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
}

func TestWithBaseTransport_Dispatches(t *testing.T) {
	custom := &capturingTransport{}
	ctx := WithBaseTransport(context.Background(), custom)
	doWith(t, ctx, NewOutbound())

	if got := custom.calls.Load(); got != 1 {
		t.Errorf("custom transport calls = %d, want 1", got)
	}
}

func TestWithBaseTransport_NilIsIgnored(t *testing.T) {
	ctx := WithBaseTransport(context.Background(), nil)
	if baseTransport(ctx) != nil {
		t.Error("a nil transport should not be stored")
	}
}

func TestBaseTransport_NilContext(t *testing.T) {
	if baseTransport(nil) != nil { //nolint:staticcheck // exercising the nil guard
		t.Error("baseTransport(nil) should be nil")
	}
}

// Without an installed transport the chain must fall through to the tuned
// default rather than nil-panicking.
func TestSwitchableTransport_FallsBackToDefault(t *testing.T) {
	fallback := &capturingTransport{}
	s := switchableTransport{fallback: fallback}

	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid/", nil)
	resp, err := s.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if fallback.calls.Load() != 1 {
		t.Error("fallback transport was not used")
	}
}

func TestRequestIDFunc_StampsHeader(t *testing.T) {
	custom := &capturingTransport{}
	ctx := WithBaseTransport(context.Background(), custom)
	ctx = WithRequestIDFunc(ctx, func() string { return "id-1" }, "")
	doWith(t, ctx, NewOutbound())

	if got := custom.last().Get("X-Request-Id"); got != "id-1" {
		t.Errorf("X-Request-Id = %q, want id-1 (empty header name should default)", got)
	}
}

func TestRequestIDFunc_CustomHeaderName(t *testing.T) {
	custom := &capturingTransport{}
	ctx := WithBaseTransport(context.Background(), custom)
	ctx = WithRequestIDFunc(ctx, func() string { return "abc" }, "X-Trace")
	doWith(t, ctx, NewOutbound())

	if got := custom.last().Get("X-Trace"); got != "abc" {
		t.Errorf("X-Trace = %q, want abc", got)
	}
}

func TestRequestIDFunc_RegeneratedPerRequest(t *testing.T) {
	custom := &capturingTransport{}
	var n atomic.Int32
	ctx := WithBaseTransport(context.Background(), custom)
	ctx = WithRequestIDFunc(ctx, func() string {
		return "id-" + string(rune('a'+n.Add(1)-1))
	}, "")

	rt := NewOutbound()
	doWith(t, ctx, rt)
	first := custom.last().Get("X-Request-Id")
	doWith(t, ctx, rt)
	second := custom.last().Get("X-Request-Id")

	if first == second {
		t.Errorf("both requests carried %q; the ID must be regenerated per request", first)
	}
}

func TestRequestIDFunc_EmptyValueWritesNoHeader(t *testing.T) {
	custom := &capturingTransport{}
	ctx := WithBaseTransport(context.Background(), custom)
	ctx = WithRequestIDFunc(ctx, func() string { return "" }, "")
	doWith(t, ctx, NewOutbound())

	if _, present := custom.last()["X-Request-Id"]; present {
		t.Error("an empty generated ID must not produce a header")
	}
}

func TestRequestIDFunc_NilIsIgnored(t *testing.T) {
	ctx := WithRequestIDFunc(context.Background(), nil, "X-Anything")
	if hasRequestID(ctx) {
		t.Error("a nil generator should not register")
	}
}

func TestHasRequestID_NilContext(t *testing.T) {
	if hasRequestID(nil) { //nolint:staticcheck // exercising the nil guard
		t.Error("hasRequestID(nil) should be false")
	}
}

// The credential headers are protected from extras; a request-ID generator must
// not become a way around that.
func TestRequestIDFunc_CannotOverwriteCredentialHeader(t *testing.T) {
	custom := &capturingTransport{}
	ctx := WithBaseTransport(context.Background(), custom)
	ctx = WithRequestIDFunc(ctx, func() string { return "sk-attacker" }, "Authorization")

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.invalid/", nil)
	req.Header.Set("Authorization", "Bearer sk-real")
	resp, err := NewOutbound().RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	if got := custom.last().Get("Authorization"); got != "Bearer sk-real" {
		t.Errorf("Authorization = %q, want the original credential", got)
	}
}

// The original request handed to RoundTrip must not be mutated — net/http
// reuses it across redirects and retries.
func TestRoundTrip_DoesNotMutateCallerRequest(t *testing.T) {
	custom := &capturingTransport{}
	ctx := WithBaseTransport(context.Background(), custom)
	ctx = WithExtraHeaders(ctx, map[string]string{"X-Title": "app"})
	ctx = WithRequestIDFunc(ctx, func() string { return "id-1" }, "")

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.invalid/", nil)
	resp, err := NewOutbound().RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	if req.Header.Get("X-Title") != "" || req.Header.Get("X-Request-Id") != "" {
		t.Errorf("caller's request was mutated: %v", req.Header)
	}
	if custom.last().Get("X-Title") != "app" {
		t.Error("the clone should have carried the extra header")
	}
}

// With neither extras nor a generator installed, RoundTrip takes the no-clone
// fast path — this pins that it still works rather than skipping the base.
func TestRoundTrip_FastPathWithNoDecoration(t *testing.T) {
	custom := &capturingTransport{}
	ctx := WithBaseTransport(context.Background(), custom)
	doWith(t, ctx, NewOutbound())

	if custom.calls.Load() != 1 {
		t.Error("base transport not reached on the undecorated path")
	}
}

// End to end against a real local server, so the whole chain — header
// injection, IP stripping, the switchable base — is exercised as assembled.
func TestOutbound_EndToEndThroughCustomTransport(t *testing.T) {
	var gotTitle, gotID, gotXFF string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTitle = r.Header.Get("X-Title")
		gotID = r.Header.Get("X-Request-Id")
		gotXFF = r.Header.Get("X-Forwarded-For")
	}))
	defer srv.Close()

	var wrapped atomic.Int32
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		wrapped.Add(1)
		return http.DefaultTransport.RoundTrip(req)
	})

	ctx := WithBaseTransport(context.Background(), base)
	ctx = WithExtraHeaders(ctx, map[string]string{
		"X-Title":         "app",
		"X-Forwarded-For": "203.0.113.9", // must be stripped before it leaves
	})
	ctx = WithRequestIDFunc(ctx, func() string { return "req-9" }, "")

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := NewOutbound().RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	if wrapped.Load() != 1 {
		t.Error("custom transport was bypassed")
	}
	if gotTitle != "app" || gotID != "req-9" {
		t.Errorf("server saw title=%q id=%q", gotTitle, gotID)
	}
	if gotXFF != "" {
		t.Errorf("X-Forwarded-For leaked through a custom transport: %q", gotXFF)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
