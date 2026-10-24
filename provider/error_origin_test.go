package provider

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestErrorOriginPreservesCauseAndMetadata(t *testing.T) {
	base := &ProviderError{http.StatusTooManyRequests, "slow down", "7", "req-1"}
	err := WithErrorOrigin(MarkUnsafeToReplay(WithErrorMetadata(base, "quota", ErrorCategoryRateLimit)), ErrorOriginUpstream)
	err = fmt.Errorf("call failed: %w", err)
	var got *ProviderError
	if !errors.As(err, &got) || got != base || !errors.Is(err, base) {
		t.Fatalf("original ProviderError hidden: %v", err)
	}
	if ErrorOriginOf(err) != ErrorOriginUpstream || ProviderCode(err) != "quota" ||
		ErrorCategoryOf(err) != ErrorCategoryRateLimit || !IsMarkedUnsafeToReplay(err) {
		t.Fatalf("wrapped metadata lost: %v", err)
	}
	if got.RetryAfter != "7" || got.RequestID != "req-1" || got.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("original error changed: %+v", got)
	}
}

func TestErrorOriginUnknownAndRepeatedAnnotation(t *testing.T) {
	base := errors.New("network failure")
	if WithErrorOrigin(nil, ErrorOriginLocal) != nil || ErrorOriginOf(nil) != ErrorOriginUnknown {
		t.Fatal("nil should remain nil with unknown origin")
	}
	for _, origin := range []ErrorOrigin{"", ErrorOriginUnknown, "typo"} {
		if got := WithErrorOrigin(base, origin); got != base {
			t.Fatalf("origin %q should leave error unchanged", origin)
		}
	}
	if got := ErrorOriginOf(&ProviderError{StatusCode: 422, RequestID: "trace"}); got != ErrorOriginUnknown {
		t.Fatalf("status and request ID cannot establish origin, got %q", got)
	}
	local := WithErrorOrigin(base, ErrorOriginLocal)
	for _, origin := range []ErrorOrigin{ErrorOriginLocal, ErrorOriginUpstream} {
		if got := WithErrorOrigin(local, origin); got != local || ErrorOriginOf(got) != ErrorOriginLocal {
			t.Fatalf("existing origin should be preserved, got %v", got)
		}
	}
	joined := errors.Join(errors.New("context"), local)
	if ErrorOriginOf(joined) != ErrorOriginLocal {
		t.Fatal("joined error hid origin")
	}
}

// The unknown marker is the answer for every path that has not established a
// source: plain and wrapped errors, joined errors, nil, and a ProviderError
// carrying only an HTTP status and request ID. Unannotated adapters depend on
// this staying unknown — a status code or header is not evidence of origin.
func TestErrorOriginUnknownMarkers(t *testing.T) {
	unmarked := &ProviderError{
		StatusCode: http.StatusBadGateway,
		RetryAfter: "30",
		RequestID:  "req-1",
	}
	cases := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"plain error", errors.New("network failure")},
		{"wrapped plain error", fmt.Errorf("request failed: %w", errors.New("network failure"))},
		{"joined plain errors", errors.Join(errors.New("a"), errors.New("b"))},
		{"provider error with status and request id", unmarked},
		{"provider error wrapped", fmt.Errorf("call: %w", unmarked)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrorOriginOf(tc.err); got != ErrorOriginUnknown {
				t.Fatalf("ErrorOriginOf = %q, want %q", got, ErrorOriginUnknown)
			}
		})
	}

	// The internal marker can hold unknown or invalid origins; neither may be
	// reported as an established source.
	for _, origin := range []ErrorOrigin{ErrorOriginUnknown, "", "typo"} {
		marked := &errorWithOrigin{err: errors.New("opaque failure"), origin: origin}
		if got := ErrorOriginOf(marked); got != ErrorOriginUnknown {
			t.Errorf("marker origin %q reported as %q, want unknown", origin, got)
		}
	}
}
