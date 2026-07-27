package llmkit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/siguago/llmkit/provider/compat"
)

// Local runtimes serve unauthenticated, so they must construct without a
// credential. Everything else must keep failing closed: for a real vendor an
// empty key is a configuration bug (unset env var, typo'd config key), and
// turning it into unauthenticated traffic would surface as a 401 far from its
// cause.
func TestNew_KeyRequiredExceptLocalRuntimes(t *testing.T) {
	for _, name := range Providers() {
		if env := EnvVar(name); env != "" {
			t.Setenv(env, "")
		}
		_, err := New(name)
		if keyOptional[name] {
			if err != nil {
				t.Errorf("New(%q) without a key = %v, want success", name, err)
			}
			continue
		}
		if !errors.Is(err, ErrNoAPIKey) {
			t.Errorf("New(%q) without a key = %v, want ErrNoAPIKey", name, err)
		}
	}
}

// WithoutAPIKey is the escape hatch for an unauthenticated endpoint that is not
// one of the built-in local runtimes — an internal gateway, a self-hosted relay.
func TestWithoutAPIKey_AllowsAnyProvider(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := New(OpenAI, WithoutAPIKey()); err != nil {
		t.Errorf("New(openai, WithoutAPIKey()) = %v, want success", err)
	}
	p := compat.New(compat.Config{ProviderName: "internal-gw", BaseURL: "http://gw.internal/v1"})
	if _, err := Wrap(p, WithoutAPIKey()); err != nil {
		t.Errorf("Wrap(custom, WithoutAPIKey()) = %v, want success", err)
	}
}

// KeyOptional is what lets tooling gate on "is this provider reachable" without
// hardcoding the list — llmkit-probe would otherwise refuse to probe a local
// runtime for want of a key that never existed.
func TestKeyOptional_MatchesTheTable(t *testing.T) {
	for _, name := range Providers() {
		if got, want := KeyOptional(name), keyOptional[name]; got != want {
			t.Errorf("KeyOptional(%q) = %v, want %v", name, got, want)
		}
	}
	if KeyOptional("no-such-provider") {
		t.Error("KeyOptional on an unknown provider must be false, not permissive")
	}
	if !KeyOptional(Ollama) || !KeyOptional(VLLM) {
		t.Error("the local runtimes must report key-optional")
	}
	if KeyOptional(OpenAI) {
		t.Error("a real vendor must never report key-optional")
	}
}

// WithoutAPIKey promises that no credential header goes out — on every route, not
// just the chat route. The routes below are the ones that build their requests
// outside the compat layer, which is where an unconditional "Bearer "+apiKey used
// to survive: a malformed credential reaching the internal gateway this option
// exists for, answered by a 401 nowhere near its cause.
func TestWithoutAPIKey_NoCredentialHeaderOnAnyRoute(t *testing.T) {
	// header is the credential header each provider would send if it sent one.
	for _, tc := range []struct {
		provider string
		header   string
		body     string
		call     func(*Client) error
	}{
		{OpenAI, "Authorization", `{"data":[]}`, func(c *Client) error {
			_, err := c.Models(context.Background())
			return err
		}},
		{Anthropic, "x-api-key", `{"data":[],"has_more":false}`, func(c *Client) error {
			_, err := c.Models(context.Background())
			return err
		}},
		{OpenRouter, "Authorization", `{"data":[]}`, func(c *Client) error {
			_, err := c.Models(context.Background())
			return err
		}},
		{Gemini, "x-goog-api-key", `{"models":[]}`, func(c *Client) error {
			_, err := c.Models(context.Background())
			return err
		}},
		{DeepSeek, "Authorization", `{"data":[]}`, func(c *Client) error {
			_, err := c.Models(context.Background())
			return err
		}},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			seen := make(chan http.Header, 1)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer ts.Close()

			if env := EnvVar(tc.provider); env != "" {
				t.Setenv(env, "")
			}
			c, err := New(tc.provider, WithoutAPIKey(), WithBaseURL(ts.URL))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			h := <-seen
			if _, present := h[http.CanonicalHeaderKey(tc.header)]; present {
				t.Errorf("%s sent %s: %q — WithoutAPIKey must send no credential header",
					tc.provider, tc.header, h.Get(tc.header))
			}
		})
	}
}

// The inverse: a supplied credential must still reach every one of those routes.
// Suppressing an empty header is worthless if it also suppresses a real one.
func TestWithAPIKey_CredentialReachesModelsRoute(t *testing.T) {
	for _, tc := range []struct {
		provider string
		header   string
		want     string
		body     string
	}{
		{OpenAI, "Authorization", "Bearer sk-test", `{"data":[]}`},
		{Anthropic, "x-api-key", "sk-test", `{"data":[],"has_more":false}`},
		{OpenRouter, "Authorization", "Bearer sk-test", `{"data":[]}`},
		{Gemini, "x-goog-api-key", "sk-test", `{"models":[]}`},
		{DeepSeek, "Authorization", "Bearer sk-test", `{"data":[]}`},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			seen := make(chan string, 1)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Get(tc.header)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer ts.Close()

			c, err := New(tc.provider, WithAPIKey("sk-test"), WithBaseURL(ts.URL))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := c.Models(context.Background()); err != nil {
				t.Fatalf("Models: %v", err)
			}
			if got := <-seen; got != tc.want {
				t.Errorf("%s sent %s = %q, want %q", tc.provider, tc.header, got, tc.want)
			}
		})
	}
}

// An empty credential must produce no Authorization header at all. "Bearer "
// with nothing after it is not a valid credential, and a proxy in front of a
// local runtime can reject the malformed header on its own.
func TestNoCredential_SendsNoAuthorizationHeader(t *testing.T) {
	seen := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer ts.Close()

	c, err := New(Ollama, WithBaseURL(ts.URL))
	if err != nil {
		t.Fatalf("New(ollama): %v", err)
	}
	if _, err := c.Say(context.Background(), "llama3.2", "hi"); err != nil {
		t.Fatalf("Say: %v", err)
	}
	if got := <-seen; got != "" {
		t.Errorf("Authorization = %q, want no header", got)
	}
}

// A credential supplied to a local runtime must still be sent — vLLM started
// with --api-key, or an Ollama behind an authenticating proxy.
func TestLocalRuntime_SendsSuppliedCredential(t *testing.T) {
	seen := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer ts.Close()

	c, err := New(VLLM, WithBaseURL(ts.URL), WithAPIKey("sk-local"))
	if err != nil {
		t.Fatalf("New(vllm): %v", err)
	}
	if _, err := c.Say(context.Background(), "Qwen/Qwen3-8B", "hi"); err != nil {
		t.Fatalf("Say: %v", err)
	}
	if got := <-seen; got != "Bearer sk-local" {
		t.Errorf("Authorization = %q, want the supplied credential", got)
	}
}
