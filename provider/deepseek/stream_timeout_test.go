package deepseek

import (
	"testing"
	"time"
)

// Streams must not carry a client-wide absolute timeout: the caller's context
// is the only lifetime bound. The non-streaming client keeps its ceiling.
func TestNew_StreamingHasNoAbsoluteTimeout(t *testing.T) {
	p := New("https://deepseek.example.test")
	if p.client.Timeout != 300*time.Second {
		t.Fatalf("non-stream client timeout = %v, want 5m", p.client.Timeout)
	}
	if p.streamClient.Timeout != 0 {
		t.Fatalf("stream client timeout = %v, want no absolute timeout", p.streamClient.Timeout)
	}
}
