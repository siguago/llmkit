package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestTerminalEventInstructionsUnionAndFractionalTimestamps(t *testing.T) {
	tests := []struct {
		name         string
		instructions string
		check        func(*testing.T, Instructions)
	}{
		{
			name: "string", instructions: `"be concise"`,
			check: func(t *testing.T, instructions Instructions) {
				if instructions.Text == nil || *instructions.Text != "be concise" || instructions.Items != nil || instructions.Null {
					t.Fatalf("string instructions = %#v", instructions)
				}
			},
		},
		{
			name:         "item list",
			instructions: `[{"type":"message","role":"developer","content":[{"type":"input_text","text":"be concise"}]}]`,
			check: func(t *testing.T, instructions Instructions) {
				if len(instructions.Items) != 1 || instructions.Items[0].Message == nil || instructions.Items[0].Message.Role != "developer" {
					t.Fatalf("list instructions = %#v", instructions)
				}
			},
		},
		{
			name: "null", instructions: `null`,
			check: func(t *testing.T, instructions Instructions) {
				if !instructions.Null || instructions.Text != nil || instructions.Items != nil || len(instructions.Raw) != 0 {
					t.Fatalf("null instructions = %#v", instructions)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire := fmt.Sprintf(`{
  "type":"response.completed","sequence_number":7,
  "response":{"id":"resp_fractional","object":"response","created_at":1.25,"completed_at":2.75,"status":"completed","instructions":%s,"model":"gpt-test","output":[],"parallel_tool_calls":true,"store":false}
}`, test.instructions)
			event := mustEvent(t, wire)
			if event.Response == nil || event.Response.CreatedAt != 1.25 || event.Response.CompletedAt == nil || *event.Response.CompletedAt != 2.75 {
				t.Fatalf("fractional timestamps = %#v", event.Response)
			}
			test.check(t, event.Response.Instructions)

			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			roundTripped := mustEvent(t, string(encoded))
			test.check(t, roundTripped.Response.Instructions)
			if roundTripped.Response.CreatedAt != 1.25 || roundTripped.Response.CompletedAt == nil || *roundTripped.Response.CompletedAt != 2.75 {
				t.Fatalf("round-trip timestamps = %#v", roundTripped.Response)
			}

			var accumulator Accumulator
			if err := accumulator.Add(event); err != nil {
				t.Fatal(err)
			}
			test.check(t, accumulator.FinalResponse().Instructions)
		})
	}
}

func TestEventRequiresTypeAndUnknownKeepsExactRaw(t *testing.T) {
	if _, err := ParseEvent([]byte(`{"sequence_number":1}`)); !errors.Is(err, ErrInvalidUnion) {
		t.Fatalf("missing-type error = %v, want ErrInvalidUnion", err)
	}
	if _, err := json.Marshal(Event{Raw: json.RawMessage(`{"value":1}`)}); !errors.Is(err, ErrInvalidUnion) {
		t.Fatalf("constructed missing-type error = %v, want ErrInvalidUnion", err)
	}

	wire := []byte("{ \n \"type\" : \"response.future.delta\", \"sequence_number\" : 7, \"delta\" : 900719925474099312345 \n}")
	event, err := ParseEvent(wire)
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != "response.future.delta" || !bytes.Equal(event.RawJSON(), wire) {
		t.Fatalf("unknown event = %#v raw=%q", event, event.RawJSON())
	}
	encoded, err := event.MarshalJSON()
	if err != nil || !bytes.Equal(encoded, wire) {
		t.Fatalf("unknown event MarshalJSON = %q, %v", encoded, err)
	}
}

func TestKnownEventTypedPayloadAndExtensions(t *testing.T) {
	wire := []byte(`{
  "type":"response.output_text.delta","sequence_number":4,
  "item_id":"msg_1","output_index":0,"content_index":0,"delta":"hel","logprobs":[],
  "future_event":900719925474099312345
}`)
	event, err := ParseEvent(wire)
	if err != nil {
		t.Fatal(err)
	}
	if event.OutputTextDelta == nil || event.OutputTextDelta.Delta != "hel" || len(event.Raw) != 0 {
		t.Fatalf("decoded event = %#v", event)
	}
	if got := string(event.ExtraFields["future_event"]); got != "900719925474099312345" {
		t.Fatalf("extension = %s", got)
	}
	event.OutputTextDelta.Delta = "edited"
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"delta":"edited"`)) ||
		!bytes.Contains(encoded, []byte(`"future_event":900719925474099312345`)) ||
		!bytes.Contains(encoded, []byte(`"logprobs":[]`)) {
		t.Fatalf("known event edit/extension lost: %s", encoded)
	}
}

func TestOutputTextEventsPreserveRequiredEmptyLogprobs(t *testing.T) {
	fixtures := []string{
		`{"type":"response.output_text.delta","sequence_number":1,"item_id":"m","output_index":0,"content_index":0,"delta":"x","logprobs":[]}`,
		`{"type":"response.output_text.done","sequence_number":2,"item_id":"m","output_index":0,"content_index":0,"text":"x","logprobs":[]}`,
	}
	for _, fixture := range fixtures {
		event := mustEvent(t, fixture)
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(encoded, []byte(`"logprobs":[]`)) {
			t.Errorf("required empty logprobs lost: %s", encoded)
		}
	}
}

func TestErrorEventPreservesRequiredNullableFields(t *testing.T) {
	event := mustEvent(t, `{"type":"error","sequence_number":3,"code":null,"message":"stream failed","param":null}`)
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"code":null`)) || !bytes.Contains(encoded, []byte(`"param":null`)) {
		t.Fatalf("required nullable error fields lost: %s", encoded)
	}
}

