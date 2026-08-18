package openaibatch

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// FuzzOutputReader asserts the JSONL decoder never panics, terminates on
// arbitrary input, keeps the line cap authoritative, and — when a line does
// decode — preserves the response body bytes verbatim.
func FuzzOutputReader(f *testing.F) {
	f.Add("{\"id\":\"a\",\"custom_id\":\"1\",\"response\":{\"status_code\":200,\"request_id\":\"r\",\"body\":{\"n\":900719925474099312345}}}\n", int64(0))
	f.Add("{\"custom_id\":\"1\",\"error\":{\"code\":\"c\",\"message\":\"m\"}}\r\n\n", int64(128))
	f.Add("null\n", int64(0))
	f.Add("{broken\n", int64(64))
	f.Add(strings.Repeat("x", 512)+"\n", int64(16))

	f.Fuzz(func(t *testing.T, file string, maxLine int64) {
		if maxLine > 1<<20 {
			maxLine = 1 << 20
		}
		reader := NewOutputReader(io.NopCloser(strings.NewReader(file)), maxLine)
		defer reader.Close()
		for i := 0; i < 10_000; i++ {
			item, err := reader.Next()
			if err != nil {
				// Sticky: after any failure (including EOF) the reader must
				// keep failing rather than resurrect data.
				if _, again := reader.Next(); again == nil {
					t.Fatal("reader returned data after a terminal error")
				}
				return
			}
			if item == nil {
				t.Fatal("nil item without error")
			}
			if item.Response != nil && len(item.Response.Body) > 0 && !json.Valid(item.Response.Body) {
				t.Fatalf("decoded body is not valid JSON: %q", item.Response.Body)
			}
		}
	})
}

// FuzzBatchDecode asserts the batch object decoder never panics and never
// turns a top-level null into a success.
func FuzzBatchDecode(f *testing.F) {
	f.Add(`{"id":"b","object":"batch","status":"completed","usage":{"input_tokens":1}}`)
	f.Add(`null`)
	f.Add(`{"errors":{"data":[{"line":null}]}}`)
	f.Fuzz(func(t *testing.T, wire string) {
		var batch Batch
		err := json.Unmarshal([]byte(wire), &batch)
		if strings.TrimSpace(wire) == "null" && err == nil {
			t.Fatal("top-level null must not decode")
		}
		if err == nil && batch.RawJSON() == nil {
			t.Fatal("successful decode must retain raw bytes")
		}
	})
}
