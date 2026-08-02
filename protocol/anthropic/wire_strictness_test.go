package anthropic

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func applyRaw(t *testing.T, accumulator *Accumulator, fixture string) error {
	t.Helper()
	event, err := ParseEvent([]byte(fixture))
	if err != nil {
		return err
	}
	return accumulator.Add(event)
}

func TestMessageResponseRequiresStableWireFields(t *testing.T) {
	cases := map[string]string{
		"missing id":            `{"type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":0,"output_tokens":0}}`,
		"null id":               `{"id":null,"type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":0,"output_tokens":0}}`,
		"empty id":              `{"id":"","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":0,"output_tokens":0}}`,
		"missing type":          `{"id":"m","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":0,"output_tokens":0}}`,
		"wrong type":            `{"id":"m","type":"future_message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":0,"output_tokens":0}}`,
		"missing role":          `{"id":"m","type":"message","model":"claude","content":[],"usage":{"input_tokens":0,"output_tokens":0}}`,
		"request role":          `{"id":"m","type":"message","role":"user","model":"claude","content":[],"usage":{"input_tokens":0,"output_tokens":0}}`,
		"missing model":         `{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":0,"output_tokens":0}}`,
		"empty model":           `{"id":"m","type":"message","role":"assistant","model":"","content":[],"usage":{"input_tokens":0,"output_tokens":0}}`,
		"missing content":       `{"id":"m","type":"message","role":"assistant","model":"claude","usage":{"input_tokens":0,"output_tokens":0}}`,
		"null content":          `{"id":"m","type":"message","role":"assistant","model":"claude","content":null,"usage":{"input_tokens":0,"output_tokens":0}}`,
		"missing usage":         `{"id":"m","type":"message","role":"assistant","model":"claude","content":[]}`,
		"null usage":            `{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"usage":null}`,
		"missing input tokens":  `{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"usage":{"output_tokens":0}}`,
		"null input tokens":     `{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":null,"output_tokens":0}}`,
		"missing output tokens": `{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":0}}`,
		"null output tokens":    `{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":0,"output_tokens":null}}`,
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			var response MessageResponse
			if err := json.Unmarshal([]byte(fixture), &response); !errors.Is(err, ErrInvalidWire) {
				t.Fatalf("Unmarshal error = %v, want ErrInvalidWire", err)
			}
		})
	}
}

func TestMessageResponseAcceptsZeroUsageAndPreservesFutureFields(t *testing.T) {
	const fixture = `{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":0,"output_tokens":0,"future_usage":true},"future_message":{"x":1}}`
	var response MessageResponse
	if err := json.Unmarshal([]byte(fixture), &response); err != nil {
		t.Fatalf("Unmarshal zero usage: %v", err)
	}
	if response.Usage.InputTokens != 0 || response.Usage.OutputTokens != 0 {
		t.Fatalf("usage = %+v, want legitimate zero counters", response.Usage)
	}
	if string(response.Usage.ExtraFields["future_usage"]) != "true" ||
		string(response.ExtraFields["future_message"]) != `{"x":1}` {
		t.Fatalf("future fields lost: response=%s usage=%s",
			response.ExtraFields["future_message"], response.Usage.ExtraFields["future_usage"])
	}
}

func TestMessageParamStillAcceptsBothRequestRoles(t *testing.T) {
	for _, role := range []Role{RoleUser, RoleAssistant} {
		var message MessageParam
		fixture := []byte(`{"role":"` + string(role) + `","content":"hello"}`)
		if err := json.Unmarshal(fixture, &message); err != nil {
			t.Fatalf("Unmarshal request role %q: %v", role, err)
		}
		if message.Role != role {
			t.Fatalf("request role = %q, want %q", message.Role, role)
		}
	}
}

