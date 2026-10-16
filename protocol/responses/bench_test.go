package responses

import (
	"fmt"
	"strings"
	"testing"
)

// These benchmarks are a *baseline*, not a gate: nothing in CI fails on them.
// The Accumulator is the cost centre of the Responses route — every streamed
// event goes through Add, and a terminal event goes through a JSON clone of the
// whole response (replaceResponse).
//
// The fixtures are parsed once per sub-benchmark and reused across iterations,
// so these numbers are Add's cost, not ParseEvent's; wire bytes are reported
// only as throughput context.
//
// Recorded values live in docs/benchmarks.md.
//
// Reproduce with:
//
//	go test ./protocol/responses -run '^$' -bench . -benchmem

// A scripted sequence plus the assertion that proves it actually assembled.
// A benchmark that silently measured rejected events would look fast and mean
// nothing, so every case is verified once before the timer starts.
type benchSequence struct {
	fixtures []string
	verify   func(*Response) error
}

func benchCreated() string {
	return `{"type":"response.created","sequence_number":0,"response":{"id":"resp_bench","object":"response","created_at":1,"status":"in_progress","model":"gpt-4.1-mini","output":[],"parallel_tool_calls":true,"store":false}}`
}

// benchTextSequence is the canonical message flow: create, add item, add
// content part, stream deltas, then close it out with the matching done events
// and a terminal snapshot.
func benchTextSequence(deltas int) benchSequence {
	var text strings.Builder
	for i := 0; i < deltas; i++ {
		text.WriteString(" token")
	}
	assembled := text.String()

	fixtures := []string{
		benchCreated(),
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"message","id":"msg_bench","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg_bench","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
	}
	sequence := 3
	for i := 0; i < deltas; i++ {
		fixtures = append(fixtures, fmt.Sprintf(
			`{"type":"response.output_text.delta","sequence_number":%d,"item_id":"msg_bench","output_index":0,"content_index":0,"delta":" token","logprobs":[]}`,
			sequence))
		sequence++
	}
	fixtures = append(fixtures,
		fmt.Sprintf(`{"type":"response.output_text.done","sequence_number":%d,"item_id":"msg_bench","output_index":0,"content_index":0,"text":%q,"logprobs":[]}`,
			sequence, assembled),
		fmt.Sprintf(`{"type":"response.content_part.done","sequence_number":%d,"item_id":"msg_bench","output_index":0,"content_index":0,"part":{"type":"output_text","text":%q,"annotations":[]}}`,
			sequence+1, assembled),
		fmt.Sprintf(`{"type":"response.output_item.done","sequence_number":%d,"output_index":0,"item":{"type":"message","id":"msg_bench","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}}`,
			sequence+2, assembled),
		fmt.Sprintf(`{"type":"response.completed","sequence_number":%d,"response":{"id":"resp_bench","object":"response","created_at":1,"status":"completed","model":"gpt-4.1-mini","output":[{"type":"message","id":"msg_bench","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}],"parallel_tool_calls":true,"store":false}}`,
			sequence+3, assembled),
	)

	return benchSequence{fixtures: fixtures, verify: func(response *Response) error {
		if response == nil || len(response.Output) != 1 {
			return fmt.Errorf("output = %#v, want one item", response)
		}
		message := response.Output[0].Message
		if message == nil || len(message.Content.Parts) != 1 || message.Content.Parts[0].OutputText == nil {
			return fmt.Errorf("message = %#v", message)
		}
		if got := message.Content.Parts[0].OutputText.Text; got != assembled {
			return fmt.Errorf("text = %q, want %q", got, assembled)
		}
		return nil
	}}
}

