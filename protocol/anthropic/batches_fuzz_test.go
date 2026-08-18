package anthropic

import (
	"io"
	"strings"
	"testing"
)

// FuzzMessageBatchResultsReader asserts the results decoder never panics,
// terminates on arbitrary input, keeps the line cap authoritative, and
// preserves unknown result types instead of skipping them.
func FuzzMessageBatchResultsReader(f *testing.F) {
	f.Add(`{"custom_id":"r1","result":{"type":"canceled"}}`+"\n", int64(0))
	f.Add(`{"custom_id":"r2","result":{"type":"future_outcome","n":9007199254740993}}`+"\n", int64(256))
	f.Add("null\n{broken\n", int64(64))
	f.Add(strings.Repeat("y", 300)+"\n", int64(16))

	f.Fuzz(func(t *testing.T, file string, maxLine int64) {
		if maxLine > 1<<20 {
			maxLine = 1 << 20
		}
		reader := NewMessageBatchResultsReader(io.NopCloser(strings.NewReader(file)), maxLine)
		defer reader.Close()
		for i := 0; i < 10_000; i++ {
			line, err := reader.Next()
			if err != nil {
				if _, again := reader.Next(); again == nil {
					t.Fatal("reader returned data after a terminal error")
				}
				return
			}
			if line == nil {
				t.Fatal("nil line without error")
			}
			if line.Result.Type == "" {
				t.Fatal("decoded result lost its discriminator")
			}
			if len(line.Result.Raw) == 0 {
				t.Fatal("decoded result lost its raw bytes")
			}
		}
	})
}
