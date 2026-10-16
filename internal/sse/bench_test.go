package sse

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// These benchmarks are a *baseline*, not a gate: nothing in CI fails on them.
// The decoder sits on the hot path of every streaming response, so the point is
// to make a regression visible in review rather than to defend a number.
//
// Recorded values and the machine they came from live in docs/benchmarks.md.
//
// Reproduce with:
//
//	go test ./internal/sse -run '^$' -bench . -benchmem

// benchStream returns a stream of Responses-shaped SSE events. Every frame
// carries an event name and a JSON payload, with a blank line between frames —
// the shape the Responses and Anthropic routes actually produce.
//
// payloadBytes pads the delta so the large-payload case measures assembling a
// genuinely big field rather than the same 130-byte object repeatedly.
func benchStream(events int, crlf bool, payloadBytes int) []byte {
	terminator := "\n"
	if crlf {
		terminator = "\r\n"
	}
	padding := ""
	if payloadBytes > 0 {
		padding = strings.Repeat("x", payloadBytes)
	}
	var b strings.Builder
	b.Grow(events * (220 + payloadBytes))
	for i := 0; i < events; i++ {
		fmt.Fprintf(&b, "event: response.output_text.delta%s", terminator)
		fmt.Fprintf(&b,
			`data: {"type":"response.output_text.delta","sequence_number":%d,"item_id":"msg_1","output_index":0,"content_index":0,"delta":" token%s"}%s`,
			i, padding, terminator)
		b.WriteString(terminator)
	}
	return []byte(b.String())
}

// drainCount decodes a whole stream and returns how many events were dispatched.
// A benchmark whose decoder silently stops early would look fast while
// measuring nothing, so every case asserts the count.
func drainCount(b *testing.B, stream []byte, chunkSize int) int {
	b.Helper()
	var reader io.Reader = bytes.NewReader(stream)
	if chunkSize > 0 {
		// chunkReader reslices its own copy of the header, so stream is intact.
		reader = &chunkReader{data: stream, size: chunkSize}
	}
	d := NewDecoder(reader, 0)
	count := 0
	for {
		_, err := d.Next()
		if err == nil {
			count++
			continue
		}
		if !errors.Is(err, io.EOF) {
			b.Fatalf("Next: %v", err)
		}
		return count
	}
}

func BenchmarkDecoderNext(b *testing.B) {
	const events = 64
	cases := []struct {
		name    string
		crlf    bool
		chunk   int
		payload int
	}{
		{name: "lf", chunk: 0, payload: 0},
		{name: "crlf", crlf: true, chunk: 0, payload: 0},
		{name: "lf/8-byte-reads", chunk: 8, payload: 0},
		{name: "lf/64kib-payload", chunk: 0, payload: 64 << 10},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			stream := benchStream(events, tc.crlf, tc.payload)
			b.SetBytes(int64(len(stream)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got := drainCount(b, stream, tc.chunk); got != events {
					b.Fatalf("decoded %d events, want %d", got, events)
				}
			}
		})
	}
}

// BenchmarkDecoderNext_SingleEvent isolates per-event dispatch cost from the
// per-stream setup (the bufio.Reader allocation), which dominates a 64-event
// stream at these sizes.
func BenchmarkDecoderNext_SingleEvent(b *testing.B) {
	stream := benchStream(1, false, 0)
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := drainCount(b, stream, 0); got != 1 {
			b.Fatalf("decoded %d events, want 1", got)
		}
	}
}
