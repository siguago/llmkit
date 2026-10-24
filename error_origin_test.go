package llmkit

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/siguago/llmkit/provider"
)

func TestErrorOriginDoesNotChangeClassification(t *testing.T) {
	for _, status := range []int{400, 401, 404, 422, 429, 500} {
		for _, origin := range []ErrorOrigin{ErrorOriginLocal, ErrorOriginUpstream} {
			base := &APIError{StatusCode: status, Message: "failure", RetryAfter: "5", RequestID: "req-1"}
			err := fmt.Errorf("operation: %w", provider.WithErrorOrigin(base, origin))
			if ErrorOriginOf(err) != origin || StatusCode(err) != status || RetryAfter(err) != 5*time.Second {
				t.Fatalf("metadata lost for status %d origin %s: %v", status, origin, err)
			}
			checks := []func(error) bool{IsAuthError, IsNotFound, IsInvalidRequest, IsRateLimited, IsServerError, IsRetryable, IsSafeToReplay}
			for i, check := range checks {
				if check(err) != check(base) {
					t.Errorf("classification %d changed for status %d origin %s", i, status, origin)
				}
			}
			var got *APIError
			if !errors.As(err, &got) || got != base || got.RequestID != "req-1" {
				t.Fatal("APIError hidden by annotation")
			}
		}
	}
	if ErrorOriginOf(nil) != ErrorOriginUnknown || ErrorOriginOf(context.Canceled) != ErrorOriginUnknown {
		t.Fatal("unmarked errors must remain unknown")
	}
	err := provider.WithErrorOrigin(context.Canceled, ErrorOriginLocal)
	if !errors.Is(err, context.Canceled) || IsRetryable(err) || IsSafeToReplay(err) {
		t.Fatal("origin annotation changed cancellation semantics")
	}
}
