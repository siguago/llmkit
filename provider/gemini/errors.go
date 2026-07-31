package gemini

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/siguago/llmkit/provider"
)

// geminiError builds a ProviderError and, when Google supplied one, attaches
// its machine-readable reason as the vendor code plus a unified category.
//
// This exists because Gemini answers an invalid API key with HTTP 400
// INVALID_ARGUMENT rather than 401:
//
//	{"error":{"code":400,"status":"INVALID_ARGUMENT",
//	          "message":"API key not valid. Please pass a valid API key.",
//	          "details":[{"@type":"...ErrorInfo","reason":"API_KEY_INVALID",...}]}}
//
// Classifying on the status code alone therefore calls a bad credential a bad
// request: IsAuthError reports false, and the caller either retries a key that
// can never work or shows the user "invalid request" for what is really a
// misconfigured key. The dependable signal is details[].reason, a stable
// identifier from google.rpc.ErrorInfo — the message text is localized and the
// status is too coarse.
func geminiError(resp *http.Response, body []byte) error {
	base := provider.NewProviderErrorFromResponse(resp, "gemini", body)
	reason := googleErrorReason(body)
	if reason == "" {
		return base
	}
	return provider.WithErrorMetadata(base, reason, googleErrorCategory(reason))
}

// googleErrorReason pulls error.details[].reason out of Google's standard error
// envelope. Returns "" for anything that isn't that shape — including the empty
// bodies an intermediary can produce — so callers fall back to the HTTP status.
func googleErrorReason(body []byte) string {
	var envelope struct {
		Error struct {
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	for _, d := range envelope.Error.Details {
		if d.Reason != "" {
			return d.Reason
		}
	}
	return ""
}

// googleErrorCategory maps a reason to a unified category, and deliberately
// covers only credential failures.
//
// Everything else stays uncategorized on purpose. An uncategorized error falls
// back to the HTTP status, which Gemini already gets right everywhere else —
// 429 for quota, 404 for a missing model, 5xx for its own faults. Adding a
// rate_limit mapping here would be actively unsafe: that category asserts the
// upstream billed nothing, and IsSafeToReplay will replay image and video
// creation on it. The status code earns that claim by itself; a guess from a
// reason string does not.
//
// Unrecognized reasons still reach the caller through ProviderCode, so nothing
// is lost by not classifying them.
func googleErrorCategory(reason string) provider.ErrorCategory {
	// Every API_KEY_* reason is about the credential itself: invalid, blocked
	// for this service, or restricted to other referrers/IPs/apps. Matching the
	// prefix keeps new ones working without a code change.
	if strings.HasPrefix(reason, "API_KEY_") {
		return provider.ErrorCategoryAuth
	}
	switch reason {
	case "CREDENTIALS_MISSING", "ACCESS_TOKEN_EXPIRED", "ACCESS_TOKEN_TYPE_UNSUPPORTED", "PERMISSION_DENIED":
		// "unprivileged credential" is explicitly part of what auth covers.
		return provider.ErrorCategoryAuth
	}
	return ""
}