func TestParseEventRejectsMalformedMessageStartResponse(t *testing.T) {
	for name, fixture := range map[string]string{
		"missing message type": `{"type":"message_start","message":{"id":"m","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":0,"output_tokens":0}}}`,
		"wrong message type":   `{"type":"message_start","message":{"id":"m","type":"future_message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":0,"output_tokens":0}}}`,
		"wrong response role":  `{"type":"message_start","message":{"id":"m","type":"message","role":"user","model":"claude","content":[],"usage":{"input_tokens":0,"output_tokens":0}}}`,
		"missing usage":        `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseEvent([]byte(fixture)); !errors.Is(err, ErrInvalidWire) {
				t.Fatalf("ParseEvent error = %v, want ErrInvalidWire", err)
			}
		})
	}
}

// A message_start missing every stable identity field used to accumulate into a
// message that completed normally with an empty ID, model and role — a result
// that looks successful and cannot be acted on.
func TestAccumulator_RejectsMessageStartWithoutIdentity(t *testing.T) {
	cases := map[string]string{
		"no id":    `{"type":"message_start","message":{"type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		"no model": `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		"no role":  `{"type":"message_start","message":{"id":"m","type":"message","model":"claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		"null id":  `{"type":"message_start","message":{"id":null,"type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		"nothing":  `{"type":"message_start","message":{"type":"message","content":[]}}`,
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			if err := applyRaw(t, NewAccumulator(), fixture); !errors.Is(err, ErrInvalidWire) {
				t.Fatalf("Add error = %v, want ErrInvalidWire", err)
			}
		})
	}
}

// index addresses the content block. Absent or null it decoded to 0, silently
// aliasing whatever block 0 happened to be.
func TestAccumulator_RequiresContentBlockIndex(t *testing.T) {
	const start = `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`
	for name, fixture := range map[string]string{
		"null start index":  `{"type":"content_block_start","index":null,"content_block":{"type":"text","text":"x"}}`,
		"no start index":    `{"type":"content_block_start","content_block":{"type":"text","text":"x"}}`,
		"null delta index":  `{"type":"content_block_delta","index":null,"delta":{"type":"text_delta","text":"x"}}`,
		"no stop index":     `{"type":"content_block_stop"}`,
		"null message stop": `{"type":"content_block_stop","index":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			accumulator := NewAccumulator()
			if err := applyRaw(t, accumulator, start); err != nil {
				t.Fatalf("message_start: %v", err)
			}
			if err := applyRaw(t, accumulator, fixture); !errors.Is(err, ErrInvalidWire) {
				t.Fatalf("Add error = %v, want ErrInvalidWire", err)
			}
		})
	}
}

// json.Valid is not enough for the Raw escape hatches: null, arrays, strings
// and numbers are all valid JSON, and emitting them would produce a wire
// payload no Anthropic decoder — including this one — can read back.
func TestRawEscapeHatchesRequireJSONObjects(t *testing.T) {
	for _, payload := range []string{"null", "[1,2]", `"a string"`, "42", "true"} {
		t.Run("block/"+payload, func(t *testing.T) {
			if _, err := (ContentBlock{Raw: []byte(payload)}).MarshalJSON(); !errors.Is(err, ErrInvalidWire) {
				t.Fatalf("ContentBlock.MarshalJSON error = %v, want ErrInvalidWire", err)
			}
		})
		t.Run("delta/"+payload, func(t *testing.T) {
			if _, err := (ContentDelta{Unknown: []byte(payload)}).MarshalJSON(); !errors.Is(err, ErrInvalidWire) {
				t.Fatalf("ContentDelta.MarshalJSON error = %v, want ErrInvalidWire", err)
			}
		})
		t.Run("event/"+payload, func(t *testing.T) {
			event := Event{Type: "future.event", Unknown: &UnknownEvent{Raw: []byte(payload)}}
			if _, err := event.MarshalJSON(); !errors.Is(err, ErrInvalidWire) {
				t.Fatalf("Event.MarshalJSON error = %v, want ErrInvalidWire", err)
			}
		})
	}

	// A genuine unknown object still round-trips: strictness must not close the
	// forward-compatibility escape hatch it protects.
	if _, err := (ContentBlock{Raw: []byte(`{"type":"future","x":1}`)}).MarshalJSON(); err != nil {
		t.Fatalf("unknown object block must still marshal: %v", err)
	}
}

// message_delta.delta carries the members that revise the final message, so an
// unrecognized one describes the completed message just as much as stop_reason
// does. Dropping it broke both forward compatibility and the guarantee that a
// streamed terminal message equals the non-streamed response.
func TestAccumulator_MergesUnknownDeltaFieldsIntoFinalMessage(t *testing.T) {
	accumulator := NewAccumulator()
	for _, fixture := range []string{
		`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null,"future_delta_field":{"nested":true}},"usage":{"output_tokens":1}}`,
		`{"type":"message_stop"}`,
	} {
		if err := applyRaw(t, accumulator, fixture); err != nil {
			t.Fatalf("apply %s: %v", fixture, err)
		}
	}

	message, err := accumulator.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	encoded, err := message.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal terminal message: %v", err)
	}
	if !strings.Contains(string(encoded), `"future_delta_field":{"nested":true}`) {
		t.Errorf("unknown delta member was dropped from the final message: %s", encoded)
	}
}

// A delta member colliding with a first-class MessageResponse field cannot be
// carried as an extra: MarshalJSON would then reject the entire message. The
// typed fields already carry everything modeled, so the merge must skip it
// rather than produce a message that cannot be serialized.
func TestAccumulator_DeltaCollisionKeepsMessageSerializable(t *testing.T) {
	accumulator := NewAccumulator()
	for _, fixture := range []string{
		`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null,"id":"not-the-real-id","usage":{"bogus":1}},"usage":{"output_tokens":1}}`,
		`{"type":"message_stop"}`,
	} {
		if err := applyRaw(t, accumulator, fixture); err != nil {
			t.Fatalf("apply %s: %v", fixture, err)
		}
	}

	message, err := accumulator.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	encoded, err := message.MarshalJSON()
	if err != nil {
		t.Fatalf("a colliding delta member must not make the message unserializable: %v", err)
	}
	if message.ID != "m" {
		t.Errorf("delta must not overwrite the message ID, got %q", message.ID)
	}
	if strings.Contains(string(encoded), "not-the-real-id") {
		t.Errorf("colliding delta member leaked into the message: %s", encoded)
	}
}
