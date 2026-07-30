package provider

import (
	"errors"
	"net/http"
	"testing"
)

// Compile-time guard for ProviderError's documented stable shape. In
// particular, adding metadata fields here would break callers that still use an
// unkeyed literal.
var _ = ProviderError{http.StatusBadRequest, "boom", "", ""}

func TestErrorMetadataPreservesProviderError(t *testing.T) {
	base := &ProviderError{
		StatusCode: http.StatusOK,
		Message:    "body error",
		RetryAfter: "9",
		RequestID:  "trace-1",
	}
	err := WithErrorMetadata(base, "vendor-42", ErrorCategoryServer)

	var gotBase *ProviderError
	if !errors.As(err, &gotBase) || gotBase != base {
		t.Fatalf("errors.As = %p, want original %p", gotBase, base)
	}
	if got := ProviderCode(err); got != "vendor-42" {
		t.Errorf("ProviderCode = %q, want vendor-42", got)
	}
	if got := ErrorCategoryOf(err); got != ErrorCategoryServer {
		t.Errorf("ErrorCategoryOf = %q, want server", got)
	}
	if gotBase.RetryAfter != "9" || gotBase.RequestID != "trace-1" {
		t.Errorf("base metadata changed: %+v", gotBase)
	}
}

func TestWithErrorMetadataNoops(t *testing.T) {
	if got := WithErrorMetadata(nil, "code", ErrorCategoryAuth); got != nil {
		t.Errorf("nil error became %v", got)
	}
	base := errors.New("plain")
	if got := WithErrorMetadata(base, "", ""); got != base {
		t.Errorf("empty metadata wrapped the error")
	}
	// A category is just a string, so a typo compiles. Honoring it would be
	// worse than ignoring it: every classification helper short-circuits on a
	// non-empty category, so "ratelimit" would make an upstream 500 report as
	// neither a server error nor retryable.
	if got := WithErrorMetadata(base, "", "ratelimit"); got != base {
		t.Errorf("unrecognized category wrapped the error: %v", got)
	}
	withCode := WithErrorMetadata(base, "vendor-1", "ratelimit")
	if ProviderCode(withCode) != "vendor-1" {
		t.Errorf("ProviderCode = %q, want the code kept", ProviderCode(withCode))
	}
	if got := ErrorCategoryOf(withCode); got != "" {
		t.Errorf("ErrorCategoryOf = %q, want the typo dropped", got)
	}
}

// Every defined category must survive WithErrorMetadata. A validator that
// rejects a real category would silently disable classification for it — the
// same failure the validator exists to prevent, in the other direction.
func TestErrorCategoryValidCoversEveryDefinedCategory(t *testing.T) {
	for _, c := range []ErrorCategory{
		ErrorCategoryAuth, ErrorCategoryRateLimit, ErrorCategoryInvalidRequest,
		ErrorCategoryNotFound, ErrorCategoryServer,
	} {
		if !c.valid() {
			t.Errorf("%q is defined but fails valid()", c)
		}
		if got := ErrorCategoryOf(WithErrorMetadata(errors.New("x"), "", c)); got != c {
			t.Errorf("round trip of %q = %q", c, got)
		}
	}
}
