package anthropic

import (
	"encoding/json"
	"errors"
	"io"
	"testing"
)

func TestEventRoundTripKnownAndUnknown(t *testing.T) {
	fixtures := []string{
		`{"type":"message_start","message":{"id":"msg","type":"message","role":"assistant","model":"claude","content":[],"container":null,"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":1,"output_tokens":0,"cache_creation_input_tokens":null,"cache_read_input_tokens":null,"cache_creation":null,"inference_geo":null,"output_tokens_details":null,"server_tool_use":null,"service_tier":null}},"future_event_field":1}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"abc","future_delta":true}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null,"stop_details":null,"container":null},"usage":{"input_tokens":null,"output_tokens":3,"cache_creation_input_tokens":null,"cache_read_input_tokens":null,"output_tokens_details":{"thinking_tokens":2},"server_tool_use":null}}`,
		`{"type":"ping","time":12345678901234567890}`,
		`{"type":"message_stop"}`,
		`{"type":"error","error":{"type":"overloaded_error","message":"busy","retry_after":2}}`,
		`{"type":"future_event","sequence":900719925474099312345,"payload":{"x":1}}`,
	}

	for _, fixture := range fixtures {
		var event Event
		if err := json.Unmarshal([]byte(fixture), &event); err != nil {
			t.Fatalf("Unmarshal %s: %v", fixture, err)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("Marshal %s: %v", fixture, err)
		}
		assertJSONEqual(t, []byte(fixture), encoded)
		if event.Type == "future_event" && (event.Unknown == nil || event.Unknown.Raw == nil) {
			t.Fatalf("unknown event not retained: %#v", event)
		}
	}
}

