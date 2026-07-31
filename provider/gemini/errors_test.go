package gemini

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/siguago/llmkit/provider"
)

// invalidKeyBody is Gemini's actual answer to a bad API key, captured verbatim
// from the live endpoint. The point of the fix is in here: code 400 and status
// INVALID_ARGUMENT, with the real cause only in details[].reason.
const invalidKeyBody = `{
  "error": {
    "code": 400,
    "message": "API key not valid. Please pass a valid API key.",
    "status": "INVALID_ARGUMENT",
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
        "reason": "API_KEY_INVALID",
        "domain": "googleapis.com",
        "metadata": {"service": "generativelanguage.googleapis.com"}
      },
      {
        "@type": "type.googleapis.com/google.rpc.LocalizedMessage",
        "locale": "en-US",
        "message": "API key not valid. Please pass a valid API key."
      }
    ]
  }
}`

// The regression this fixes: an invalid credential arrives as 400, so
// status-code classification calls it a bad request and IsAuthError says false.
// The caller then retries a key that can never work, or tells the user their
// request was malformed when their key is simply wrong.
func TestGeminiError_InvalidKeyIsAuthNotInvalidRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, invalidKeyBody)
	}))
	defer srv.Close()

	_, err := NewWithBaseURL(srv.URL).ChatCompletion(context.Background(), "bad-key", "gemini-2.5-flash",
		&provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected an error")
	}

	if got := provider.ErrorCategoryOf(err); got != provider.ErrorCategoryAuth {
		t.Errorf("category = %q, want %q", got, provider.ErrorCategoryAuth)
	}
	if got := provider.ProviderCode(err); got != "API_KEY_INVALID" {
		t.Errorf("ProviderCode = %q, want the vendor's machine-readable reason", got)
	}
	// The real HTTP status must survive: classification adds meaning, it does
	// not rewrite what the wire said.
	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected a *provider.ProviderError in the chain, got %T", err)
	}
	if pe.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want the real 400", pe.StatusCode)
	}
}

// The same classification has to hold on every route, not just chat — a caller
// hitting embeddings with a bad key deserves the same answer.
func TestGeminiError_ClassifiedOnEmbeddingsRoute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, invalidKeyBody)
	}))
	defer srv.Close()

	_, err := NewWithBaseURL(srv.URL).Embeddings(context.Background(), "bad-key", "gemini-embedding-001",
		&provider.EmbeddingRequest{Input: "hi"})
	if got := provider.ErrorCategoryOf(err); got != provider.ErrorCategoryAuth {
		t.Errorf("category = %q, want %q", got, provider.ErrorCategoryAuth)
	}
}

func TestGoogleErrorReason(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"standard error info", invalidKeyBody, "API_KEY_INVALID"},
		{"first non-empty reason wins", `{"error":{"details":[{"reason":""},{"reason":"SERVICE_DISABLED"}]}}`, "SERVICE_DISABLED"},
		{"no details", `{"error":{"code":404,"message":"nope"}}`, ""},
		{"empty details", `{"error":{"details":[]}}`, ""},
		{"not google shaped", `{"message":"something else"}`, ""},
		// An intermediary can return an empty body or HTML; falling back to the
		// HTTP status is the correct behaviour there, not a crash.
		{"empty body", ``, ""},
		{"html", `<html><body>404</body></html>`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := googleErrorReason([]byte(tc.body)); got != tc.want {
				t.Errorf("googleErrorReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGoogleErrorCategory(t *testing.T) {
	cases := []struct {
		reason string
		want   provider.ErrorCategory
	}{
		{"API_KEY_INVALID", provider.ErrorCategoryAuth},
		{"API_KEY_SERVICE_BLOCKED", provider.ErrorCategoryAuth},
		{"API_KEY_HTTP_REFERRER_BLOCKED", provider.ErrorCategoryAuth},
		{"API_KEY_SOMETHING_GOOGLE_ADDS_LATER", provider.ErrorCategoryAuth},
		{"PERMISSION_DENIED", provider.ErrorCategoryAuth},
		{"CREDENTIALS_MISSING", provider.ErrorCategoryAuth},
		{"ACCESS_TOKEN_EXPIRED", provider.ErrorCategoryAuth},

		// Deliberately unclassified. Gemini already returns 429 for quota, and
		// rate_limit asserts "nothing was billed" — IsSafeToReplay replays paid
		// image/video creation on that claim. The status code earns it; a guess
		// from a reason string does not.
		{"RATE_LIMIT_EXCEEDED", ""},
		{"RESOURCE_EXHAUSTED", ""},
		{"QUOTA_EXCEEDED", ""},
		// Not a credential problem: the key is fine, the API is off.
		{"SERVICE_DISABLED", ""},
		{"", ""},
		{"SOMETHING_UNKNOWN", ""},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if got := googleErrorCategory(tc.reason); got != tc.want {
				t.Errorf("googleErrorCategory(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

// A body with no usable reason must not invent a category — the HTTP status
// then decides, which is right for every other Gemini failure.
func TestGeminiError_UnclassifiedFallsBackToStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":404,"message":"model not found","status":"NOT_FOUND"}}`)
	}))
	defer srv.Close()

	_, err := NewWithBaseURL(srv.URL).ChatCompletion(context.Background(), "k", "nope",
		&provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hi"}}})
	if got := provider.ErrorCategoryOf(err); got != "" {
		t.Errorf("category = %q, want it left to the HTTP status", got)
	}
	var pe *provider.ProviderError
	if !errors.As(err, &pe) || pe.StatusCode != http.StatusNotFound {
		t.Errorf("the 404 must still reach the caller: %v", err)
	}
}
