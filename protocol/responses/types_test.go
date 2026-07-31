package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestInputTypeOmittedEasyMessagesRoundTripOnlyInInputContext(t *testing.T) {
	wire := []byte(`[
  {"role":"system","content":"system prompt","future_integer":900719925474099312345},
  {"role":"developer","content":[{"type":"input_text","text":"developer prompt"}]},
  {"role":"user","content":"hello"},
  {"role":"assistant","content":[{"type":"input_text","text":"prior answer"}]}
]`)
	var input Input
	if err := json.Unmarshal(wire, &input); err != nil {
		t.Fatalf("Unmarshal Input: %v", err)
	}
	if len(input.Items) != 4 {
		t.Fatalf("items = %d", len(input.Items))
	}
	for index, role := range []string{"system", "developer", "user", "assistant"} {
		item := input.Items[index]
		if item.Type != "" || item.EasyMessage == nil || item.EasyMessage.Role != role || item.Message != nil {
			t.Fatalf("items[%d] = %#v", index, item)
		}
	}
	if got := string(input.Items[0].EasyMessage.ExtraFields["future_integer"]); got != "900719925474099312345" {
		t.Fatalf("future integer = %s", got)
	}

	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal Input: %v", err)
	}
	var roundTrip []map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	for index, item := range roundTrip {
		if _, hasType := item["type"]; hasType {
			t.Fatalf("items[%d] gained a type discriminator: %s", index, encoded)
		}
	}
	if !bytes.Contains(encoded, []byte(`"future_integer":900719925474099312345`)) {
		t.Fatalf("extension lost: %s", encoded)
	}
	if _, err := NewRawInput(wire); err != nil {
		t.Fatalf("NewRawInput: %v", err)
	}

	constructed := CreateRequest{
		Model: "gpt-test",
		Input: NewItemInput(NewEasyInputMessageItem("user", NewTextContent("constructed"))),
	}
	constructedJSON, err := json.Marshal(constructed)
	if err != nil {
		t.Fatalf("Marshal constructed request: %v", err)
	}
	if bytes.Contains(constructedJSON, []byte(`"type":"message"`)) || !bytes.Contains(constructedJSON, []byte(`"role":"user"`)) {
		t.Fatalf("constructed easy message wire = %s", constructedJSON)
	}

	var strictItem Item
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hello"}`), &strictItem); !errors.Is(err, ErrInvalidUnion) {
		t.Fatalf("direct Item missing-type error = %v", err)
	}
	for _, invalid := range []string{
		`[{"role":"user"}]`,
		`[{"content":"hello"}]`,
		`[{"role":"user","other":true}]`,
	} {
		var rejected Input
		if err := json.Unmarshal([]byte(invalid), &rejected); !errors.Is(err, ErrInvalidUnion) {
			t.Errorf("Input %s error = %v, want ErrInvalidUnion", invalid, err)
		}
	}
}

func TestRequestInputOmissionAndExplicitEmptyVariants(t *testing.T) {
	tests := []struct {
		name  string
		input Input
		want  string
	}{
		{name: "omitted", want: "missing"},
		{name: "empty string", input: NewTextInput(""), want: `""`},
		{name: "empty list", input: NewItemInput(), want: `[]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := []any{
				CreateRequest{Model: "gpt-test", Input: test.input},
				TokenCountRequest{Model: "gpt-test", Input: test.input},
			}
			for _, request := range requests {
				encoded, err := json.Marshal(request)
				if err != nil {
					t.Fatalf("Marshal(%T): %v", request, err)
				}
				var object map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &object); err != nil {
					t.Fatalf("Unmarshal result: %v", err)
				}
				got, exists := object["input"]
				if test.want == "missing" {
					if exists {
						t.Errorf("%T encoded zero Input as %s in %s", request, got, encoded)
					}
					continue
				}
				if !exists || string(got) != test.want {
					t.Errorf("%T input = %s (exists %v), want %s; JSON=%s", request, got, exists, test.want, encoded)
				}
			}
		})
	}
}

func TestCreateRequestExtraFields(t *testing.T) {
	request := CreateRequest{
		Model: "gpt-test",
		Input: NewTextInput("hello"),
		ExtraFields: ExtraFields{
			"future_integer": json.RawMessage(`900719925474099312345`),
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"future_integer":900719925474099312345`)) {
		t.Fatalf("future integer was not retained exactly: %s", encoded)
	}

	request.ExtraFields["model"] = json.RawMessage(`"shadow"`)
	_, err = json.Marshal(request)
	if !errors.Is(err, ErrExtraFieldConflict) {
		t.Fatalf("conflict error = %v, want ErrExtraFieldConflict", err)
	}
	var conflict *ExtraFieldConflictError
	if !errors.As(err, &conflict) || conflict.Field != "model" {
		t.Fatalf("conflict detail = %#v, %v", conflict, err)
	}
}