func TestAccumulatorBuildsOrderedMessage(t *testing.T) {
	fixtures := []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"container":null,"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":11,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"step "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"one"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"complete"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"encrypted-exact"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"","citations":[]}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"hello "}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"page_location","start_page_number":1,"end_page_number":2}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"world"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"ticker\":"}}`,
		`{"type":"future_event","payload":"retained"}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"\"AAPL\",\"n\":900719925474099312345}"}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null,"stop_details":null,"container":{"id":"ctr_1","future":true}},"usage":{"output_tokens":21,"cache_read_input_tokens":5,"future_usage":"x"}}`,
		`{"type":"message_stop"}`,
	}

	accumulator := NewAccumulator()
	accumulator.SetRequestID("req_stream")
	for index, fixture := range fixtures {
		event, err := ParseEvent([]byte(fixture))
		if err != nil {
			t.Fatalf("fixture %d ParseEvent: %v", index, err)
		}
		if err := accumulator.Add(event); err != nil {
			t.Fatalf("fixture %d Add: %v", index, err)
		}
	}

	message, err := accumulator.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if message.RequestID != "req_stream" || len(message.Content) != 4 {
		t.Fatalf("message metadata/content = %#v", message)
	}
	if message.Content[0].Thinking.Thinking != "step one" || message.Content[0].Thinking.Signature != "sig-complete" {
		t.Fatalf("thinking block = %#v", message.Content[0])
	}
	if message.Content[1].RedactedThinking.Data != "encrypted-exact" {
		t.Fatalf("redacted block = %#v", message.Content[1])
	}
	if got := MessageText(message); got != "hello world" {
		t.Fatalf("text = %q", got)
	}
	var citations []json.RawMessage
	if err := json.Unmarshal(message.Content[2].Text.Citations, &citations); err != nil || len(citations) != 1 {
		t.Fatalf("citations = %s, err %v", message.Content[2].Text.Citations, err)
	}
	uses := ToolUses(message)
	if len(uses) != 1 || string(uses[0].Input) != `{"ticker":"AAPL","n":900719925474099312345}` {
		t.Fatalf("tool uses = %#v, raw=%s", uses, uses[0].Input)
	}
	if message.StopReason == nil || *message.StopReason != StopReasonToolUse || message.Usage.OutputTokens != 21 {
		t.Fatalf("stop/usage = %#v / %#v", message.StopReason, message.Usage)
	}
	if len(accumulator.UnknownEvents()) != 1 {
		t.Fatalf("unknown events = %d", len(accumulator.UnknownEvents()))
	}
	if !accumulator.Complete() || accumulator.FinalMessage() == nil {
		t.Fatal("accumulator not complete")
	}
}

func TestAccumulatorMergesMessageDeltaOutputTokenDetails(t *testing.T) {
	accumulator := NewAccumulator()
	applyFixture(t, accumulator, `{"type":"message_start","message":{"id":"msg_usage","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"container":null,"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":11,"output_tokens":1}}}`)

	event, err := ParseEvent([]byte(`{
  "type":"message_delta",
  "delta":{"stop_reason":"end_turn","stop_sequence":null,"stop_details":null,"container":null},
  "usage":{
    "output_tokens":23,
    "output_tokens_details":{"thinking_tokens":7,"future_detail":900719925474099312345}
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	if event.MessageDelta == nil || event.MessageDelta.Usage.OutputTokensDetails == nil {
		t.Fatalf("typed message_delta usage = %#v", event.MessageDelta)
	}
	if err := accumulator.Add(event); err != nil {
		t.Fatal(err)
	}

	// The accumulator owns its snapshot; later caller mutation of the event,
	// including nested raw extension bytes, must not alter final usage.
	event.MessageDelta.Usage.OutputTokensDetails.ThinkingTokens = 99
	event.MessageDelta.Usage.OutputTokensDetails.ExtraFields["future_detail"][0] = '0'

	applyFixture(t, accumulator, `{"type":"message_stop"}`)
	message, err := accumulator.Result()
	if err != nil {
		t.Fatal(err)
	}
	details := message.Usage.OutputTokensDetails
	if message.Usage.OutputTokens != 23 || details == nil || details.ThinkingTokens != 7 {
		t.Fatalf("final usage = %#v", message.Usage)
	}
	if got := string(details.ExtraFields["future_detail"]); got != "900719925474099312345" {
		t.Fatalf("future detail = %s", got)
	}
	if _, leaked := message.Usage.ExtraFields["output_tokens_details"]; leaked {
		t.Fatalf("known output_tokens_details leaked into Usage.ExtraFields: %#v", message.Usage.ExtraFields)
	}
	if _, err := json.Marshal(message); err != nil {
		t.Fatalf("Marshal final message: %v", err)
	}
}

func TestAccumulatorMergesFinalUsageFieldsWithoutExtraFieldConflicts(t *testing.T) {
	accumulator := NewAccumulator()
	applyFixture(t, accumulator, `{"type":"message_start","message":{"id":"msg_usage","type":"message","role":"assistant","model":"claude","content":[],"container":null,"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":11,"output_tokens":1}}}`)
	applyFixture(t, accumulator, `{
  "type":"message_delta",
  "delta":{"stop_reason":"end_turn","stop_sequence":null,"stop_details":null,"container":null},
  "usage":{
    "output_tokens":23,
    "cache_creation":{"ephemeral_1h_input_tokens":7,"ephemeral_5m_input_tokens":3,"future_cache":900719925474099312345},
    "inference_geo":"us",
    "service_tier":"priority",
    "future_usage":900719925474099312345
  }
}`)
	applyFixture(t, accumulator, `{"type":"message_stop"}`)

	message, err := accumulator.Result()
	if err != nil || message == nil {
		t.Fatalf("Result = %#v, %v", message, err)
	}
	if message.Usage.CacheCreation == nil || message.Usage.CacheCreation.Ephemeral1hInputTokens != 7 {
		t.Fatalf("cache creation = %#v", message.Usage.CacheCreation)
	}
	if message.Usage.InferenceGeo == nil || *message.Usage.InferenceGeo != "us" {
		t.Fatalf("inference geo = %#v", message.Usage.InferenceGeo)
	}
	if message.Usage.ServiceTier == nil || *message.Usage.ServiceTier != "priority" {
		t.Fatalf("service tier = %#v", message.Usage.ServiceTier)
	}
	if got := string(message.Usage.ExtraFields["future_usage"]); got != "900719925474099312345" {
		t.Fatalf("future usage = %s", got)
	}
	if _, err := json.Marshal(message); err != nil {
		t.Fatalf("Marshal final message: %v", err)
	}

	// Returned snapshots must not share pointers or raw extension bytes with the
	// accumulator, even for fields promoted from message_delta extras.
	*message.Usage.InferenceGeo = "mutated"
	*message.Usage.ServiceTier = "mutated"
	message.Usage.CacheCreation.ExtraFields["future_cache"][0] = '0'
	message.Usage.ExtraFields["future_usage"][0] = '0'
	second, err := accumulator.Result()
	if err != nil || second == nil {
		t.Fatalf("second Result = %#v, %v", second, err)
	}
	if *second.Usage.InferenceGeo != "us" || *second.Usage.ServiceTier != "priority" {
		t.Fatalf("snapshot pointers leaked: %#v", second.Usage)
	}
	if got := string(second.Usage.CacheCreation.ExtraFields["future_cache"]); got != "900719925474099312345" {
		t.Fatalf("nested snapshot raw leaked: %s", got)
	}
	if got := string(second.Usage.ExtraFields["future_usage"]); got != "900719925474099312345" {
		t.Fatalf("snapshot raw leaked: %s", got)
	}
}

func TestAccumulatorSnapshotsUnstoppedPartialToolJSON(t *testing.T) {
	accumulator := NewAccumulator()
	applyFixture(t, accumulator, `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
	applyFixture(t, accumulator, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"lookup","input":{}}}`)
	applyFixture(t, accumulator, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Par"}}`)

	partial := accumulator.Message()
	if partial == nil || len(partial.Content) != 1 {
		t.Fatalf("partial message = %#v", partial)
	}
	if got := partial.Content[0].PartialJSON; got != `{"city":"Par` {
		t.Fatalf("PartialJSON = %q", got)
	}
	if got := string(partial.Content[0].ToolUse.Input); got != `{}` {
		t.Fatalf("unstopped input was parsed: %s", got)
	}
	partial.Content[0].ToolUse.Input[0] = '['

	applyFixture(t, accumulator, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
	partial, err := accumulator.Result()
	var apiError *APIError
	if !errors.As(err, &apiError) || partial == nil {
		t.Fatalf("Result = %#v, %#v", partial, err)
	}
	if got := partial.Content[0].PartialJSON; got != `{"city":"Par` {
		t.Fatalf("error snapshot PartialJSON = %q", got)
	}
	if got := string(partial.Content[0].ToolUse.Input); got != `{}` {
		t.Fatalf("snapshot input leaked into accumulator: %s", got)
	}
}

func TestDrainStreamCarriesRequestIDAndPartialJSON(t *testing.T) {
	fixtures := []string{
		`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"lookup","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Par"}}`,
	}
	events := make([]*Event, 0, len(fixtures))
	for _, fixture := range fixtures {
		event, err := ParseEvent([]byte(fixture))
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}

	for _, supplied := range []bool{false, true} {
		t.Run(map[bool]string{false: "new accumulator", true: "supplied accumulator"}[supplied], func(t *testing.T) {
			var accumulator *Accumulator
			if supplied {
				accumulator = NewAccumulator()
				accumulator.SetRequestID("stale")
			}
			stream := &scriptedStream{events: events, requestID: "req_drain", terminalErr: io.EOF}
			message, err := DrainStream(stream, accumulator)
			if !errors.Is(err, ErrStreamIncomplete) {
				t.Fatalf("DrainStream error = %v", err)
			}
			if message == nil || message.RequestID != "req_drain" {
				t.Fatalf("message = %#v", message)
			}
			if len(message.Content) != 1 || message.Content[0].PartialJSON != `{"city":"Par` {
				t.Fatalf("partial content = %#v", message.Content)
			}
		})
	}
}

func TestDrainStreamCarriesRequestIDOnSuccessfulCompletion(t *testing.T) {
	fixtures := []string{
		`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
	}
	events := make([]*Event, 0, len(fixtures))
	for _, fixture := range fixtures {
		event, err := ParseEvent([]byte(fixture))
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	message, err := DrainStream(&scriptedStream{events: events, requestID: "req_complete"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if message == nil || message.RequestID != "req_complete" || message.Usage.OutputTokens != 2 {
		t.Fatalf("message = %#v", message)
	}
}

func TestAccumulatorAppliesBetaContextManagementAndCompactionDelta(t *testing.T) {
	accumulator := NewAccumulator()
	applyFixture(t, accumulator, `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0},"context_management":null}}`)
	applyFixture(t, accumulator, `{"type":"content_block_start","index":0,"content_block":{"type":"compaction","content":"","encrypted_content":"stale"}}`)
	applyFixture(t, accumulator, `{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"Summary so far.","encrypted_content":"opaque-next-turn"}}`)
	applyFixture(t, accumulator, `{"type":"content_block_stop","index":0}`)
	applyFixture(t, accumulator, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4},"context_management":{"applied_edits":[{"type":"clear_tool_uses_20250919"}]}}`)
	applyFixture(t, accumulator, `{"type":"message_stop"}`)

	message, err := accumulator.Result()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(message.ExtraFields["context_management"]); got != `{"applied_edits":[{"type":"clear_tool_uses_20250919"}]}` {
		t.Fatalf("context_management = %s", got)
	}
	if len(message.Content) != 1 || message.Content[0].Raw == nil {
		t.Fatalf("content = %#v", message.Content)
	}
	var block struct {
		Type             string `json:"type"`
		Content          string `json:"content"`
		EncryptedContent string `json:"encrypted_content"`
	}
	if err := json.Unmarshal(message.Content[0].Raw, &block); err != nil {
		t.Fatal(err)
	}
	if block.Type != "compaction" || block.Content != "Summary so far." || block.EncryptedContent != "opaque-next-turn" {
		t.Fatalf("compaction block = %#v", block)
	}
	if got := accumulator.UnknownEvents(); len(got) != 1 || string(got[0]) != `{"type":"compaction_delta","content":"Summary so far.","encrypted_content":"opaque-next-turn"}` {
		t.Fatalf("unknown delta retention = %q", got)
	}

	message.Content[0].Raw[0] = '['
	fresh := accumulator.FinalMessage()
	if fresh == nil || fresh.Content[0].Raw[0] != '{' {
		t.Fatal("FinalMessage returned aliased compaction metadata")
	}
}

