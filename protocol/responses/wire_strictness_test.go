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

func TestAccumulator_RejectedEventRestoresExistingEmptyItemID(t *testing.T) {
	t.Run("message", func(t *testing.T) {
		for _, test := range []struct {
			name         string
			content      MessageContent
			contentIndex int
			wantPartsNil bool
		}{
			{name: "negative index", content: NewPartContent(), contentIndex: -1},
			{name: "skipped index", content: MessageContent{}, contentIndex: 2, wantPartsNil: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				acc := Accumulator{response: &Response{Output: []Item{NewMessageItem(Message{
					Role: "assistant", Status: StatusInProgress, Content: test.content,
				})}}}
				event := &Event{
					Type: EventTypeResponseOutputTextDelta,
					OutputTextDelta: &TextDeltaEvent{
						ItemID: "msg_new", OutputIndex: 0, ContentIndex: test.contentIndex, Delta: "x",
					},
				}
				if err := acc.Add(event); err == nil {
					t.Fatal("Add accepted an invalid content_index")
				}
				message := acc.Response().Output[0].Message
				if message.ID != "" {
					t.Fatalf("rejected event changed message ID to %q", message.ID)
				}
				if got := message.Content.Parts == nil; got != test.wantPartsNil {
					t.Fatalf("message parts nil = %t, want %t", got, test.wantPartsNil)
				}
				if len(message.Content.Parts) != 0 {
					t.Fatalf("rejected event left message parts behind: %#v", message.Content.Parts)
				}
			})
		}
	})

	t.Run("reasoning", func(t *testing.T) {
		for _, test := range []struct {
			name         string
			contentIndex int
		}{
			{name: "negative index", contentIndex: -1},
			{name: "skipped index", contentIndex: 2},
		} {
			t.Run(test.name, func(t *testing.T) {
				acc := Accumulator{response: &Response{Output: []Item{NewReasoningItem(Reasoning{
					Status: StatusInProgress, Content: []ContentPart{},
				})}}}
				event := &Event{
					Type: EventTypeResponseReasoningTextDelta,
					ReasoningTextDelta: &ReasoningTextDeltaEvent{
						ItemID: "rs_new", OutputIndex: 0, ContentIndex: test.contentIndex, Delta: "x",
					},
				}
				if err := acc.Add(event); err == nil {
					t.Fatal("Add accepted an invalid content_index")
				}
				reasoning := acc.Response().Output[0].Reasoning
				if reasoning.ID != "" {
					t.Fatalf("rejected event changed reasoning ID to %q", reasoning.ID)
				}
				if len(reasoning.Content) != 0 {
					t.Fatalf("rejected event left reasoning parts behind: %#v", reasoning.Content)
				}
			})
		}
	})
}

func TestAccumulator_RejectedEventRollsBackContentPartAppend(t *testing.T) {
	acc := Accumulator{
		response: &Response{Output: []Item{NewMessageItem(Message{
			ID: "msg_1", Role: "assistant", Status: StatusInProgress, Content: NewPartContent(),
		})}},
		pendingContentParts: map[pendingContentPartKey]pendingContentPart{
			{outputIndex: 0, contentIndex: 1}: {
				itemID: "msg_other", part: ContentPart{Type: "future_part"},
			},
		},
	}
	event := &Event{
		Type: EventTypeResponseContentPartAdded,
		ContentPart: &ContentPartEvent{
			ItemID: "msg_1", OutputIndex: 0, ContentIndex: 0, Part: NewOutputTextPart("new"),
		},
	}
	if err := acc.Add(event); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Add mismatch error = %v", err)
	}
	parts := acc.Response().Output[0].Message.Content.Parts
	if len(parts) != 0 {
		t.Fatalf("rejected event left appended content behind: %#v", parts)
	}
	key := pendingContentPartKey{outputIndex: 0, contentIndex: 1}
	if pending, ok := acc.pendingContentParts[key]; !ok || pending.itemID != "msg_other" {
		t.Fatalf("rejected event changed pending part: %#v", acc.pendingContentParts)
	}
}