func TestNestedDTOExtraFieldsRoundTripAndTypedEdits(t *testing.T) {
	wire := []byte(`{
  "model":"gpt-test","input":"hello",
  "reasoning":{"effort":"low","mode":"standard","future_reasoning":900719925474099312345},
  "text":{"verbosity":"low","future_text":{"enabled":true},"format":{"type":"json_schema","name":"old","schema":{"type":"object"},"future_format":[1,2]}},
  "prompt":{"id":"pmpt_1","version":"1","variables":{"name":"Ada"},"future_prompt":"kept"},
  "prompt_cache_options":{"mode":"explicit","ttl":"30m","future_cache":3},
  "stream_options":{"include_obfuscation":true,"future_stream":4}
}`)
	var request CreateRequest
	if err := json.Unmarshal(wire, &request); err != nil {
		t.Fatal(err)
	}
	if request.Reasoning == nil || string(request.Reasoning.ExtraFields["future_reasoning"]) != "900719925474099312345" {
		t.Fatalf("reasoning extensions = %#v", request.Reasoning)
	}
	if request.Text == nil || request.Text.Format == nil ||
		string(request.Text.ExtraFields["future_text"]) != `{"enabled":true}` ||
		string(request.Text.Format.ExtraFields["future_format"]) != `[1,2]` {
		t.Fatalf("text extensions = %#v", request.Text)
	}
	if request.Prompt == nil || string(request.Prompt.ExtraFields["future_prompt"]) != `"kept"` {
		t.Fatalf("prompt extensions = %#v", request.Prompt)
	}
	if request.PromptCacheOptions == nil || string(request.PromptCacheOptions.ExtraFields["future_cache"]) != "3" {
		t.Fatalf("prompt cache extensions = %#v", request.PromptCacheOptions)
	}
	if request.StreamOptions == nil || string(request.StreamOptions.ExtraFields["future_stream"]) != "4" {
		t.Fatalf("stream extensions = %#v", request.StreamOptions)
	}

	request.Reasoning.Effort = "high"
	request.Reasoning.Mode = "pro"
	request.Text.Verbosity = "high"
	request.Text.Format.Name = "edited"
	request.Prompt.Version = "2"
	request.PromptCacheOptions.TTL = "24h"
	includeObfuscation := false
	request.StreamOptions.IncludeObfuscation = &includeObfuscation
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(encoded)
	for _, wanted := range []string{
		`"effort":"high"`, `"mode":"pro"`, `"verbosity":"high"`, `"name":"edited"`,
		`"version":"2"`, `"ttl":"24h"`, `"include_obfuscation":false`,
		`"future_reasoning":900719925474099312345`, `"future_text":{"enabled":true}`,
		`"future_format":[1,2]`, `"future_prompt":"kept"`, `"future_cache":3`, `"future_stream":4`,
	} {
		if !strings.Contains(jsonText, wanted) {
			t.Errorf("nested round-trip lacks %s: %s", wanted, encoded)
		}
	}
}

