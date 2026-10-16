package compat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// These benchmarks are a *baseline*, not a gate: nothing in CI fails on them.
// Every OpenAI-compatible provider funnels its stream through StreamReader, so
// this is the per-frame cost of the whole compat family.
//
// Two costs are baked into every measurement and worth naming: NewStreamReader
// builds a bufio.Scanner with a 64 KiB initial frame buffer, and the reasoning
// case re-parses each frame a second time (liftReasoningOnChunk) on top of the
// typed decode.
//
// Recorded values live in docs/benchmarks.md.
//
// Reproduce with:
//
//	go test ./provider/compat -run '^$' -bench . -benchmem

// benchFrame renders one SSE frame whose choices array is the given raw JSON.
func benchFrame(choices string) string {
	return fmt.Sprintf(
		`data: {"id":"chatcmpl-bench","object":"chat.completion.chunk","created":1,"model":"gpt-4.1-mini","choices":%s}`+"\n\n",
		choices)
}

// benchStream builds an OpenAI-compatible SSE stream: a role frame, a run of
// delta frames, and a finish frame before [DONE].
//
// The reasoning variant emits delta.reasoning instead of delta.content, which
// is what providers using the shorter field name send (Kimi K2.6, some vLLM
// templates) and what forces the second parse pass.
func benchStream(deltas int, reasoning bool) []byte {
	var b strings.Builder
	b.Grow(deltas * 200)
	b.WriteString(benchFrame(`[{"index":0,"delta":{"role":"assistant","content":""}}]`))
	for i := 0; i < deltas; i++ {
		if reasoning {
			b.WriteString(benchFrame(`[{"index":0,"delta":{"reasoning":" think"}}]`))
			continue
		}
		b.WriteString(benchFrame(`[{"index":0,"delta":{"content":" token"}}]`))
	}
	b.WriteString(benchFrame(`[{"index":0,"delta":{},"finish_reason":"stop"}]`))
	b.WriteString("data: [DONE]\n\n")
	return []byte(b.String())
}

func drainStream(b *testing.B, stream []byte) int {
	b.Helper()
	r := NewStreamReader(context.Background(), io.NopCloser(bytes.NewReader(stream)), "bench", false)
	defer func() { _ = r.Close() }()
	count := 0
	for {
		_, err := r.Recv()
		if err == nil {
			count++
			continue
		}
		if !errors.Is(err, io.EOF) {
			b.Fatalf("Recv: %v", err)
		}
		return count
	}
}

func BenchmarkStreamReaderRecv(b *testing.B) {
	const deltas = 256
	cases := []struct {
		name      string
		reasoning bool
	}{
		{name: "content"},
		{name: "reasoning"},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			stream := benchStream(deltas, tc.reasoning)
			want := deltas + 2 // role frame + deltas + finish frame
			b.SetBytes(int64(len(stream)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got := drainStream(b, stream); got != want {
					b.Fatalf("received %d chunks, want %d", got, want)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*want), "ns/chunk")
		})
	}
}
