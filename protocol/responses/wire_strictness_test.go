package responses

import (
	"errors"
	"strings"
	"testing"
)

// Go's encoding/json matches struct tags case-insensitively and decodes both a
// missing member and an explicit null to the zero value. Decoding events
// straight into a struct therefore accepted payloads no OpenAI stream produces,
// and turned them into confident-looking results: an "empty response.completed"
// became a successful terminal response whose ID and status were both "".
func TestParseEvent_RejectsPayloadsThatAreNotThisProtocol(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    error
	}{{
		name:    "case-variant discriminator is not a discriminator",
		payload: `{"TYPE":"response.completed","sequence_number":1,"response":{"id":"r","status":"completed"}}`,
		want:    ErrInvalidUnion,
	}, {
		name:    "case-variant known field",
		payload: `{"type":"response.completed","sequence_number":1,"SEQUENCE_NUMBER":2,"response":{"id":"r","status":"completed"}}`,
		want:    ErrInvalidWire,
	}, {
		name:    "missing sequence_number is not sequence 0",
		payload: `{"type":"response.completed","response":{"id":"r","status":"completed"}}`,
		want:    ErrInvalidWire,
	}, {
		name:    "null sequence_number is not sequence 0",
		payload: `{"type":"response.completed","sequence_number":null,"response":{"id":"r","status":"completed"}}`,
		want:    ErrInvalidWire,
	}, {
		name:    "empty response object is not a completed response",
		payload: `{"type":"response.completed","sequence_number":1,"response":{}}`,
		want:    ErrInvalidWire,
	}, {
		name:    "null response id",
		payload: `{"type":"response.completed","sequence_number":1,"response":{"id":null,"status":"completed"}}`,
		want:    ErrInvalidWire,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseEvent([]byte(tc.payload))
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseEvent error = %v, want %v", err, tc.want)
			}
		})
	}
}

// A lifecycle event's type and its payload status are read by different
// callers — Accumulator.Add terminates on the type, Response.IsTerminal reads
// the status — so letting them disagree produced an accumulator that was
// terminal while its own FinalResponse said it was not.
func TestParseEvent_RejectsTypeStatusDisagreement(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.completed","sequence_number":1,"response":{"id":"r","status":"in_progress"}}`,
		`{"type":"response.failed","sequence_number":1,"response":{"id":"r","status":"queued"}}`,
		`{"type":"response.in_progress","sequence_number":1,"response":{"id":"r","status":"completed"}}`,
	} {
		_, err := ParseEvent([]byte(payload))
		if !errors.Is(err, ErrInvalidWire) {
			t.Errorf("ParseEvent(%s) error = %v, want ErrInvalidWire", payload, err)
		}
		if err != nil && !strings.Contains(err.Error(), "terminal") {
			t.Errorf("error should explain the terminal disagreement, got %v", err)
		}
	}
}

// The strictness above must not reach events this package does not model: an
// unknown type is kept as Raw, where a case-variant key claims nothing about
// this schema.
func TestParseEvent_KeepsValidAndUnknownEventsWorking(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"r","status":"queued"}}`,
		`{"type":"response.in_progress","sequence_number":1,"response":{"id":"r","status":"in_progress"}}`,
		`{"type":"response.completed","sequence_number":2,"response":{"id":"r","status":"completed"}}`,
		`{"type":"response.failed","sequence_number":3,"response":{"id":"r","status":"failed"}}`,
		`{"type":"response.incomplete","sequence_number":4,"response":{"id":"r","status":"incomplete"}}`,
		`{"type":"some.future.event","sequence_number":5,"TYPE":"still fine here"}`,
	} {
		if _, err := ParseEvent([]byte(payload)); err != nil {
			t.Errorf("ParseEvent(%s) = %v, want success", payload, err)
		}
	}
}

// A rejected event must leave nothing behind. flushPendingContentParts deletes
// map entries and appends parts as it goes, and Go randomizes map iteration
// order, so a mid-loop failure used to strand a different subset of pending
// parts on every run.
func TestAccumulator_RejectedEventLeavesStateUnchanged(t *testing.T) {
	var acc Accumulator
	apply := func(t *testing.T, payload string) {
		t.Helper()
		event, err := ParseEvent([]byte(payload))
		if err != nil {
			t.Fatalf("ParseEvent(%s): %v", payload, err)
		}
		if err := acc.Add(event); err != nil {
			t.Fatalf("Add(%s): %v", payload, err)
		}
	}

	apply(t, `{"type":"response.created","sequence_number":0,"response":{"id":"r","status":"in_progress"}}`)
	apply(t, `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","status":"in_progress","content":[]}}`)
	apply(t, `{"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"hello"}`)

	before := acc.Response()
	beforeText := before.Output[0].Message.Content.Parts[0].OutputText.Text

	// A mismatched item_id must be refused outright.
	event, err := ParseEvent([]byte(`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_OTHER","output_index":0,"content_index":0,"delta":" world"}`))
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if err := acc.Add(event); err == nil {
		t.Fatal("Add accepted a mismatched item_id")
	}

	after := acc.Response()
	if got := after.Output[0].Message.Content.Parts[0].OutputText.Text; got != beforeText {
		t.Errorf("rejected event changed accumulated text: %q -> %q", beforeText, got)
	}
	if len(after.Output) != len(before.Output) {
		t.Errorf("rejected event changed output length: %d -> %d", len(before.Output), len(after.Output))
	}
}

