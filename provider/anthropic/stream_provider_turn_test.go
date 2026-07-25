package anthropic

import (
	"io"
	"strings"
	"testing"
)

// 流式 web_search 调用：server_tool_use 块通过 input_json_delta 累积输入，
// web_search_tool_result 块在 content_block_start 一次性给出 content。
// 二者都应在各自 content_block_stop 时 emit 一个 ProviderTurnBlocks delta。
func TestStreamReader_AccumulatesServerToolUseBlock(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_x","name":"web_search"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"claude shannon\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`
	r := NewStreamReader(io.NopCloser(strings.NewReader(sse)), "")
	var serverToolBlock map[string]any
	for {
		chunk, err := r.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
			continue
		}
		if blocks := chunk.Choices[0].Delta.ProviderTurnBlocks; len(blocks) > 0 {
			if m, ok := blocks[0].(map[string]any); ok {
				serverToolBlock = m
			}
		}
	}
	if serverToolBlock == nil {
		t.Fatal("server_tool_use block was never emitted as ProviderTurnBlocks delta")
	}
	if serverToolBlock["type"] != "server_tool_use" {
		t.Errorf("type wrong: %+v", serverToolBlock)
	}
	if serverToolBlock["id"] != "srvtoolu_x" {
		t.Errorf("id lost: %+v", serverToolBlock)
	}
	// Critical: input_json_delta chunks must be accumulated and parsed into
	// the input field — multi-turn round-trip needs the original query.
	input, ok := serverToolBlock["input"].(map[string]any)
	if !ok {
		t.Fatalf("input not parsed from accumulated partial_json: %+v", serverToolBlock["input"])
	}
	if input["query"] != "claude shannon" {
		t.Errorf("query lost or corrupted: %+v", input)
	}
}

func TestStreamReader_EmitsWebSearchToolResultBlock(t *testing.T) {
	// web_search_tool_result arrives complete in the start event (no deltas).
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_x","content":[{"type":"web_search_result","url":"https://en.wikipedia.org","title":"W","encrypted_content":"BLOB"}]}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_stop
data: {"type":"message_stop"}

`
	r := NewStreamReader(io.NopCloser(strings.NewReader(sse)), "")
	var resultBlock map[string]any
	for {
		chunk, err := r.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
			continue
		}
		if blocks := chunk.Choices[0].Delta.ProviderTurnBlocks; len(blocks) > 0 {
			if m, ok := blocks[0].(map[string]any); ok {
				resultBlock = m
			}
		}
	}
	if resultBlock == nil {
		t.Fatal("web_search_tool_result block never emitted as ProviderTurnBlocks delta")
	}
	if resultBlock["type"] != "web_search_tool_result" {
		t.Errorf("type wrong: %+v", resultBlock)
	}
	if resultBlock["tool_use_id"] != "srvtoolu_x" {
		t.Errorf("tool_use_id lost: %+v", resultBlock)
	}
	contentArr, ok := resultBlock["content"].([]any)
	if !ok || len(contentArr) == 0 {
		t.Fatalf("content array missing or empty: %+v", resultBlock["content"])
	}
	first, _ := contentArr[0].(map[string]any)
	if first["encrypted_content"] != "BLOB" {
		t.Errorf("encrypted_content lost — multi-turn streaming round-trip broken: %+v", first)
	}
}

func TestStreamReader_TextBlockIntermixedWithProviderTurn_StillWorks(t *testing.T) {
	// Mixed sequence: text chunks come before AND after a server_tool_use
	// block; we must continue emitting text deltas normally and only the
	// server_tool_use block becomes a ProviderTurnBlocks delta.
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me search."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_x","name":"web_search"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Done."}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_stop
data: {"type":"message_stop"}

`
	r := NewStreamReader(io.NopCloser(strings.NewReader(sse)), "")
	var (
		textChunks        []string
		providerTurnCount int
	)
	for {
		chunk, err := r.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
			continue
		}
		d := chunk.Choices[0].Delta
		if d.Content != nil {
			if s, ok := d.Content.(string); ok && s != "" {
				textChunks = append(textChunks, s)
			}
		}
		if len(d.ProviderTurnBlocks) > 0 {
			providerTurnCount++
		}
	}
	if got := strings.Join(textChunks, ""); got != "Let me search.Done." {
		t.Errorf("text chunks lost or merged with provider_turn: %q", got)
	}
	if providerTurnCount != 1 {
		t.Errorf("expected exactly 1 ProviderTurnBlocks delta, got %d", providerTurnCount)
	}
}