func TestNestedDTOExtraFieldConflicts(t *testing.T) {
	tests := []struct {
		name  string
		value any
		field string
	}{
		{"reasoning", ReasoningConfig{ExtraFields: ExtraFields{"effort": json.RawMessage(`"shadow"`)}}, "effort"},
		{"text", TextConfig{ExtraFields: ExtraFields{"format": json.RawMessage(`null`)}}, "format"},
		{"text format", TextFormat{ExtraFields: ExtraFields{"type": json.RawMessage(`"text"`)}}, "type"},
		{"prompt", Prompt{ExtraFields: ExtraFields{"id": json.RawMessage(`"other"`)}}, "id"},
		{"prompt cache", PromptCacheOptions{ExtraFields: ExtraFields{"ttl": json.RawMessage(`"other"`)}}, "ttl"},
		{"stream", StreamOptions{ExtraFields: ExtraFields{"include_obfuscation": json.RawMessage(`false`)}}, "include_obfuscation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := json.Marshal(test.value)
			if !errors.Is(err, ErrExtraFieldConflict) {
				t.Fatalf("error = %v, want ErrExtraFieldConflict", err)
			}
			var conflict *ExtraFieldConflictError
			if !errors.As(err, &conflict) || conflict.Field != test.field {
				t.Fatalf("conflict = %#v, want field %q", conflict, test.field)
			}
		})
	}
}

