package openaibatch

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// These benchmarks are a *baseline*, not a gate: nothing in CI fails on them.
// A batch output file is read one JSONL line at a time, and the interesting
// variable is line size — a batched image response puts base64 on a single
// line, which is exactly why DefaultMaxLineBytes is 32 MiB.
//
// Recorded values live in docs/benchmarks.md.
//
// Reproduce with:
//
//	go test ./protocol/openaibatch -run '^$' -bench . -benchmem

const benchCustomID = "batch_req_9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c"

// benchOutputJSONL builds a batch output file with the given number of lines,
// each carrying a chat-completion body whose content field is padded to
// bodyBytes.
func benchOutputJSONL(lines, bodyBytes int) []byte {
	content := strings.Repeat("x", bodyBytes)
	var b strings.Builder
	b.Grow(lines * (bodyBytes + 320))
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b,
			`{"id":"batch_req_%d","custom_id":"req-%d","response":{"status_code":200,"request_id":"req_upstream_%d","body":{"id":"chatcmpl-%d","object":"chat.completion","model":"gpt-4.1-mini","choices":[{"index":0,"message":{"role":"assistant","content":"%s"},"finish_reason":"stop"}]}},"error":null}`+"\n",
			i, i, i, i, content)
	}
	return []byte(b.String())
}

// drainOutput reads a whole output file and returns the line count. Counting is
// the point: a reader that stopped early would otherwise benchmark as very fast
// while doing nothing.
func drainOutput(b *testing.B, file []byte) int {
	b.Helper()
	r := NewOutputReader(io.NopCloser(bytes.NewReader(file)), 0)
	defer func() { _ = r.Close() }()
	count := 0
	for {
		_, err := r.Next()
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

func BenchmarkOutputReaderNext(b *testing.B) {
	cases := []struct {
		name      string
		lines     int
		bodyBytes int
	}{
		// The common case: a text completion, small lines, many of them.
		{name: "small-lines", lines: 128, bodyBytes: 64},
		// The tail case that sets the line ceiling: base64 image data.
		{name: "large-body/256kib", lines: 8, bodyBytes: 256 << 10},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			file := benchOutputJSONL(tc.lines, tc.bodyBytes)
			b.SetBytes(int64(len(file)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got := drainOutput(b, file); got != tc.lines {
					b.Fatalf("decoded %d lines, want %d", got, tc.lines)
				}
			}
		})
	}
}

func BenchmarkEncodeInput(b *testing.B) {
	const lines = 128
	items := make([]InputItem, 0, lines)
	for i := 0; i < lines; i++ {
		item, err := NewInputItem(fmt.Sprintf("req-%d", i), EndpointChatCompletions, map[string]any{
			"model": "gpt-4.1-mini",
			"messages": []map[string]string{
				{"role": "user", "content": "hello"},
			},
		})
		if err != nil {
			b.Fatalf("NewInputItem: %v", err)
		}
		items = append(items, item)
	}

	// Size the payload once so SetBytes reports real throughput, and prove the
	// encoder emits one line per item before timing it.
	var sized bytes.Buffer
	if err := EncodeInput(&sized, items...); err != nil {
		b.Fatalf("EncodeInput: %v", err)
	}
	if got := bytes.Count(sized.Bytes(), []byte{'\n'}); got != lines {
		b.Fatalf("encoded %d lines, want %d", got, lines)
	}

	b.SetBytes(int64(sized.Len()))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := EncodeInput(io.Discard, items...); err != nil {
			b.Fatalf("EncodeInput: %v", err)
		}
	}
}