func TestAccumulator_RejectedEventRestoresFlushedPendingParts(t *testing.T) {
	key := pendingContentPartKey{outputIndex: 0, contentIndex: 0}
	acc := Accumulator{
		response: &Response{Output: []Item{NewMessageItem(Message{
			ID: "msg_1", Role: "assistant", Status: StatusInProgress, Content: NewPartContent(),
		})}},
		pendingContentParts: map[pendingContentPartKey]pendingContentPart{
			key: {itemID: "msg_1", part: ContentPart{Type: "future_part"}},
		},
	}
	event := &Event{
		Type: EventTypeResponseOutputTextDelta,
		OutputTextDelta: &TextDeltaEvent{
			ItemID: "msg_1", OutputIndex: 0, ContentIndex: -1, Delta: "x",
		},
	}
	if err := acc.Add(event); err == nil {
		t.Fatal("Add accepted a negative content_index")
	}
	if parts := acc.Response().Output[0].Message.Content.Parts; len(parts) != 0 {
		t.Fatalf("rejected event left flushed content behind: %#v", parts)
	}
	if pending, ok := acc.pendingContentParts[key]; !ok || pending.itemID != "msg_1" || pending.part.Type != "future_part" {
		t.Fatalf("rejected event did not restore pending part: %#v", acc.pendingContentParts)
	}
}

func TestSetContentPartRollbackRestoresAppendAndReplace(t *testing.T) {
	t.Run("append", func(t *testing.T) {
		parts := make([]ContentPart, 1, 1)
		parts[0] = NewOutputTextPart("existing")
		message := Message{Content: NewPartContent(parts...)}
		original := message.Content.Parts
		originalSlot := &message.Content.Parts[0]
		var undo rollback
		if err := setContentPart(&message, 1, NewOutputTextPart("new"), &undo); err != nil {
			t.Fatal(err)
		}
		if cap(message.Content.Parts) == cap(original) {
			t.Fatal("test setup did not grow the content slice")
		}
		undo.run()
		if len(message.Content.Parts) != len(original) || cap(message.Content.Parts) != cap(original) {
			t.Fatalf("rollback slice = len %d cap %d, want len %d cap %d",
				len(message.Content.Parts), cap(message.Content.Parts), len(original), cap(original))
		}
		if &message.Content.Parts[0] != originalSlot {
			t.Fatal("rollback did not restore the original backing array")
		}
		if got := message.Content.Parts[0].OutputText.Text; got != "existing" {
			t.Fatalf("rollback changed existing part to %q", got)
		}
	})

	t.Run("replace", func(t *testing.T) {
		message := Message{Content: NewPartContent(NewOutputTextPart("old"))}
		var undo rollback
		if err := setContentPart(&message, 0, NewOutputTextPart("new"), &undo); err != nil {
			t.Fatal(err)
		}
		undo.run()
		part := message.Content.Parts[0]
		if part.OutputText == nil || part.OutputText.Text != "old" {
			t.Fatalf("rollback did not restore replaced part: %#v", part)
		}
	})
}

func TestAccumulatorReasoningContentPartReplacementIsRollbackTracked(t *testing.T) {
	for _, test := range []struct {
		name    string
		summary bool
	}{
		{name: "reasoning text"},
		{name: "summary text", summary: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reasoning := Reasoning{ID: "rs_1", Status: StatusInProgress}
			part := NewReasoningTextPart("new")
			if test.summary {
				reasoning.Summary = []ContentPart{NewSummaryTextPart("old")}
				part = NewSummaryTextPart("new")
			} else {
				reasoning.Content = []ContentPart{NewReasoningTextPart("old")}
			}
			acc := Accumulator{response: &Response{Output: []Item{NewReasoningItem(reasoning)}}}
			event := &Event{
				Type: EventTypeResponseContentPartAdded,
				ContentPart: &ContentPartEvent{
					ItemID: "rs_1", OutputIndex: 0, ContentIndex: 0, Part: part,
				},
			}

			var undo rollback
			if err := acc.apply(event, &undo); err != nil {
				t.Fatalf("apply: %v", err)
			}
			var got string
			if test.summary {
				got = acc.response.Output[0].Reasoning.Summary[0].SummaryText.Text
			} else {
				got = acc.response.Output[0].Reasoning.Content[0].ReasoningText.Text
			}
			if got != "new" {
				t.Fatalf("applied text = %q, want new", got)
			}

			undo.run()
			if test.summary {
				got = acc.response.Output[0].Reasoning.Summary[0].SummaryText.Text
			} else {
				got = acc.response.Output[0].Reasoning.Content[0].ReasoningText.Text
			}
			if got != "old" {
				t.Fatalf("rolled-back text = %q, want old", got)
			}
		})
	}
}