func TestKnownItemAndContentExtensionsRemainEditable(t *testing.T) {
	wire := []byte(`{
  "type":"message","id":"msg_1","role":"assistant","status":"completed",
  "content":[{"type":"output_text","text":"old","annotations":[],"future_number":900719925474099312345}],
  "future_item":{"nested":true}
}`)
	var item Item
	if err := json.Unmarshal(wire, &item); err != nil {
		t.Fatal(err)
	}
	if item.Message == nil || len(item.Raw) != 0 {
		t.Fatalf("known item was not typed: %#v", item)
	}
	if got := string(item.Message.ExtraFields["future_item"]); got != `{"nested":true}` {
		t.Fatalf("item extension = %s", got)
	}
	part := item.Message.Content.Parts[0]
	if part.OutputText == nil || len(part.Raw) != 0 {
		t.Fatalf("known content part was not typed: %#v", part)
	}
	if got := string(part.OutputText.ExtraFields["future_number"]); got != `900719925474099312345` {
		t.Fatalf("content extension = %s", got)
	}

	item.Message.Content.Parts[0].OutputText.Text = "new"
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"text":"new"`)) || bytes.Contains(encoded, []byte(`"text":"old"`)) {
		t.Fatalf("typed edit was ignored: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(`"future_number":900719925474099312345`)) ||
		!bytes.Contains(encoded, []byte(`"future_item":{"nested":true}`)) {
		t.Fatalf("extensions were lost: %s", encoded)
	}
}

func TestUnknownUnionsKeepExactRawJSON(t *testing.T) {
	itemWire := []byte("{ \n  \"type\" : \"future_item\", \"n\" : 900719925474099312345 \n}")
	var item Item
	if err := json.Unmarshal(itemWire, &item); err != nil {
		t.Fatal(err)
	}
	if item.Type != "future_item" || !bytes.Equal(item.RawJSON(), itemWire) {
		t.Fatalf("unknown item raw mismatch: %q", item.RawJSON())
	}
	encoded, err := item.MarshalJSON()
	if err != nil || !bytes.Equal(encoded, itemWire) {
		t.Fatalf("unknown item MarshalJSON = %q, %v", encoded, err)
	}

	partWire := []byte("{ \"type\" : \"future_content\", \"value\" : [1, 2] }")
	var part ContentPart
	if err := json.Unmarshal(partWire, &part); err != nil {
		t.Fatal(err)
	}
	if part.Type != "future_content" || !bytes.Equal(part.RawJSON(), partWire) {
		t.Fatalf("unknown part raw mismatch: %q", part.RawJSON())
	}
	encoded, err = part.MarshalJSON()
	if err != nil || !bytes.Equal(encoded, partWire) {
		t.Fatalf("unknown part MarshalJSON = %q, %v", encoded, err)
	}

	toolWire := []byte("{ \"type\" : \"future_tool\", \"config\" : { \"large\" : 900719925474099312345 } }")
	var tool Tool
	if err := json.Unmarshal(toolWire, &tool); err != nil {
		t.Fatal(err)
	}
	if tool.Type != "future_tool" || !bytes.Equal(tool.RawJSON(), toolWire) {
		t.Fatalf("unknown tool raw mismatch: %q", tool.RawJSON())
	}
	encoded, err = tool.MarshalJSON()
	if err != nil || !bytes.Equal(encoded, toolWire) {
		t.Fatalf("unknown tool MarshalJSON = %q, %v", encoded, err)
	}
}

func TestTaggedUnionsRejectMissingEmptyAndNonStringType(t *testing.T) {
	decoders := []struct {
		name   string
		decode func([]byte) error
	}{
		{"item", func(data []byte) error { var value Item; return json.Unmarshal(data, &value) }},
		{"content", func(data []byte) error { var value ContentPart; return json.Unmarshal(data, &value) }},
		{"tool", func(data []byte) error { var value Tool; return json.Unmarshal(data, &value) }},
		{"event", func(data []byte) error { _, err := ParseEvent(data); return err }},
	}
	invalid := []string{`{}`, `{"type":""}`, `{"type":null}`, `{"type":7}`}
	for _, decoder := range decoders {
		for _, wire := range invalid {
			t.Run(decoder.name+"/"+wire, func(t *testing.T) {
				err := decoder.decode([]byte(wire))
				if !errors.Is(err, ErrInvalidUnion) {
					t.Fatalf("error = %v, want wrapping ErrInvalidUnion", err)
				}
			})
		}
	}
}

func TestKnownUnionTypeCannotUseRaw(t *testing.T) {
	values := []any{
		Item{Type: ItemTypeMessage, Raw: json.RawMessage(`{"type":"message","role":"assistant","content":[]}`)},
		ContentPart{Type: ContentTypeOutputText, Raw: json.RawMessage(`{"type":"output_text","text":"x"}`)},
		Tool{Type: "function", Raw: json.RawMessage(`{"type":"function","name":"f"}`)},
		Event{Type: EventTypeResponseCompleted, Raw: json.RawMessage(`{"type":"response.completed","sequence_number":1}`)},
	}
	for _, value := range values {
		if _, err := json.Marshal(value); !errors.Is(err, ErrInvalidUnion) {
			t.Errorf("Marshal(%T) error = %v, want ErrInvalidUnion", value, err)
		}
	}
}

func TestKnownUnionConstructionRequiresDiscriminator(t *testing.T) {
	values := []any{
		Item{Message: &Message{Type: ItemTypeMessage, Role: "assistant", Content: NewPartContent()}},
		Item{Type: ItemTypeMessage, Message: &Message{Role: "assistant", Content: NewPartContent()}},
		ContentPart{OutputText: &OutputText{Type: ContentTypeOutputText, Text: "x"}},
		ContentPart{Type: ContentTypeOutputText, OutputText: &OutputText{Text: "x"}},
		Tool{Function: &FunctionTool{Type: "function", Name: "f"}},
		Tool{Type: "function", Function: &FunctionTool{Name: "f"}},
	}
	for _, value := range values {
		if _, err := json.Marshal(value); !errors.Is(err, ErrInvalidUnion) {
			t.Errorf("Marshal(%T %#v) error = %v, want ErrInvalidUnion", value, value, err)
		}
	}

	direct := []any{&Message{}, &OutputText{}, &FunctionTool{}}
	for _, target := range direct {
		if err := json.Unmarshal([]byte(`{}`), target); !errors.Is(err, ErrInvalidUnion) {
			t.Errorf("Unmarshal(%T) error = %v, want ErrInvalidUnion", target, err)
		}
	}
}

func TestResponseTypedMutationExtensionsHelpersAndRequestID(t *testing.T) {
	wire := []byte(`{
  "id":"resp_1","object":"response","created_at":1,"status":"completed",
  "model":"gpt-test","output":[
    {"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]},
    {"type":"function_call","id":"fc_1","call_id":"call_1","name":"weather","arguments":"{}","status":"completed","caller":{"kind":"model"},"future_call":1}
  ],
  "parallel_tool_calls":true,"store":false,
  "usage":{"input_tokens":2,"input_tokens_details":{"cached_tokens":1,"future_cache":2},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1,"future_reasoning":4},"total_tokens":5,"future_usage":6},
  "future_response":900719925474099312345
}`)
	var response Response
	if err := json.Unmarshal(wire, &response); err != nil {
		t.Fatal(err)
	}
	response.RequestID = "req_secret"
	if got := response.OutputText(); got != "hello" {
		t.Fatalf("OutputText = %q", got)
	}
	response.Output[0].Message.Content.Parts[0].OutputText.Text = "edited"

	calls := response.FunctionCalls()
	if len(calls) != 1 || calls[0].Name != "weather" {
		t.Fatalf("FunctionCalls = %#v", calls)
	}
	calls[0].Caller[0] = '['
	calls[0].ExtraFields["future_call"][0] = '9'
	if string(response.Output[1].FunctionCall.Caller) != `{"kind":"model"}` ||
		string(response.Output[1].FunctionCall.ExtraFields["future_call"]) != "1" {
		t.Fatal("FunctionCalls did not return owned raw values")
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(encoded)
	for _, wanted := range []string{
		`"text":"edited"`, `"future_response":900719925474099312345`,
		`"future_usage":6`, `"future_cache":2`, `"future_reasoning":4`,
	} {
		if !strings.Contains(jsonText, wanted) {
			t.Errorf("encoded response lacks %s: %s", wanted, encoded)
		}
	}
	if strings.Contains(jsonText, "req_secret") || strings.Contains(jsonText, "RequestID") {
		t.Fatalf("header request ID leaked into JSON: %s", encoded)
	}
}

func TestResponseRequiredCollectionsAndNullablePresenceRoundTrip(t *testing.T) {
	wire := []byte(`{
  "id":"resp_presence","object":"response","created_at":1.25,"status":"completed",
  "completed_at":null,"background":null,"error":null,"incomplete_details":null,
  "instructions":null,"max_output_tokens":null,"max_tool_calls":null,
  "model":"gpt-test","output":[
    {"type":"reasoning","id":"rs_1","status":"completed","summary":[],"content":[]},
    {"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[
      {"type":"output_text","text":"hello","annotations":[],"logprobs":[]}
    ]}
  ],
  "parallel_tool_calls":false,"previous_response_id":null,"reasoning":null,"store":false,
  "temperature":null,"top_logprobs":null,"text":null,"tool_choice":"auto","tools":[],
  "top_p":null,"truncation":null,"usage":null,"metadata":{},"service_tier":null,
  "conversation":null,"context_management":null,"moderation":null,"prompt":null,
  "prompt_cache_key":null,"prompt_cache_options":null,"prompt_cache_retention":null,
  "safety_identifier":null,"user":null
}`)
	var response Response
	if err := json.Unmarshal(wire, &response); err != nil {
		t.Fatalf("Unmarshal Response: %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal Response: %v", err)
	}
	assertSemanticJSONEqual(t, wire, encoded)
	nullMetadataWire := bytes.Replace(wire, []byte(`"metadata":{}`), []byte(`"metadata":null`), 1)
	var nullMetadata Response
	if err := json.Unmarshal(nullMetadataWire, &nullMetadata); err != nil {
		t.Fatalf("Unmarshal null metadata: %v", err)
	}
	nullMetadataJSON, err := json.Marshal(nullMetadata)
	if err != nil {
		t.Fatalf("Marshal null metadata: %v", err)
	}
	assertSemanticJSONEqual(t, nullMetadataWire, nullMetadataJSON)

	terminal := mustEvent(t, `{"type":"response.completed","sequence_number":1,"response":`+string(wire)+`}`)
	var accumulator Accumulator
	if err := accumulator.Add(terminal); err != nil {
		t.Fatalf("Accumulator.Add: %v", err)
	}
	cloned, err := json.Marshal(accumulator.FinalResponse())
	if err != nil {
		t.Fatalf("Marshal terminal clone: %v", err)
	}
	assertSemanticJSONEqual(t, wire, cloned)

	continuation, err := json.Marshal(CreateRequest{
		Model: "gpt-next",
		Input: NewItemInput(response.Output...),
	})
	if err != nil {
		t.Fatalf("Marshal stateless continuation: %v", err)
	}
	for _, required := range []string{`"summary":[]`, `"content":[]`, `"annotations":[]`, `"logprobs":[]`} {
		if !bytes.Contains(continuation, []byte(required)) {
			t.Errorf("continuation lacks %s: %s", required, continuation)
		}
	}

	constructed, err := json.Marshal(Response{
		ID: "r", Object: "response", Model: "gpt-test", Output: nil,
	})
	if err != nil {
		t.Fatalf("Marshal constructed response: %v", err)
	}
	for _, required := range []string{`"output":[]`, `"tools":[]`, `"metadata":null`, `"temperature":null`, `"tool_choice":"auto"`, `"top_p":null`} {
		if !bytes.Contains(constructed, []byte(required)) {
			t.Errorf("constructed response lacks %s: %s", required, constructed)
		}
	}
}

func TestUsagePreservesRequiredZeroCacheWriteTokens(t *testing.T) {
	var usage Usage
	wire := []byte(`{"input_tokens":0,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":0}`)
	if err := json.Unmarshal(wire, &usage); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"cache_write_tokens":0`)) {
		t.Fatalf("required zero cache_write_tokens lost: %s", encoded)
	}
	constructed, err := json.Marshal(Usage{})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}`,
		`"output_tokens_details":{"reasoning_tokens":0}`,
	} {
		if !bytes.Contains(constructed, []byte(required)) {
			t.Errorf("constructed usage lacks %s: %s", required, constructed)
		}
	}
}