func TestAccumulatorRoutesReasoningAndIgnoresUnknownContentPartBoundaries(t *testing.T) {
	events := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp","object":"response","created_at":1,"status":"in_progress","error":null,"incomplete_details":null,"instructions":null,"metadata":{},"model":"gpt-test","output":[],"parallel_tool_calls":true,"temperature":null,"tool_choice":"auto","tools":[],"top_p":null,"store":false}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"in_progress","summary":[],"content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":2,"item_id":"rs_1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.reasoning_text.delta","sequence_number":3,"item_id":"rs_1","output_index":0,"content_index":0,"delta":"step "}`,
		`{"type":"response.reasoning_text.done","sequence_number":4,"item_id":"rs_1","output_index":0,"content_index":0,"text":"step one"}`,
		`{"type":"response.content_part.done","sequence_number":5,"item_id":"rs_1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":"step one"}}`,
		`{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed","summary":[],"content":[{"type":"reasoning_text","text":"step one"}]}}`,
	}
	var accumulator Accumulator
	for index, fixture := range events {
		if err := accumulator.Add(mustEvent(t, fixture)); err != nil {
			t.Fatalf("event %d: %v", index, err)
		}
	}
	response := accumulator.Response()
	if response == nil || len(response.Output) != 1 || response.Output[0].Reasoning == nil ||
		len(response.Output[0].Reasoning.Content) != 1 || response.Output[0].Reasoning.Content[0].ReasoningText.Text != "step one" {
		t.Fatalf("reasoning response = %#v", response)
	}

	unknown := []string{
		`{"type":"response.output_item.added","sequence_number":7,"output_index":1,"item":{"type":"future_item","id":"future_1"}}`,
		`{"type":"response.content_part.added","sequence_number":8,"item_id":"future_1","output_index":1,"content_index":0,"part":{"type":"future_part","value":1}}`,
		`{"type":"response.completed","sequence_number":9,"response":{"id":"resp","object":"response","created_at":1,"status":"completed","error":null,"incomplete_details":null,"instructions":null,"metadata":{},"model":"gpt-test","output":[{"type":"future_item","id":"future_1"}],"parallel_tool_calls":true,"temperature":null,"tool_choice":"auto","tools":[],"top_p":null,"store":false}}`,
	}
	for index, fixture := range unknown {
		if err := accumulator.Add(mustEvent(t, fixture)); err != nil {
			t.Fatalf("unknown event %d: %v", index, err)
		}
	}
	if !accumulator.IsTerminal() || len(accumulator.FinalResponse().Output) != 1 || accumulator.FinalResponse().Output[0].Raw == nil {
		t.Fatalf("terminal unknown response = %#v", accumulator.FinalResponse())
	}
}

