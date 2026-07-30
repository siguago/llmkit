package llmkit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// recordingTransport counts round trips and remembers the last request it saw,
// standing in for the tracing/metrics wrapper a caller would install.
type recordingTransport struct {
	base    http.RoundTripper
	calls   atomic.Int32
	lastReq atomic.Pointer[http.Request]
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.calls.Add(1)
	rt.lastReq.Store(req)
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func TestWithTransport_IsUsedForRequests(t *testing.T) {
	rt := &recordingTransport{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	}, WithTransport(rt))

	if _, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := rt.calls.Load(); got != 1 {
		t.Errorf("custom transport round trips = %d, want 1", got)
	}
}

// The custom transport sits at the bottom of the chain, so by the time it runs
// the credential and the caller's headers are already on the request. A caller
// instrumenting traffic sees what actually goes out.
func TestWithTransport_SeesFullyDecoratedRequest(t *testing.T) {
	rt := &recordingTransport{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	}, WithTransport(rt), WithHeader("X-Title", "my app"))

	if _, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	seen := rt.lastReq.Load()
	if seen == nil {
		t.Fatal("transport never saw a request")
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization at transport = %q, want the negotiated credential", got)
	}
	if got := seen.Header.Get("X-Title"); got != "my app" {
		t.Errorf("X-Title at transport = %q", got)
	}
}

// A custom transport must not be able to undo the client-IP stripping, so the
// forwarding headers are already gone when it runs.
func TestWithTransport_CannotSeeStrippedIPHeaders(t *testing.T) {
	rt := &recordingTransport{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	}, WithTransport(rt), WithHeader("X-Forwarded-For", "203.0.113.7"))

	if _, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	seen := rt.lastReq.Load()
	if seen == nil {
		t.Fatal("transport never saw a request")
	}
	if got := seen.Header.Get("X-Forwarded-For"); got != "" {
		t.Errorf("X-Forwarded-For = %q, want it stripped before the custom transport", got)
	}
}

// Streams get the same decoration as unary calls — they take the decorate path
// rather than prepare, and a regression there would silently drop the caller's
// transport, logger and headers on exactly the long-lived calls that most need
// instrumenting.
func TestWithTransport_AppliesToStreams(t *testing.T) {
	rt := &recordingTransport{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n")
	}, WithTransport(rt))

	stream, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()
	for {
		if _, err := stream.Recv(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("Recv: %v", err)
		}
	}
	if got := rt.calls.Load(); got != 1 {
		t.Errorf("custom transport round trips on stream = %d, want 1", got)
	}
}

func TestWithRequestID_SendsHeader(t *testing.T) {
	var seen []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("X-Request-Id"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	}, WithRequestID(func() string { return "req-42" }))

	if _, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(seen) != 1 || seen[0] != "req-42" {
		t.Errorf("X-Request-Id upstream = %v, want [req-42]", seen)
	}
}

func TestWithRequestIDHeader_Overrides(t *testing.T) {
	var seen string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Correlation-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	}, WithRequestID(func() string { return "abc" }), WithRequestIDHeader("X-Correlation-Id"))

	if _, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if seen != "abc" {
		t.Errorf("X-Correlation-Id = %q, want abc", seen)
	}
}

// Each attempt gets its own ID, so a retried call is traceable attempt by
// attempt rather than collapsing into one identifier.
func TestWithRequestID_FreshPerAttempt(t *testing.T) {
	var ids []string
	var served, generated atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		ids = append(ids, r.Header.Get("X-Request-Id"))
		if served.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	},
		WithRetry(fastRetry()),
		// Counted on the generator side: the ID is produced before the request
		// goes out, so a counter the handler bumps would lag by one attempt.
		WithRequestID(func() string {
			return "req-" + string(rune('a'+generated.Add(1)-1))
		}),
	)

	if _, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("attempts = %d, want 3", len(ids))
	}
	if ids[0] == ids[1] || ids[1] == ids[2] {
		t.Errorf("request IDs repeated across attempts: %v", ids)
	}
}

