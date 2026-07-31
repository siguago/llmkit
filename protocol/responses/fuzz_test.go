package responses

import (
	"bytes"
	"encoding/json"
	"testing"
)

func FuzzItemJSON(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}`),
		[]byte(`{"type":"function_call","call_id":"c","name":"f","arguments":"{}"}`),
		[]byte(`{ "type" : "future_item", "large" : 900719925474099312345 }`),
		[]byte(`null`),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var item Item
		if err := json.Unmarshal(data, &item); err != nil {
			return
		}
		wire := bytes.TrimSpace(data)
		if len(item.Raw) > 0 && !bytes.Equal(item.RawJSON(), wire) {
			t.Fatalf("unknown item raw changed: input=%q raw=%q", data, item.RawJSON())
		}
		encoded, err := item.MarshalJSON()
		if err != nil {
			return
		}
		if !json.Valid(encoded) {
			t.Fatalf("invalid marshaled item: %q", encoded)
		}
		if len(item.Raw) > 0 && !bytes.Equal(encoded, wire) {
			t.Fatalf("unknown item did not round-trip exactly: input=%q output=%q", data, encoded)
		}
	})
}

func FuzzContentPartJSON(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"type":"output_text","text":"hello","annotations":[]}`),
		[]byte(`{"type":"input_image","image_url":"data:image/png;base64,AA=="}`),
		[]byte(`{ "type" : "future_content", "large" : 900719925474099312345 }`),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var part ContentPart
		if err := json.Unmarshal(data, &part); err != nil {
			return
		}
		wire := bytes.TrimSpace(data)
		if len(part.Raw) > 0 && !bytes.Equal(part.RawJSON(), wire) {
			t.Fatalf("unknown content raw changed: input=%q raw=%q", data, part.RawJSON())
		}
		encoded, err := part.MarshalJSON()
		if err != nil {
			return
		}
		if !json.Valid(encoded) {
			t.Fatalf("invalid marshaled content: %q", encoded)
		}
		if len(part.Raw) > 0 && !bytes.Equal(encoded, wire) {
			t.Fatalf("unknown content did not round-trip exactly: input=%q output=%q", data, encoded)
		}
	})
}

func FuzzEventJSON(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"type":"response.output_text.delta","sequence_number":1,"item_id":"m","output_index":0,"content_index":0,"delta":"x"}`),
		[]byte(`{"type":"response.completed","sequence_number":2,"response":{"id":"r","object":"response","created_at":1,"status":"completed","model":"gpt","output":[],"parallel_tool_calls":true,"store":false}}`),
		[]byte(`{ "type" : "response.future.delta", "sequence_number" : 3, "value" : 900719925474099312345 }`),
		[]byte(`{"sequence_number":4}`),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		event, err := ParseEvent(data)
		if err != nil {
			return
		}
		wire := bytes.TrimSpace(data)
		if len(event.Raw) > 0 && !bytes.Equal(event.RawJSON(), wire) {
			t.Fatalf("unknown event raw changed: input=%q raw=%q", data, event.RawJSON())
		}
		encoded, err := event.MarshalJSON()
		if err != nil {
			return
		}
		if !json.Valid(encoded) {
			t.Fatalf("invalid marshaled event: %q", encoded)
		}
		if len(event.Raw) > 0 && !bytes.Equal(encoded, wire) {
			t.Fatalf("unknown event did not round-trip exactly: input=%q output=%q", data, encoded)
		}

		var accumulator Accumulator
		_ = accumulator.Add(event)
		if response := accumulator.Response(); response != nil {
			_, _ = json.Marshal(response)
		}
	})
}
