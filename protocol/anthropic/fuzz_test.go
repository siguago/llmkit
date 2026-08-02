package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
)

func FuzzContentBlockRoundTrip(f *testing.F) {
	f.Add(`{"type":"text","text":"hello","future":9007199254740993}`)
	f.Add(`{"type":"thinking","thinking":"x","signature":"sig"}`)
	f.Add(`{"type":"redacted_thinking","data":"opaque"}`)
	f.Add(`{"type":"future_block","payload":{"x":1}}`)
	f.Add(`{}`)
	f.Add(`{"type":""}`)
	f.Add(`null`)
	f.Add(`{bad`)

	f.Fuzz(func(t *testing.T, input string) {
		var block ContentBlock
		if err := json.Unmarshal([]byte(input), &block); err != nil {
			return
		}
		if block.Type == "" {
			t.Fatal("accepted content block with an empty discriminator")
		}
		encoded, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("marshal accepted block: %v", err)
		}
		var second ContentBlock
		if err := json.Unmarshal(encoded, &second); err != nil {
			t.Fatalf("reparse marshaled block: %v", err)
		}
	})
}

func FuzzEventRoundTrip(f *testing.F) {
	f.Add(`{"type":"ping"}`)
	f.Add(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`)
	f.Add(`{"type":"future_event","n":9007199254740993}`)
	f.Add(`{}`)
	f.Add(`{"type":""}`)
	f.Add(`{"type":"content_block_start","index":0,"content_block":{}}`)
	f.Add(`{"type":"content_block_delta","index":0,"delta":{"type":""}}`)
	f.Add(`[]`)
	f.Add(`{bad`)

	f.Fuzz(func(t *testing.T, input string) {
		event, err := ParseEvent([]byte(input))
		if err != nil {
			return
		}
		if event.Type == "" {
			t.Fatal("accepted event with an empty discriminator")
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal accepted event: %v", err)
		}
		if _, err := ParseEvent(encoded); err != nil {
			t.Fatalf("reparse marshaled event: %v", err)
		}
	})
}

func FuzzAccumulatorNeverPanics(f *testing.F) {
	f.Add("{\"type\":\"ping\"}\n{\"type\":\"message_stop\"}")
	f.Add("{\"type\":\"future_event\",\"x\":1}")
	f.Add("{\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"s\",\"name\":\"web_search\"}}\n{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{bad\"}}")
	f.Add("")

	f.Fuzz(func(t *testing.T, input string) {
		accumulator := NewAccumulator()
		lines := strings.Split(input, "\n")
		if len(lines) > 100 {
			lines = lines[:100]
		}
		for _, line := range lines {
			event, err := ParseEvent([]byte(line))
			if err != nil {
				continue
			}
			_ = accumulator.Add(event)
		}
		_, _ = accumulator.Result()
		_ = accumulator.Message()
		_ = accumulator.UnknownEvents()
	})
}
