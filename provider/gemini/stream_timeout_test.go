package gemini

import (
	"testing"
	"time"
)

// Streams must not carry a client-wide absolute timeout: the caller's context
// is the only lifetime bound. The non-streaming client keeps its ceiling.
func TestNewWithBaseURL_StreamingHasNoAbsoluteTimeout(t *testing.T) {
	p := NewWithBaseURL("https://gemini.example.test/v1beta")
	if p.client.Timeout != 300*time.Second {
		t.Fatalf("non-stream client timeout = %v, want 5m", p.client.Timeout)
	}
	if p.streamClient.Timeout != 0 {
		t.Fatalf("stream client timeout = %v, want no absolute timeout", p.streamClient.Timeout)
	}
}