func TestAccumulatorCompactionDeltaPreservesExplicitNullMetadata(t *testing.T) {
	accumulator := NewAccumulator()
	applyFixture(t, accumulator, `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
	applyFixture(t, accumulator, `{"type":"content_block_start","index":0,"content_block":{"type":"compaction","content":"before","encrypted_content":"before"}}`)
	applyFixture(t, accumulator, `{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":null,"encrypted_content":null}}`)
	applyFixture(t, accumulator, `{"type":"content_block_stop","index":0}`)
	applyFixture(t, accumulator, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`)
	applyFixture(t, accumulator, `{"type":"message_stop"}`)

	message, err := accumulator.Result()
	if err != nil {
		t.Fatal(err)
	}
	var block map[string]json.RawMessage
	if err := json.Unmarshal(message.Content[0].Raw, &block); err != nil {
		t.Fatal(err)
	}
	if string(block["content"]) != "null" || string(block["encrypted_content"]) != "null" {
		t.Fatalf("compaction block = %s", message.Content[0].Raw)
	}
}

func TestAccumulatorFallbackRelabelsModelUsingFinalHop(t *testing.T) {
	accumulator := NewAccumulator()
	applyFixture(t, accumulator, `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"requested-alias","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
	applyFixture(t, accumulator, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"from requested model"}}`)
	applyFixture(t, accumulator, `{"type":"content_block_stop","index":0}`)
	applyFixture(t, accumulator, `{"type":"content_block_start","index":1,"content_block":{"type":"fallback","from":{"model":"requested-alias"},"to":{"model":"claude-sonnet-fallback"},"trigger":{"type":"refusal"}}}`)
	if got := accumulator.Message().Model; got != "claude-sonnet-fallback" {
		t.Fatalf("model after first fallback = %q", got)
	}
	applyFixture(t, accumulator, `{"type":"content_block_stop","index":1}`)
	applyFixture(t, accumulator, `{"type":"content_block_start","index":2,"content_block":{"type":"fallback","from":{"model":"claude-sonnet-fallback"},"to":{"model":"claude-haiku-final"},"trigger":{"type":"refusal"}}}`)
	applyFixture(t, accumulator, `{"type":"content_block_stop","index":2}`)
	applyFixture(t, accumulator, `{"type":"content_block_start","index":3,"content_block":{"type":"text","text":"from final model"}}`)
	applyFixture(t, accumulator, `{"type":"content_block_stop","index":3}`)
	applyFixture(t, accumulator, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}`)
	applyFixture(t, accumulator, `{"type":"message_stop"}`)

	message, err := accumulator.Result()
	if err != nil {
		t.Fatal(err)
	}
	if message.Model != "claude-haiku-final" {
		t.Fatalf("final model = %q", message.Model)
	}
	if len(message.Content) != 4 || message.Content[1].Raw == nil || message.Content[2].Raw == nil {
		t.Fatalf("fallback blocks were not preserved: %#v", message.Content)
	}
}

func TestAccumulatorPreservesInvalidPartialToolJSONAtBlockStop(t *testing.T) {
	accumulator := NewAccumulator()
	applyFixture(t, accumulator, `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
	applyFixture(t, accumulator, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"f","input":{}}}`)
	applyFixture(t, accumulator, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{bad"}}`)
	applyFixture(t, accumulator, `{"type":"content_block_stop","index":0}`)
	applyFixture(t, accumulator, `{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":4}}`)
	applyFixture(t, accumulator, `{"type":"message_stop"}`)

	message, err := accumulator.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if len(message.Content) != 1 || message.Content[0].ToolUse == nil {
		t.Fatalf("content = %#v", message.Content)
	}
	if got := message.Content[0].PartialJSON; got != "{bad" {
		t.Fatalf("PartialJSON = %q", got)
	}
	if got := string(message.Content[0].ToolUse.Input); got != `{}` {
		t.Fatalf("typed input changed to %s", got)
	}
	if _, err := json.Marshal(message); err != nil {
		t.Fatalf("Marshal final message: %v", err)
	}
}

func TestAccumulatorAppliesInputJSONDeltaToUnknownServerToolBlock(t *testing.T) {
	accumulator := NewAccumulator()
	applyFixture(t, accumulator, `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
	applyFixture(t, accumulator, `{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search"}}`)
	applyFixture(t, accumulator, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`)
	applyFixture(t, accumulator, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"claude shannon\"}"}}`)
	applyFixture(t, accumulator, `{"type":"content_block_stop","index":0}`)
	applyFixture(t, accumulator, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`)
	applyFixture(t, accumulator, `{"type":"message_stop"}`)

	message, err := accumulator.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if len(message.Content) != 1 || message.Content[0].Raw == nil {
		t.Fatalf("content = %#v", message.Content)
	}
	if got := message.Content[0].PartialJSON; got != `{"query":"claude shannon"}` {
		t.Fatalf("PartialJSON = %q", got)
	}
	var raw struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(message.Content[0].Raw, &raw); err != nil {
		t.Fatalf("decode raw block: %v", err)
	}
	if raw.Type != "server_tool_use" || raw.ID != "srvtoolu_1" || raw.Name != "web_search" || string(raw.Input) != `{"query":"claude shannon"}` {
		t.Fatalf("raw block = %#v (%s)", raw, message.Content[0].Raw)
	}
	if _, err := json.Marshal(message); err != nil {
		t.Fatalf("Marshal final message: %v", err)
	}
}

func TestAccumulatorReportsPrematureEOFAndStreamError(t *testing.T) {
	accumulator := NewAccumulator()
	applyFixture(t, accumulator, `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
	if _, err := accumulator.Result(); !errors.Is(err, ErrStreamIncomplete) {
		t.Fatalf("Result error = %v, want ErrStreamIncomplete", err)
	}
	applyFixture(t, accumulator, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
	message, err := accumulator.Result()
	if message == nil {
		t.Fatal("partial message should remain available")
	}
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Type != "overloaded_error" {
		t.Fatalf("Result error = %#v", err)
	}
}

func TestAccumulatorRejectsGappedBlockIndexes(t *testing.T) {
	accumulator := NewAccumulator()
	applyFixture(t, accumulator, `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
	applyFixture(t, accumulator, `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"x"}}`)
	applyFixture(t, accumulator, `{"type":"content_block_stop","index":1}`)
	applyFixture(t, accumulator, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`)
	event, _ := ParseEvent([]byte(`{"type":"message_stop"}`))
	if err := accumulator.Add(event); !errors.Is(err, ErrStreamState) {
		t.Fatalf("Add error = %v, want ErrStreamState", err)
	}
}

func TestAccumulatorEnforcesMessageStreamPhases(t *testing.T) {
	const (
		emptyStart   = `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`
		finalDelta   = `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`
		messageStop  = `{"type":"message_stop"}`
		textBlock    = `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
		textBlockEnd = `{"type":"content_block_stop","index":0}`
	)

	t.Run("message_start content must be empty", func(t *testing.T) {
		accumulator := NewAccumulator()
		err := applyFixtureError(t, accumulator, `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"already complete"}],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
		if !errors.Is(err, ErrStreamState) {
			t.Fatalf("Add error = %v, want ErrStreamState", err)
		}
	})

	t.Run("message_stop requires message_delta", func(t *testing.T) {
		accumulator := NewAccumulator()
		applyFixture(t, accumulator, emptyStart)
		err := applyFixtureError(t, accumulator, messageStop)
		if !errors.Is(err, ErrStreamState) {
			t.Fatalf("Add error = %v, want ErrStreamState", err)
		}
	})

	t.Run("message_delta waits for content blocks", func(t *testing.T) {
		accumulator := NewAccumulator()
		applyFixture(t, accumulator, emptyStart)
		applyFixture(t, accumulator, textBlock)
		err := applyFixtureError(t, accumulator, finalDelta)
		if !errors.Is(err, ErrStreamState) {
			t.Fatalf("Add error = %v, want ErrStreamState", err)
		}
	})

	t.Run("content cannot restart after message_delta", func(t *testing.T) {
		accumulator := NewAccumulator()
		applyFixture(t, accumulator, emptyStart)
		applyFixture(t, accumulator, finalDelta)
		err := applyFixtureError(t, accumulator, textBlock)
		if !errors.Is(err, ErrStreamState) {
			t.Fatalf("Add error = %v, want ErrStreamState", err)
		}
	})

	t.Run("multiple message_delta events are valid", func(t *testing.T) {
		accumulator := NewAccumulator()
		applyFixture(t, accumulator, emptyStart)
		applyFixture(t, accumulator, textBlock)
		applyFixture(t, accumulator, textBlockEnd)
		applyFixture(t, accumulator, finalDelta)
		applyFixture(t, accumulator, `{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":2}}`)
		applyFixture(t, accumulator, messageStop)
		message, err := accumulator.Result()
		if err != nil || message.StopReason == nil || *message.StopReason != StopReasonMaxTokens ||
			message.Usage.OutputTokens != 2 {
			t.Fatalf("Result = %#v, %v", message, err)
		}
	})
}

func applyFixture(t *testing.T, accumulator *Accumulator, fixture string) {
	t.Helper()
	event, err := ParseEvent([]byte(fixture))
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if err := accumulator.Add(event); err != nil {
		t.Fatalf("Add: %v", err)
	}
}

func applyFixtureError(t *testing.T, accumulator *Accumulator, fixture string) error {
	t.Helper()
	event, err := ParseEvent([]byte(fixture))
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	return accumulator.Add(event)
}

type scriptedStream struct {
	events      []*Event
	next        int
	requestID   string
	terminalErr error
}

func (stream *scriptedStream) Recv() (*Event, error) {
	if stream.next < len(stream.events) {
		event := stream.events[stream.next]
		stream.next++
		return event, nil
	}
	if stream.terminalErr != nil {
		return nil, stream.terminalErr
	}
	return nil, io.EOF
}

func (stream *scriptedStream) Close() error                   { return nil }
func (stream *scriptedStream) RequestID() string              { return stream.requestID }
func (stream *scriptedStream) FinalMessage() *MessageResponse { return nil }
