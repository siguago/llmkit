package compat

import (
	"testing"
	"time"
)

// Streams must not carry a client-wide absolute timeout: background and long
// reasoning turns legitimately hold a connection open, and the caller's
// context is the only lifetime bound. The non-streaming client keeps its
// regular-request ceiling.
func TestNew_StreamingHasNoAbsoluteTimeout(t *testing.T) {
	p := New(Config{ProviderName: "example", BaseURL: "https://api.example.test/v1"})
	if p.client.Timeout != 300*time.Second {
		t.Fatalf("non-stream client timeout = %v, want 5m", p.client.Timeout)
	}
	if p.streamClient.Timeout != 0 {
		t.Fatalf("stream client timeout = %v, want no absolute timeout", p.streamClient.Timeout)
	}
}