func TestReplaceContentPartTrackedRollbackSurvivesSliceGrowth(t *testing.T) {
	parts := make([]ContentPart, 1, 1)
	parts[0] = NewOutputTextPart("old")
	originalSlot := &parts[0]
	var undo rollback
	replaceContentPartTracked(&parts, 0, NewOutputTextPart("new"), &undo)

	parts = append(parts, NewOutputTextPart("later"))
	if &parts[0] == originalSlot {
		t.Fatal("test setup did not reallocate the parts slice")
	}
	undo.run()

	if got := parts[0].OutputText.Text; got != "old" {
		t.Fatalf("rolled-back text after slice growth = %q, want old", got)
	}
	if got := parts[1].OutputText.Text; got != "later" {
		t.Fatalf("rollback unexpectedly changed appended part to %q", got)
	}
}

func TestAccumulatorRejectedPartInitializationLeavesStateUnchanged(t *testing.T) {
	key := pendingContentPartKey{outputIndex: 0, contentIndex: 1}

	t.Run("message", func(t *testing.T) {
		acc := Accumulator{
			response: &Response{Output: []Item{NewMessageItem(Message{
				ID: "msg_1", Role: "assistant", Status: StatusInProgress, Content: NewPartContent(),
			})}},
			pendingContentParts: map[pendingContentPartKey]pendingContentPart{
				key: {itemID: "msg_other", part: ContentPart{Type: "future_part"}},
			},
		}
		event := &Event{
			Type: EventTypeResponseOutputTextDelta,
			OutputTextDelta: &TextDeltaEvent{
				ItemID: "msg_1", OutputIndex: 0, ContentIndex: 0, Delta: "new",
			},
		}
		if err := acc.Add(event); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("Add mismatch error = %v", err)
		}
		if parts := acc.response.Output[0].Message.Content.Parts; len(parts) != 0 {
			t.Fatalf("rejected event left message parts behind: %#v", parts)
		}
		if pending, ok := acc.pendingContentParts[key]; !ok || pending.itemID != "msg_other" {
			t.Fatalf("rejected event changed pending part: %#v", acc.pendingContentParts)
		}
	})

	t.Run("reasoning", func(t *testing.T) {
		acc := Accumulator{
			response: &Response{Output: []Item{NewReasoningItem(Reasoning{
				ID: "rs_1", Status: StatusInProgress, Content: []ContentPart{},
			})}},
			pendingContentParts: map[pendingContentPartKey]pendingContentPart{
				key: {itemID: "rs_other", part: ContentPart{Type: "future_part"}},
			},
		}
		event := &Event{
			Type: EventTypeResponseReasoningTextDelta,
			ReasoningTextDelta: &ReasoningTextDeltaEvent{
				ItemID: "rs_1", OutputIndex: 0, ContentIndex: 0, Delta: "new",
			},
		}
		if err := acc.Add(event); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("Add mismatch error = %v", err)
		}
		if parts := acc.response.Output[0].Reasoning.Content; len(parts) != 0 {
			t.Fatalf("rejected event left reasoning parts behind: %#v", parts)
		}
		if pending, ok := acc.pendingContentParts[key]; !ok || pending.itemID != "rs_other" {
			t.Fatalf("rejected event changed pending part: %#v", acc.pendingContentParts)
		}
	})
}