// The credential must survive a request-ID generator: headersFor rebuilds the
// header map per call, and a bug there could drop or leak the wrong values.
func TestWithRequestID_DoesNotDisturbOtherHeaders(t *testing.T) {
	var auth, title string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		title = r.Header.Get("X-Title")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	}, WithHeader("X-Title", "my app"), WithRequestID(func() string { return "r1" }))

	if _, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if auth != "Bearer sk-test" || title != "my app" {
		t.Errorf("auth = %q, title = %q", auth, title)
	}
}

// A generator returning "" means "no ID for this one" and must not put an empty
// header on the wire.
func TestWithRequestID_EmptyIDSendsNoHeader(t *testing.T) {
	var present bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["X-Request-Id"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	}, WithRequestID(func() string { return "" }))

	if _, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if present {
		t.Error("empty request ID should not produce a header")
	}
}

// The vendor's own request ID is the one their support asks for, and it is only
// available while the failing response is in hand.
func TestAPIError_CarriesUpstreamRequestID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "vendor-req-99")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}, WithRetry(NoRetry()))

	_, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want *APIError", err, err)
	}
	if apiErr.RequestID != "vendor-req-99" {
		t.Errorf("RequestID = %q, want vendor-req-99", apiErr.RequestID)
	}
}

func TestAPIError_RequestIDAlternateHeaders(t *testing.T) {
	for _, header := range []string{
		"Request-Id",
		"Cf-Ray",
		"X-Amzn-Requestid",
		"X-Guploader-Uploadid",
		"X-Generation-Id",
		"Minimax-Request-Id",
		"trace_id",
		"Trace-Id",
	} {
		t.Run(header, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(header, "id-from-"+header)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"nope"}`)
			}, WithRetry(NoRetry()))

			_, err := c.Chat(context.Background(), &ChatRequest{
				Model:    "test-model",
				Messages: []Message{User("hi")},
			})
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want *APIError", err)
			}
			if apiErr.RequestID != "id-from-"+header {
				t.Errorf("RequestID = %q, want id-from-%s", apiErr.RequestID, header)
			}
		})
	}
}

func TestAPIError_NoRequestIDHeader(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"nope"}`)
	}, WithRetry(NoRetry()))

	_, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.RequestID != "" {
		t.Errorf("RequestID = %q, want empty when the vendor sent none", apiErr.RequestID)
	}
}

// The whole point of WithLogger is that the default is silence: a library must
// not write to the embedding program's log stream uninvited.
func TestLogging_SilentByDefault(t *testing.T) {
	var global bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&global, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	// A stream with a malformed chunk in the middle: the historical trigger for
	// an uninvited slog.Warn.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {not json}\n\ndata: [DONE]\n\n")
	})
	stream, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	if global.Len() != 0 {
		t.Errorf("SDK wrote to the global logger without being asked:\n%s", global.String())
	}
}

func TestWithLogger_ReceivesDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {not json}\n\ndata: [DONE]\n\n")
	}, WithLogger(logger), WithStreamTolerance(TolerateMalformedChunks))

	stream, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer stream.Close()
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	got := buf.String()
	if !strings.Contains(got, "malformed stream frame") {
		t.Errorf("configured logger saw no malformed-frame diagnostic:\n%s", got)
	}
	// The diagnostic has to say which provider and carry enough of the payload
	// to identify it, or it can't be acted on.
	if !strings.Contains(got, "provider=deepseek") || !strings.Contains(got, "not json") {
		t.Errorf("diagnostic lacks provider or payload context:\n%s", got)
	}
}

// httptest servers are localhost, which ProxyFromEnvironment skips, so this
// verifies the default path still works when no transport is injected.
func TestDefaultTransport_StillUsedWhenUnset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatOK)
	}))
	defer srv.Close()

	c, err := New(DeepSeek, WithAPIKey("sk-test"), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "test-model",
		Messages: []Message{User("hi")},
	}); err != nil {
		t.Fatalf("Chat with default transport: %v", err)
	}
}