func TestAccumulatorCreatedThenDeltasRemainVisibleWhenMarshaled(t *testing.T) {
	created := mustEvent(t, `{
  "type":"response.created","sequence_number":0,
  "response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"gpt-test","output":[],"parallel_tool_calls":true,"store":false,"future_response":7}
}`)
	itemAdded := mustEvent(t, `{
  "type":"response.output_item.added","sequence_number":1,"output_index":0,
  "item":{"type":"message","id":"msg_1","role":"assistant","status":"in_progress","content":[]}
}`)
	partAdded := mustEvent(t, `{
  "type":"response.content_part.added","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":0,
  "part":{"type":"output_text","text":"","annotations":[]}
}`)
	delta1 := mustEvent(t, `{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"hel","logprobs":[]}`)
	delta2 := mustEvent(t, `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"lo","logprobs":[]}`)

	var accumulator Accumulator
	accumulator.SetRequestID("req_123")
	for _, event := range []*Event{created, itemAdded, partAdded, delta1, delta2} {
		if err := accumulator.Add(event); err != nil {
			t.Fatalf("Add(%s): %v", event.Type, err)
		}
	}
	response := accumulator.Response()
	if response == nil || response.OutputText() != "hello" || response.RequestID != "req_123" {
		t.Fatalf("partial response = %#v, text %q", response, response.OutputText())
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"text":"hello"`)) ||
		!bytes.Contains(encoded, []byte(`"future_response":7`)) {
		t.Fatalf("partial marshal fell back to created snapshot: %s", encoded)
	}
	if bytes.Contains(encoded, []byte("req_123")) {
		t.Fatalf("request ID leaked: %s", encoded)
	}
}

func TestAccumulatorFunctionArgumentsAndTerminalSnapshot(t *testing.T) {
	var accumulator Accumulator
	events := []*Event{
		mustEvent(t, `{"type":"response.function_call_arguments.delta","sequence_number":1,"item_id":"fc_1","output_index":0,"delta":"{\"city\":"}`),
		mustEvent(t, `{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"\"Paris\"}"}`),
		mustEvent(t, `{"type":"response.function_call_arguments.done","sequence_number":3,"item_id":"fc_1","output_index":0,"name":"weather","arguments":"{\"city\":\"Paris\"}"}`),
	}
	for _, event := range events {
		if err := accumulator.Add(event); err != nil {
			t.Fatal(err)
		}
	}
	calls := accumulator.Response().FunctionCalls()
	if len(calls) != 1 || calls[0].Name != "weather" || calls[0].Arguments != `{"city":"Paris"}` {
		t.Fatalf("assembled calls = %#v", calls)
	}

	completed := mustEvent(t, `{
  "type":"response.completed","sequence_number":4,
  "response":{"id":"resp_done","object":"response","created_at":1,"status":"completed","model":"gpt-test","output":[{"type":"message","id":"msg_final","role":"assistant","status":"completed","content":[{"type":"output_text","text":"authoritative","annotations":[]}]}],"parallel_tool_calls":true,"store":false,"future_final":true}
}`)
	if err := accumulator.Add(completed); err != nil {
		t.Fatal(err)
	}
	if !accumulator.IsTerminal() || accumulator.FinalResponse().ID != "resp_done" || accumulator.FinalResponse().OutputText() != "authoritative" {
		t.Fatalf("terminal response = %#v", accumulator.FinalResponse())
	}
	if got := string(accumulator.FinalResponse().ExtraFields["future_final"]); got != "true" {
		t.Fatalf("terminal extension = %s", got)
	}
	if err := accumulator.Add(delta1ForTest()); !errors.Is(err, ErrAccumulatorTerminal) {
		t.Fatalf("post-terminal error = %v", err)
	}
}

func TestAccumulatorTerminalClonePreservesNestedNullableConfig(t *testing.T) {
	completed := mustEvent(t, `{
  "type":"response.completed","sequence_number":1,
  "response":{
    "id":"resp_nullable","object":"response","created_at":1,"status":"completed",
    "model":"gpt-test","output":[
      {"type":"message","id":"msg_1","role":"assistant","status":"completed","phase":null,"content":[]},
      {"type":"reasoning","id":"rs_1","summary":[],"content":[],"encrypted_content":null,"status":"completed"}
    ],"parallel_tool_calls":false,"store":false,
    "reasoning":{"effort":null,"summary":null,"generate_summary":null},
    "text":{"format":{"type":"json_schema","name":"answer","schema":{},"strict":null},"verbosity":null},
    "prompt":{"id":"pmpt_1","version":null,"variables":null},
    "tools":[{"type":"function","name":"lookup","description":null,"parameters":{},"strict":false}]
  }
}`)
	var accumulator Accumulator
	if err := accumulator.Add(completed); err != nil {
		t.Fatalf("Accumulator.Add: %v", err)
	}
	encoded, err := json.Marshal(accumulator.FinalResponse())
	if err != nil {
		t.Fatalf("Marshal terminal clone: %v", err)
	}
	var nested struct {
		Reasoning json.RawMessage   `json:"reasoning"`
		Text      json.RawMessage   `json:"text"`
		Prompt    json.RawMessage   `json:"prompt"`
		Output    []json.RawMessage `json:"output"`
		Tools     []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(encoded, &nested); err != nil {
		t.Fatalf("Unmarshal terminal clone: %v", err)
	}
	assertSemanticJSONEqual(t,
		[]byte(`{"effort":null,"summary":null,"generate_summary":null}`),
		nested.Reasoning,
	)
	assertSemanticJSONEqual(t,
		[]byte(`{"format":{"type":"json_schema","name":"answer","schema":{},"strict":null},"verbosity":null}`),
		nested.Text,
	)
	assertSemanticJSONEqual(t,
		[]byte(`{"id":"pmpt_1","version":null,"variables":null}`),
		nested.Prompt,
	)
	if len(nested.Output) != 2 || len(nested.Tools) != 1 {
		t.Fatalf("nested output/tools = %d/%d", len(nested.Output), len(nested.Tools))
	}
	assertSemanticJSONEqual(t,
		[]byte(`{"type":"message","id":"msg_1","role":"assistant","status":"completed","phase":null,"content":[]}`),
		nested.Output[0],
	)
	assertSemanticJSONEqual(t,
		[]byte(`{"type":"reasoning","id":"rs_1","summary":[],"content":[],"encrypted_content":null,"status":"completed"}`),
		nested.Output[1],
	)
	assertSemanticJSONEqual(t,
		[]byte(`{"type":"function","name":"lookup","description":null,"parameters":{},"strict":false}`),
		nested.Tools[0],
	)

	continuation, err := json.Marshal(NewItemInput(accumulator.FinalResponse().Output...))
	if err != nil {
		t.Fatalf("Marshal continuation input: %v", err)
	}
	var continued []json.RawMessage
	if err := json.Unmarshal(continuation, &continued); err != nil {
		t.Fatalf("Unmarshal continuation input: %v", err)
	}
	if len(continued) != 2 {
		t.Fatalf("continued items = %d", len(continued))
	}
	assertSemanticJSONEqual(t, nested.Output[0], continued[0])
	assertSemanticJSONEqual(t, nested.Output[1], continued[1])
}

func TestAccumulatorPreservesFailedAndIncompleteSnapshots(t *testing.T) {
	tests := []struct {
		name       string
		eventType  string
		status     string
		outcome    string
		assertFunc func(*testing.T, *Response)
	}{
		{
			name: "failed", eventType: "response.failed", status: StatusFailed,
			outcome: `"error":{"code":"server_error","message":"boom","future_error":1},"incomplete_details":null`,
			assertFunc: func(t *testing.T, response *Response) {
				if response.Error == nil || response.Error.Message != "boom" || string(response.Error.ExtraFields["future_error"]) != "1" {
					t.Fatalf("failed response error = %#v", response.Error)
				}
			},
		},
		{
			name: "incomplete", eventType: "response.incomplete", status: StatusIncomplete,
			outcome: `"error":null,"incomplete_details":{"reason":"max_output_tokens","future_incomplete":2}`,
			assertFunc: func(t *testing.T, response *Response) {
				if response.IncompleteDetails == nil || response.IncompleteDetails.Reason != "max_output_tokens" || string(response.IncompleteDetails.ExtraFields["future_incomplete"]) != "2" {
					t.Fatalf("incomplete details = %#v", response.IncompleteDetails)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire := `{"type":"` + test.eventType + `","sequence_number":9,"response":{` +
				`"id":"resp_terminal","object":"response","created_at":1,"status":"` + test.status + `",` + test.outcome + `,` +
				`"model":"gpt-test","output":[],"parallel_tool_calls":true,"store":false,"future_terminal":3}}`
			var accumulator Accumulator
			if err := accumulator.Add(mustEvent(t, wire)); err != nil {
				t.Fatal(err)
			}
			response := accumulator.FinalResponse()
			if response == nil || response.Status != test.status || !response.IsTerminal() || string(response.ExtraFields["future_terminal"]) != "3" {
				t.Fatalf("terminal response = %#v", response)
			}
			test.assertFunc(t, response)
		})
	}
}

func TestAccumulatorErrorEventCreatesFailedPartialResponse(t *testing.T) {
	var accumulator Accumulator
	accumulator.SetRequestID("req_error")
	err := accumulator.Add(mustEvent(t, `{"type":"error","sequence_number":1,"code":"bad_gateway","message":"relay failed","param":null}`))
	if err != nil {
		t.Fatal(err)
	}
	response := accumulator.FinalResponse()
	if response == nil || response.Status != StatusFailed || response.Error == nil || response.Error.Code != "bad_gateway" || response.RequestID != "req_error" {
		t.Fatalf("failed partial = %#v", response)
	}
}

func TestTerminalHelpers(t *testing.T) {
	for _, status := range []string{StatusCompleted, StatusFailed, StatusIncomplete, StatusCancelled} {
		if !IsTerminalStatus(status) {
			t.Errorf("%q should be terminal", status)
		}
	}
	for _, status := range []string{"", StatusQueued, StatusInProgress, "future"} {
		if IsTerminalStatus(status) {
			t.Errorf("%q should not be terminal", status)
		}
	}
	event := &Event{Type: EventTypeResponseFailed, Response: &Response{Status: StatusFailed}}
	if !event.IsTerminal() || event.TerminalResponse() != event.Response {
		t.Fatalf("terminal event helpers failed: %#v", event)
	}
}

func mustEvent(t *testing.T, wire string) *Event {
	t.Helper()
	event, err := ParseEvent([]byte(wire))
	if err != nil {
		t.Fatalf("ParseEvent: %v\n%s", err, wire)
	}
	return event
}

func delta1ForTest() *Event {
	return &Event{
		Type: EventTypeResponseOutputTextDelta,
		OutputTextDelta: &TextDeltaEvent{
			ItemID: "msg", Delta: "ignored",
		},
	}
}

type compileTimeStream struct{}

func (compileTimeStream) Recv() (*Event, error)    { return nil, io.EOF }
func (compileTimeStream) Close() error             { return nil }
func (compileTimeStream) RequestID() string        { return "req" }
func (compileTimeStream) FinalResponse() *Response { return nil }

var _ Stream = compileTimeStream{}

func TestEventPayloadMismatchIsRejected(t *testing.T) {
	event := Event{
		Type:       EventTypeResponseOutputTextDelta,
		OutputItem: &OutputItemEvent{},
	}
	_, err := json.Marshal(event)
	if !errors.Is(err, ErrInvalidUnion) || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("payload mismatch error = %v", err)
	}
}