// benchReasoningSequence exercises the same stream through the reasoning item,
// which routes through ensureReasoningPart instead of ensureMessagePart.
func benchReasoningSequence(deltas int) benchSequence {
	var text strings.Builder
	for i := 0; i < deltas; i++ {
		text.WriteString(" think")
	}
	assembled := text.String()

	fixtures := []string{
		benchCreated(),
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"reasoning","id":"rs_bench","status":"in_progress","summary":[],"content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":2,"item_id":"rs_bench","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":""}}`,
	}
	sequence := 3
	for i := 0; i < deltas; i++ {
		fixtures = append(fixtures, fmt.Sprintf(
			`{"type":"response.reasoning_text.delta","sequence_number":%d,"item_id":"rs_bench","output_index":0,"content_index":0,"delta":" think"}`,
			sequence))
		sequence++
	}
	fixtures = append(fixtures,
		fmt.Sprintf(`{"type":"response.reasoning_text.done","sequence_number":%d,"item_id":"rs_bench","output_index":0,"content_index":0,"text":%q}`,
			sequence, assembled),
		fmt.Sprintf(`{"type":"response.content_part.done","sequence_number":%d,"item_id":"rs_bench","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":%q}}`,
			sequence+1, assembled),
		fmt.Sprintf(`{"type":"response.output_item.done","sequence_number":%d,"output_index":0,"item":{"type":"reasoning","id":"rs_bench","status":"completed","summary":[],"content":[{"type":"reasoning_text","text":%q}]}}`,
			sequence+2, assembled),
	)

	return benchSequence{fixtures: fixtures, verify: func(response *Response) error {
		if response == nil || len(response.Output) != 1 || response.Output[0].Reasoning == nil {
			return fmt.Errorf("output = %#v, want one reasoning item", response)
		}
		content := response.Output[0].Reasoning.Content
		if len(content) != 1 || content[0].ReasoningText == nil {
			return fmt.Errorf("reasoning content = %#v", content)
		}
		if got := content[0].ReasoningText.Text; got != assembled {
			return fmt.Errorf("text = %q, want %q", got, assembled)
		}
		return nil
	}}
}

// benchUnknownSequence feeds an output item whose type this version does not
// know. Those events are deliberately not assembled — they stay as raw JSON —
// so the interesting cost is the guard checks that decide to skip them.
func benchUnknownSequence(deltas int) benchSequence {
	fixtures := []string{
		benchCreated(),
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"future_item","id":"future_bench"}}`,
	}
	sequence := 2
	for i := 0; i < deltas; i++ {
		fixtures = append(fixtures, fmt.Sprintf(
			`{"type":"response.output_text.delta","sequence_number":%d,"item_id":"future_bench","output_index":0,"content_index":0,"delta":" token","logprobs":[]}`,
			sequence))
		sequence++
	}
	fixtures = append(fixtures, fmt.Sprintf(
		`{"type":"response.completed","sequence_number":%d,"response":{"id":"resp_bench","object":"response","created_at":1,"status":"completed","model":"gpt-4.1-mini","output":[{"type":"future_item","id":"future_bench"}],"parallel_tool_calls":true,"store":false}}`,
		sequence))

	return benchSequence{fixtures: fixtures, verify: func(response *Response) error {
		if response == nil || len(response.Output) != 1 || len(response.Output[0].Raw) == 0 {
			return fmt.Errorf("output = %#v, want one raw-passthrough item", response)
		}
		return nil
	}}
}

// benchEvents parses the fixtures once and returns them with their wire size.
func benchEvents(b *testing.B, fixtures []string) ([]*Event, int) {
	b.Helper()
	events := make([]*Event, 0, len(fixtures))
	wire := 0
	for i, fixture := range fixtures {
		event, err := ParseEvent([]byte(fixture))
		if err != nil {
			b.Fatalf("fixture %d: %v", i, err)
		}
		events = append(events, event)
		wire += len(fixture)
	}
	return events, wire
}

func BenchmarkAccumulatorAdd(b *testing.B) {
	cases := []struct {
		name     string
		deltas   int
		sequence func(int) benchSequence
	}{
		{name: "text/128-deltas", deltas: 128, sequence: benchTextSequence},
		{name: "text/512-deltas", deltas: 512, sequence: benchTextSequence},
		{name: "reasoning/128-deltas", deltas: 128, sequence: benchReasoningSequence},
		{name: "unknown-item/128-deltas", deltas: 128, sequence: benchUnknownSequence},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			script := tc.sequence(tc.deltas)
			events, wire := benchEvents(b, script.fixtures)

			// Untimed control pass: proves the sequence assembles as expected, so
			// the timed loop below cannot be measuring a stream of rejections.
			var control Accumulator
			for i, event := range events {
				if err := control.Add(event); err != nil {
					b.Fatalf("control event %d: %v", i, err)
				}
			}
			if err := script.verify(control.FinalResponse()); err != nil {
				b.Fatalf("control pass: %v", err)
			}

			b.SetBytes(int64(wire))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var accumulator Accumulator
				for _, event := range events {
					if err := accumulator.Add(event); err != nil {
						b.Fatalf("Add: %v", err)
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(events)), "ns/event")
		})
	}
}
