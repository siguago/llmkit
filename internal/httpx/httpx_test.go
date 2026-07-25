package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// The whole reason httpx exists is that a hand-rolled &http.Transport{} does
// NOT honor HTTP_PROXY/HTTPS_PROXY — only an explicit Proxy field does. If this
// regresses, every user behind a proxy silently loses connectivity.
//
// The assertion is on identity rather than behavior on purpose:
// http.ProxyFromEnvironment reads the environment exactly once per process
// (sync.Once), so any t.Setenv-based check would pass or fail depending on
// which test ran first. Pinning the function identity states the actual
// contract — "environment proxying is delegated to the standard library" —
// without that ordering hazard. NO_PROXY, CIDR matching and the rest are the
// standard library's semantics and are its own to test.
func TestNewTransport_DelegatesProxyToEnvironment(t *testing.T) {
	tr := NewTransport()
	if tr.Proxy == nil {
		t.Fatal("Transport.Proxy is nil; HTTP(S)_PROXY and NO_PROXY would be ignored")
	}
	want := reflect.ValueOf(http.ProxyFromEnvironment).Pointer()
	got := reflect.ValueOf(tr.Proxy).Pointer()
	if got != want {
		t.Errorf("Transport.Proxy is not http.ProxyFromEnvironment")
	}
}

// SSE latency depends on not gzipping every frame.
func TestNewTransport_DisablesCompression(t *testing.T) {
	if !NewTransport().DisableCompression {
		t.Error("DisableCompression is off; SSE frames would be gzipped")
	}
}

func TestWithExtraHeaders_RoundTrips(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewOutbound()}
	ctx := WithExtraHeaders(context.Background(), map[string]string{
		"X-Title":      "my app",
		"HTTP-Referer": "https://myapp.example",
	})
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got.Get("X-Title") != "my app" {
		t.Errorf("X-Title = %q", got.Get("X-Title"))
	}
	if got.Get("HTTP-Referer") != "https://myapp.example" {
		t.Errorf("HTTP-Referer = %q", got.Get("HTTP-Referer"))
	}
}

// Credential headers set by the adapter must survive caller-supplied extras —
// otherwise a stray WithHeader could send the wrong vendor's key upstream.
func TestExtraHeaders_CannotOverrideCredentials(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewOutbound()}
	ctx := WithExtraHeaders(context.Background(), map[string]string{
		"Authorization":     "Bearer sk-attacker",
		"authorization":     "Bearer sk-attacker-lowercase",
		"X-Api-Key":         "attacker",
		"x-goog-api-key":    "attacker",
		"anthropic-version": "1999-01-01",
	})
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer sk-real")
	req.Header.Set("x-api-key", "real")
	req.Header.Set("x-goog-api-key", "real")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	for name, want := range map[string]string{
		"Authorization":     "Bearer sk-real",
		"X-Api-Key":         "real",
		"X-Goog-Api-Key":    "real",
		"Anthropic-Version": "2023-06-01",
	} {
		if got.Get(name) != want {
			t.Errorf("%s = %q, want %q (protected header was overwritten)", name, got.Get(name), want)
		}
	}
}

// Extras are applied BEFORE IP stripping, so a caller cannot smuggle an end
// user's address to the vendor via a forwarding header.
func TestExtraHeaders_CannotReintroduceIPDisclosure(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewOutbound()}
	ctx := WithExtraHeaders(context.Background(), map[string]string{
		"X-Forwarded-For":  "203.0.113.7",
		"X-Real-IP":        "203.0.113.7",
		"CF-Connecting-IP": "203.0.113.7",
	})
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	for _, name := range []string{"X-Forwarded-For", "X-Real-Ip", "Cf-Connecting-Ip"} {
		if v := got.Get(name); v != "" {
			t.Errorf("%s leaked through as %q; IP stripping must run after extras", name, v)
		}
	}
}

// The original request must not be mutated — it may be retried.
func TestExtraHeaders_DoesNotMutateOriginalRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	client := &http.Client{Transport: NewOutbound()}
	ctx := WithExtraHeaders(context.Background(), map[string]string{"X-Title": "t"})
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if v := req.Header.Get("X-Title"); v != "" {
		t.Errorf("caller's request was mutated: X-Title = %q", v)
	}
}

func TestWithExtraHeaders_EmptyMapIsNoop(t *testing.T) {
	ctx := context.Background()
	if WithExtraHeaders(ctx, nil) != ctx {
		t.Error("nil map should return the same context")
	}
	if WithExtraHeaders(ctx, map[string]string{}) != ctx {
		t.Error("empty map should return the same context")
	}
}

func TestNewClientPair(t *testing.T) {
	c, s := NewClientPair()
	if c.Timeout != DefaultTimeout {
		t.Errorf("client timeout = %v, want %v", c.Timeout, DefaultTimeout)
	}
	if s.Timeout != DefaultStreamTimeout {
		t.Errorf("stream timeout = %v, want %v", s.Timeout, DefaultStreamTimeout)
	}
	if s.Timeout <= c.Timeout {
		t.Error("stream client should tolerate longer requests than the regular one")
	}
}

func TestNewClient_ZeroUsesDefault(t *testing.T) {
	if got := NewClient(0); got.Timeout != DefaultTimeout {
		t.Errorf("NewClient(0).Timeout = %v, want %v", got.Timeout, DefaultTimeout)
	}
}