// A rejected event must not grow the accumulator. The damage is not a stray
// empty item: an appended placeholder changes len(Output), so a later *valid*
// event for that same output_index takes the "already exists" branch instead of
// the "create" one, and the stream silently reconstructs into the wrong shape.
func TestAccumulator_RejectedEventDoesNotCreateOutputSlots(t *testing.T) {
	var acc Accumulator
	mustAdd := func(t *testing.T, payload string) {
		t.Helper()
		event, err := ParseEvent([]byte(payload))
		if err != nil {
			t.Fatalf("ParseEvent(%s): %v", payload, err)
		}
		if err := acc.Add(event); err != nil {
			t.Fatalf("Add(%s): %v", payload, err)
		}
	}
	reject := func(t *testing.T, payload string) {
		t.Helper()
		event, err := ParseEvent([]byte(payload))
		if err != nil {
			return // rejected at parse time, nothing reached the accumulator
		}
		if err := acc.Add(event); err == nil {
			t.Fatalf("Add(%s) was accepted, want rejection", payload)
		}
	}

	mustAdd(t, `{"type":"response.created","sequence_number":0,"response":{"id":"r","status":"in_progress"}}`)

	// Every one of these fails only after ensure* has had to create something.
	reject(t, `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":-1,"delta":"x"}`)
	reject(t, `{"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":7,"delta":"x"}`)
	reject(t, `{"type":"response.reasoning_summary_text.delta","sequence_number":3,"item_id":"rsn_1","output_index":0,"summary_index":9,"delta":"x"}`)

	if got := len(acc.Response().Output); got != 0 {
		t.Fatalf("rejected events created %d output slot(s), want 0: %+v", got, acc.Response().Output)
	}

	// The slot must still be free for a genuine item at the same index.
	mustAdd(t, `{"type":"response.output_item.added","sequence_number":4,"output_index":0,"item":{"type":"message","id":"msg_real","role":"assistant","status":"in_progress","content":[]}}`)
	mustAdd(t, `{"type":"response.output_text.delta","sequence_number":5,"item_id":"msg_real","output_index":0,"content_index":0,"delta":"ok"}`)

	output := acc.Response().Output
	if len(output) != 1 {
		t.Fatalf("output length = %d, want 1: %+v", len(output), output)
	}
	if output[0].Message == nil || output[0].Message.ID != "msg_real" {
		t.Fatalf("output[0] is not the real message: %+v", output[0])
	}
	if got := output[0].Message.Content.Parts[0].OutputText.Text; got != "ok" {
		t.Errorf("accumulated text = %q, want %q", got, "ok")
	}
}

// The response itself must not spring into existence for a rejected event.
func TestAccumulator_RejectedFirstEventLeavesNoResponse(t *testing.T) {
	var acc Accumulator
	event, err := ParseEvent([]byte(`{"type":"response.output_text.delta","sequence_number":1,"item_id":"m","output_index":0,"content_index":-1,"delta":"x"}`))
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if err := acc.Add(event); err == nil {
		t.Fatal("Add accepted a negative content_index")
	}
	if acc.Response() != nil {
		t.Fatalf("a rejected first event created a response: %+v", acc.Response())
	}
}