func assertSemanticJSONEqual(t *testing.T, want, got []byte) {
	t.Helper()
	var wantValue, gotValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode wanted JSON: %v", err)
	}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode actual JSON: %v", err)
	}
	if !reflect.DeepEqual(wantValue, gotValue) {
		t.Fatalf("JSON changed\n got: %s\nwant: %s", got, want)
	}
}

func TestRequestIDNeverSerializesOnResponseEnvelopes(t *testing.T) {
	values := []any{
		Response{RequestID: "req_1"},
		InputItemList{RequestID: "req_2"},
		DeletedResponse{RequestID: "req_3"},
		TokenCountResponse{RequestID: "req_4"},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("Marshal(%T): %v", value, err)
		}
		if bytes.Contains(encoded, []byte("req_")) || bytes.Contains(encoded, []byte("RequestID")) {
			t.Errorf("%T leaked RequestID: %s", value, encoded)
		}
	}
}

func TestTokenCountRequestValidationAndPersonality(t *testing.T) {
	request := TokenCountRequest{
		Model:       "gpt-test",
		Personality: "concise",
		ToolChoice:  json.RawMessage(`{"type":"none"}`),
		ExtraFields: ExtraFields{"future": json.RawMessage(`true`)},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"personality":"concise"`)) || !bytes.Contains(encoded, []byte(`"future":true`)) {
		t.Fatalf("token-count fields missing: %s", encoded)
	}
	request.ExtraFields["personality"] = json.RawMessage(`"other"`)
	if _, err := json.Marshal(request); !errors.Is(err, ErrExtraFieldConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestUnionConflictsAreRejected(t *testing.T) {
	text := "x"
	_, err := json.Marshal(Input{Text: &text, Items: []Item{}})
	if !errors.Is(err, ErrInvalidUnion) {
		t.Fatalf("Input conflict error = %v", err)
	}
	_, err = json.Marshal(ContentPart{
		Type:       ContentTypeOutputText,
		OutputText: &OutputText{Text: "x"},
		Refusal:    &Refusal{Refusal: "no"},
	})
	if !errors.Is(err, ErrInvalidUnion) {
		t.Fatalf("ContentPart conflict error = %v", err)
	}
	_, err = json.Marshal(Response{Output: []Item{
		NewEasyInputMessageItem("user", NewTextContent("not output")),
	}})
	if !errors.Is(err, ErrInvalidUnion) {
		t.Fatalf("easy message in output error = %v", err)
	}
}
