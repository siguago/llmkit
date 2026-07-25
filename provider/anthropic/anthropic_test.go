package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/siguago/llmkit/provider"
)

func TestBuildUsage_IncludesCacheTokens(t *testing.T) {
	// Anthropic's input_tokens excludes cached portions; PromptTokens must
	// re-add cache reads/creation so total_tokens reconciles for clients.
	u := buildUsage(responseUsage{
		InputTokens:              50,
		OutputTokens:             100,
		CacheReadInputTokens:     1000,
		CacheCreationInputTokens: 200,
	})
	if u.PromptTokens != 1250 {
		t.Fatalf("prompt_tokens should sum input+cache_read+cache_creation, got %d", u.PromptTokens)
	}
	if u.TotalTokens != 1350 {
		t.Fatalf("total_tokens mismatch: got %d, want 1350", u.TotalTokens)
	}
	if u.CachedTokens != 1000 {
		t.Fatalf("cached_tokens should mirror cache_read_input_tokens, got %d", u.CachedTokens)
	}
	if u.PromptTokensDetails == nil || u.PromptTokensDetails.CachedTokens != 1000 {
		t.Fatalf("prompt_tokens_details.cached_tokens missing: %+v", u.PromptTokensDetails)
	}
}

func TestMapStopReason_PauseAndRefusal(t *testing.T) {
	cases := []struct{ in, want string }{
		{"end_turn", "stop"},
		{"max_tokens", "length"},
		{"stop_sequence", "stop"},
		{"tool_use", "tool_calls"},
		{"pause_turn", "stop"},
		{"refusal", "content_filter"},
		{"unknown_future", "stop"}, // graceful default
	}
	for _, c := range cases {
		if got := mapStopReason(c.in); got != c.want {
			t.Fatalf("%s: want %s, got %s", c.in, c.want, got)
		}
	}
}

func TestBuildRequest_ThinkingNormalizesIncompatibleParams(t *testing.T) {
	temp := 0.7
	topK := 40
	req := &provider.ChatCompletionRequest{
		Messages:    []provider.Message{{Role: "user", Content: "hi"}},
		Temperature: &temp,
		TopK:        &topK,
		Thinking: &provider.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: 2048,
		},
	}
	r, _ := buildRequest("claude-opus-4-7", req)
	if r.Thinking == nil || r.Thinking.Type != "enabled" {
		t.Fatalf("thinking config not propagated: %+v", r.Thinking)
	}
	if r.Temperature != nil {
		t.Fatalf("temperature must be cleared when thinking is enabled (Anthropic forbids it), got %v", *r.Temperature)
	}
	if r.TopK != nil {
		t.Fatalf("top_k must be cleared when thinking is enabled (Anthropic forbids it), got %v", *r.TopK)
	}
	if r.MaxTokens <= req.Thinking.BudgetTokens {
		t.Fatalf("max_tokens must exceed budget_tokens, got max=%d budget=%d", r.MaxTokens, req.Thinking.BudgetTokens)
	}
}

func TestBuildRequest_StopSequencesFromArrayAndString(t *testing.T) {
	cases := []struct {
		name string
		stop any
		want []string
	}{
		{"string", "STOP", []string{"STOP"}},
		{"array_any", []any{"a", "b"}, []string{"a", "b"}},
		{"array_string", []string{"x", "y"}, []string{"x", "y"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
				Messages: []provider.Message{{Role: "user", Content: "hi"}},
				Stop:     c.stop,
			})
			if len(r.StopSequences) != len(c.want) {
				t.Fatalf("stop sequences mismatch: got %v want %v", r.StopSequences, c.want)
			}
			for i, want := range c.want {
				if r.StopSequences[i] != want {
					t.Fatalf("stop[%d]: got %s, want %s", i, r.StopSequences[i], want)
				}
			}
		})
	}
}

func TestBuildRequest_ToolCacheControlPropagated(t *testing.T) {
	// Anthropic supports cache_control on the LAST tool to cache the entire
	// tools list. The unified Tool type now carries CacheControl; ensure it
	// reaches anthropicTool.
	cc := map[string]any{"type": "ephemeral"}
	r, _ := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Tools: []provider.Tool{{
			Type:         "function",
			Function:     provider.ToolFunction{Name: "f", Parameters: map[string]any{"type": "object"}},
			CacheControl: cc,
		}},
	})
	if len(r.Tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(r.Tools))
	}
	tool, ok := r.Tools[0].(anthropicTool)
	if !ok {
		t.Fatalf("function tool not stored as anthropicTool: %+v", r.Tools[0])
	}
	if tool.CacheControl == nil {
		t.Fatalf("tool cache_control lost: %+v", tool)
	}
}

func TestBuildRequest_ServiceTierAndUserMetadata(t *testing.T) {
	tier := "auto"
	r, _ := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages:    []provider.Message{{Role: "user", Content: "hi"}},
		ServiceTier: &tier,
		User:        "u-42",
	})
	if r.ServiceTier != "auto" {
		t.Fatalf("service_tier not propagated: %+v", r.ServiceTier)
	}
	meta, ok := r.Metadata.(map[string]any)
	if !ok || meta["user_id"] != "u-42" {
		t.Fatalf("user_id metadata missing: %+v", r.Metadata)
	}
}

func TestBuildRequest_SafetyIdentifierWinsOverUser(t *testing.T) {
	// OpenAI's safety_identifier supersedes user in newer SDKs. When both are
	// set we should prefer safety_identifier for Anthropic's metadata.user_id
	// so the abuse-tracking signal aligns with the client's intent.
	r, _ := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages:         []provider.Message{{Role: "user", Content: "hi"}},
		User:             "legacy-user",
		SafetyIdentifier: "modern-id",
	})
	meta, ok := r.Metadata.(map[string]any)
	if !ok || meta["user_id"] != "modern-id" {
		t.Fatalf("safety_identifier should win when both set: %+v", r.Metadata)
	}
}

func TestListModels_FiltersDeprecatedPastNow(t *testing.T) {
	// Run the filter logic on a synthetic API response payload to confirm the
	// time comparison behavior. We exercise the same ParseRFC3339 path the
	// real ListModels uses.
	type modelEntry struct {
		ID           string
		DeprecatedAt string
		WantIncluded bool
	}
	now := time.Now().UTC()
	cases := []modelEntry{
		{"claude-active", "", true},
		{"claude-deprecated-past", now.Add(-24 * time.Hour).Format(time.RFC3339), false},
		{"claude-deprecated-now", now.Format(time.RFC3339), false},
		{"claude-deprecated-future", now.Add(24 * time.Hour).Format(time.RFC3339), true},
		{"claude-malformed-date", "not-a-date", true}, // unparseable → keep (defensive)
	}
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			included := true
			if c.DeprecatedAt != "" {
				if t2, err := time.Parse(time.RFC3339, c.DeprecatedAt); err == nil && !t2.After(now) {
					included = false
				}
			}
			if included != c.WantIncluded {
				t.Fatalf("%s: included=%v want %v", c.ID, included, c.WantIncluded)
			}
		})
	}
}

func TestBuildRequest_ToolChoiceConversion(t *testing.T) {
	cases := []struct {
		choice any
		want   string
	}{
		{"auto", "auto"},
		{"required", "any"},
		{"none", "none"},
		{map[string]any{"type": "function", "function": map[string]any{"name": "f"}}, "tool"},
	}
	for _, c := range cases {
		got := convertToolChoice(c.choice)
		if got == nil || got.Type != c.want {
			t.Fatalf("choice=%v want type=%s, got %+v", c.choice, c.want, got)
		}
	}
}

func TestBuildRequest_SystemCacheControlPreserved(t *testing.T) {
	// User marks the heavy chunk of the system prompt for caching. The Anthropic
	// builder must keep the system field as an array of blocks (not collapse to
	// a string) so the cache_control breakpoint reaches the API.
	cc := map[string]any{"type": "ephemeral"}
	req := &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{
				Role: "system",
				Content: []provider.ContentPart{
					{Type: "text", Text: "you are helpful"},
					{Type: "text", Text: "very long context...", CacheControl: cc},
				},
			},
			{Role: "user", Content: "hi"},
		},
	}
	r, _ := buildRequest("claude-opus-4-7", req)
	blocks, ok := r.System.([]contentBlock)
	if !ok {
		t.Fatalf("system must be []contentBlock when cache_control is present, got %T", r.System)
	}
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
	if blocks[1].CacheControl == nil {
		t.Fatalf("cache_control lost on second block: %+v", blocks[1])
	}
}

func TestBuildRequest_SystemNoCacheCollapsesToString(t *testing.T) {
	// Without cache_control, system should remain a plain string for back-compat
	// with the existing wire format.
	req := &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "system", Content: "first"},
			{Role: "system", Content: "second"},
			{Role: "user", Content: "hi"},
		},
	}
	r, _ := buildRequest("claude-opus-4-7", req)
	s, ok := r.System.(string)
	if !ok {
		t.Fatalf("system should stay a string when no cache hints, got %T", r.System)
	}
	if s != "first\n\nsecond" {
		t.Fatalf("system text wrong: %q", s)
	}
}

func TestBuildUserMessage_CacheControlForcesBlockArray(t *testing.T) {
	// A single-text user message normally collapses to a string, but a cache
	// hint must promote it to a block array.
	cc := map[string]any{"type": "ephemeral"}
	msg := provider.Message{
		Role: "user",
		Content: []provider.ContentPart{
			{Type: "text", Text: "long doc...", CacheControl: cc},
		},
	}
	out := buildUserMessage(msg)
	blocks, ok := out.Content.([]contentBlock)
	if !ok {
		t.Fatalf("content must be []contentBlock when cache_control is set, got %T", out.Content)
	}
	if len(blocks) != 1 || blocks[0].CacheControl == nil {
		t.Fatalf("cache_control lost: %+v", blocks)
	}
}

func TestValidateRequest_RejectsN_GreaterThanOne(t *testing.T) {
	n := 2
	err := validateRequest(&provider.ChatCompletionRequest{N: &n})
	if err == nil {
		t.Fatalf("expected n>1 to be rejected")
	}
	pe, ok := err.(*provider.ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.StatusCode != 422 {
		t.Fatalf("expected 422, got %d", pe.StatusCode)
	}

	// n=1 should pass (explicit single completion)
	one := 1
	if err := validateRequest(&provider.ChatCompletionRequest{N: &one}); err != nil {
		t.Fatalf("n=1 must be allowed, got %v", err)
	}
	// nil n should pass (default behavior)
	if err := validateRequest(&provider.ChatCompletionRequest{}); err != nil {
		t.Fatalf("nil n must be allowed, got %v", err)
	}
}

func TestValidateRequest_RejectsNonPositiveMaxTokens(t *testing.T) {
	zero := 0
	err := validateRequest(&provider.ChatCompletionRequest{MaxTokens: &zero})
	if err == nil {
		t.Fatalf("expected max_tokens=0 to be rejected")
	}
	neg := -10
	err = validateRequest(&provider.ChatCompletionRequest{MaxCompletionTokens: &neg})
	if err == nil {
		t.Fatalf("expected max_completion_tokens<0 to be rejected")
	}
}

func TestRequiredBetas_AutoSetsForThinkingPlusTools(t *testing.T) {
	// Thinking + tools should auto-enable interleaved-thinking-2025-05-14 so
	// the model can think between tool calls.
	betas := requiredBetas("claude-3-5-sonnet-20241022", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Thinking: &provider.ThinkingConfig{Type: "enabled", BudgetTokens: 1024},
		Tools: []provider.Tool{{
			Type:     "function",
			Function: provider.ToolFunction{Name: "f"},
		}},
	})
	if !strings.Contains(betas, "interleaved-thinking-2025-05-14") {
		t.Fatalf("interleaved-thinking beta missing: %q", betas)
	}
}

func TestRequiredBetas_AutoSetsForExtendedCacheTTL(t *testing.T) {
	cc := map[string]any{"type": "ephemeral", "ttl": "1h"}
	betas := requiredBetas("claude-3-5-sonnet-20241022", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{
			Role: "user",
			Content: []provider.ContentPart{
				{Type: "text", Text: "long doc", CacheControl: cc},
			},
		}},
	})
	if !strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
		t.Fatalf("extended-cache-ttl beta missing: %q", betas)
	}
}

func TestRequiredBetas_NoExtraHeadersByDefault(t *testing.T) {
	betas := requiredBetas("claude-3-5-sonnet-20241022", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if betas != "" {
		t.Fatalf("plain request must not set beta header, got %q", betas)
	}
}

func TestRequiredBetas_AddsContext1M_ForClaude4Family(t *testing.T) {
	// Claude 4+ models (Opus 4.x, Sonnet 4.x) need the context-1m beta header to
	// unlock the 1M token window. Anthropic still requires this on natively-
	// supporting models (Opus 4.6+, Sonnet 4.6) — no header → caps at 200k.
	cases := []struct {
		model string
		want  bool
	}{
		{"claude-opus-4-7", true},
		{"claude-opus-4-7-20251215", true},
		{"claude-opus-4-6", true},
		{"claude-sonnet-4-6", true},
		{"claude-sonnet-4-5-20250929", true},
		{"claude-opus-4-1-20250805", true},
		{"claude-sonnet-4-20250514", true},
		// Claude 3.x: no 1M context support.
		{"claude-3-5-sonnet-20241022", false},
		{"claude-3-5-haiku-20241022", false},
		{"claude-3-opus-20240229", false},
		// Vendor-prefixed (e.g. via OpenRouter routing through anthropic provider directly): still detect.
		{"anthropic/claude-opus-4-7", true},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			betas := requiredBetas(tc.model, &provider.ChatCompletionRequest{
				Messages: []provider.Message{{Role: "user", Content: "x"}},
			})
			has := strings.Contains(betas, "context-1m-2025-08-07")
			if has != tc.want {
				t.Fatalf("model=%s: want context-1m=%v, got betas=%q", tc.model, tc.want, betas)
			}
		})
	}
}

func TestBuildRequest_DisableParallelToolUse_FlagsToolChoice(t *testing.T) {
	// OpenAI clients pass parallel_tool_calls=false; Anthropic carries the
	// opt-out under tool_choice.disable_parallel_tool_use instead.
	parallel := false
	r, _ := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Tools: []provider.Tool{{
			Type:     "function",
			Function: provider.ToolFunction{Name: "f"},
		}},
		ParallelToolCalls: &parallel,
	})
	if r.ToolChoice == nil {
		t.Fatal("tool_choice must be synthesized when parallel_tool_calls=false but no tool_choice was given")
	}
	if !r.ToolChoice.DisableParallelToolUse {
		t.Errorf("disable_parallel_tool_use = false, want true")
	}
}

func TestBuildRequest_DisableParallelToolUse_PreservedAcrossJSONSchemaRewrite(t *testing.T) {
	// 当客户端同时传 parallel_tool_calls=false + response_format=json_schema 且无 tools，
	// json_schema 路径会用 ToolChoice={Type:tool, Name:X} 替换之前合成的 auto。
	// 确保 DisableParallelToolUse 标记被保留而不是丢失。
	parallel := false
	r, _ := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages:          []provider.Message{{Role: "user", Content: "x"}},
		ParallelToolCalls: &parallel,
		ResponseFormat: &provider.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &provider.JSONSchemaSpec{
				Name:   "answer",
				Schema: map[string]any{"type": "object"},
			},
		},
	})
	if r.ToolChoice == nil || r.ToolChoice.Type != "tool" || r.ToolChoice.Name != "answer" {
		t.Fatalf("json_schema tool_choice not set correctly: %+v", r.ToolChoice)
	}
	if !r.ToolChoice.DisableParallelToolUse {
		t.Errorf("json_schema rewrite dropped DisableParallelToolUse")
	}
}

func TestBuildRequest_ExtraToolsAppendedAsRaw(t *testing.T) {
	// Anthropic built-in tools (web_search etc.) ship through ExtraTools and
	// must survive the request build verbatim — the upstream rejects unknown
	// fields like input_schema on a web_search tool.
	r, _ := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		// Mix a regular function tool with two built-in tools to exercise both paths.
		Tools: []provider.Tool{{
			Type:     "function",
			Function: provider.ToolFunction{Name: "calc", Parameters: map[string]any{"type": "object"}},
		}},
		ExtraTools: []any{
			map[string]any{
				"type":     "web_search_20250305",
				"name":     "web_search",
				"max_uses": 5,
			},
			map[string]any{
				"type": "code_execution_20250522",
				"name": "code_execution",
			},
		},
	})
	if len(r.Tools) != 3 {
		t.Fatalf("want 3 tools (1 function + 2 builtin), got %d: %+v", len(r.Tools), r.Tools)
	}
	// Function tool first.
	if _, ok := r.Tools[0].(anthropicTool); !ok {
		t.Errorf("first tool must be anthropicTool, got %T", r.Tools[0])
	}
	// Built-in tools must round-trip as the original maps.
	web, ok := r.Tools[1].(map[string]any)
	if !ok || web["type"] != "web_search_20250305" || web["max_uses"] != 5 {
		t.Errorf("web_search tool corrupted: %+v", r.Tools[1])
	}
	code, ok := r.Tools[2].(map[string]any)
	if !ok || code["type"] != "code_execution_20250522" {
		t.Errorf("code_execution tool corrupted: %+v", r.Tools[2])
	}
}

func TestCitationToAnnotation_WebSearchURLCitation(t *testing.T) {
	got := citationToAnnotation(citation{
		Type:           "web_search_result_location",
		URL:            "https://example.com",
		Title:          "Example",
		EncryptedIndex: "OPAQUE_INDEX",
		CitedText:      "fact about X",
	})
	if got["type"] != "url_citation" {
		t.Fatalf("type = %v, want url_citation", got["type"])
	}
	urlCit, _ := got["url_citation"].(map[string]any)
	if urlCit["url"] != "https://example.com" || urlCit["title"] != "Example" {
		t.Errorf("url/title not preserved: %+v", urlCit)
	}
	if urlCit["encrypted_index"] != "OPAQUE_INDEX" {
		t.Errorf("encrypted_index lost — multi-turn round-trip will break: %+v", urlCit)
	}
	if urlCit["cited_text"] != "fact about X" {
		t.Errorf("cited_text lost: %+v", urlCit)
	}
}

func TestCitationToAnnotation_DocumentCitationKeepsLocators(t *testing.T) {
	startChar := 100
	endChar := 200
	docIdx := 0
	got := citationToAnnotation(citation{
		Type:           "char_location",
		EncryptedIndex: "DOC_OPAQUE",
		CitedText:      "from page 5",
		DocumentIndex:  &docIdx,
		StartCharIndex: &startChar,
		EndCharIndex:   &endChar,
	})
	if got["type"] != "file_citation" {
		t.Fatalf("type = %v, want file_citation", got["type"])
	}
	if _, exists := got["citation_type"]; exists {
		t.Errorf("citation_type must NOT be at the OpenAI annotation top level — strict clients reject unknown keys: %+v", got)
	}
	fc, _ := got["file_citation"].(map[string]any)
	if fc["start_char_index"] != 100 || fc["end_char_index"] != 200 {
		t.Errorf("locators lost: %+v", fc)
	}
	if fc["anthropic_citation_type"] != "char_location" {
		t.Errorf("anthropic_citation_type lost from file_citation — round-trip impossible: %+v", fc)
	}
}

func TestConvertResponse_PreservesProviderTurnBlocks(t *testing.T) {
	// Response with web_search server-tool blocks — these don't fit OpenAI
	// shape but must round-trip so multi-turn citation continuity works.
	resp := &response{
		ID:    "m_1",
		Model: "claude-opus-4-7",
		Content: []responseBlock{
			{Type: "server_tool_use", ID: "srv_01", Name: "web_search"},
			{Type: "web_search_tool_result", ID: "wsr_01"},
			{Type: "text", Text: "Result from web search."},
		},
		StopReason: "end_turn",
	}
	rawContent := []map[string]any{
		{
			"type":  "server_tool_use",
			"id":    "srv_01",
			"name":  "web_search",
			"input": map[string]any{"query": "anthropic citation"},
		},
		{
			"type":        "web_search_tool_result",
			"tool_use_id": "srv_01",
			"content": []any{
				map[string]any{
					"type":              "web_search_result",
					"url":               "https://example.com",
					"title":             "Example",
					"encrypted_content": "OPAQUE_BLOB",
				},
			},
		},
		{"type": "text", "text": "Result from web search."},
	}
	out := convertResponse(resp, rawContent, "")
	msg := out.Choices[0].Message
	if len(msg.ProviderTurnBlocks) != 2 {
		t.Fatalf("expected 2 provider turn blocks (server_tool_use + web_search_tool_result), got %d: %+v",
			len(msg.ProviderTurnBlocks), msg.ProviderTurnBlocks)
	}
	srvUse, _ := msg.ProviderTurnBlocks[0].(map[string]any)
	if srvUse["type"] != "server_tool_use" || srvUse["id"] != "srv_01" {
		t.Errorf("server_tool_use block corrupted: %+v", srvUse)
	}
	tr, _ := msg.ProviderTurnBlocks[1].(map[string]any)
	if tr["type"] != "web_search_tool_result" {
		t.Errorf("web_search_tool_result block missing/corrupted: %+v", tr)
	}
	// Critical: encrypted_content must survive verbatim — it's what Anthropic
	// requires echoed back for multi-turn citations to keep working.
	contentArr, _ := tr["content"].([]any)
	if len(contentArr) == 0 {
		t.Fatalf("web_search_tool_result.content lost")
	}
	first, _ := contentArr[0].(map[string]any)
	if first["encrypted_content"] != "OPAQUE_BLOB" {
		t.Errorf("encrypted_content lost — multi-turn round-trip broken: %+v", first)
	}
}

func TestBuildAssistantMessage_RoundTripsProviderTurnBlocks(t *testing.T) {
	// Client echoed back the assistant message including provider_turn_blocks.
	// buildAssistantMessage must re-emit those blocks verbatim — the gateway
	// can't reconstruct them from OpenAI shape alone.
	rawWebSearch := map[string]any{
		"type":        "web_search_tool_result",
		"tool_use_id": "srv_01",
		"content": []any{
			map[string]any{
				"type":              "web_search_result",
				"url":               "https://example.com",
				"encrypted_content": "OPAQUE_BLOB_ROUND_TRIPS",
			},
		},
	}
	rawSrvUse := map[string]any{
		"type":  "server_tool_use",
		"id":    "srv_01",
		"name":  "web_search",
		"input": map[string]any{"query": "x"},
	}

	out := buildAssistantMessage(provider.Message{
		Role:               "assistant",
		Content:            "Result text.",
		ProviderTurnBlocks: []any{rawSrvUse, rawWebSearch},
	})
	blocks, ok := out.Content.([]contentBlock)
	if !ok {
		t.Fatalf("expected []contentBlock when ProviderTurnBlocks present, got %T", out.Content)
	}
	// Serialize and confirm raw blocks survive.
	bytes, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(bytes)
	if !strings.Contains(got, "OPAQUE_BLOB_ROUND_TRIPS") {
		t.Errorf("encrypted_content didn't round-trip through MarshalJSON: %s", got)
	}
	if !strings.Contains(got, `"type":"server_tool_use"`) {
		t.Errorf("server_tool_use block lost on serialize: %s", got)
	}
	if !strings.Contains(got, `"type":"web_search_tool_result"`) {
		t.Errorf("web_search_tool_result block lost on serialize: %s", got)
	}
}

func TestBuildRequest_DisableParallelToolUse_LeavesToolChoiceNoneAlone(t *testing.T) {
	// "none" semantics is "do not call tools at all"; setting
	// disable_parallel_tool_use on it is contradictory and Anthropic rejects it.
	parallel := false
	r, _ := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages:          []provider.Message{{Role: "user", Content: "x"}},
		Tools:             []provider.Tool{{Type: "function", Function: provider.ToolFunction{Name: "f"}}},
		ToolChoice:        "none",
		ParallelToolCalls: &parallel,
	})
	if r.ToolChoice == nil || r.ToolChoice.Type != "none" {
		t.Fatalf("tool_choice should be none, got %+v", r.ToolChoice)
	}
	if r.ToolChoice.DisableParallelToolUse {
		t.Error("must not set disable_parallel_tool_use when tool_choice=none")
	}
}

func TestBuildRequest_PassesThinkingDisplayField(t *testing.T) {
	// Display 字段透传给 Anthropic 的 thinking 块，让客户端能选择 summarized 或 omitted。
	r, _ := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Thinking: &provider.ThinkingConfig{Type: "enabled", BudgetTokens: 1024, Display: "omitted"},
	})
	if r.Thinking == nil {
		t.Fatal("thinking not set")
	}
	if r.Thinking.Display != "omitted" {
		t.Fatalf("display = %q, want omitted", r.Thinking.Display)
	}
}

func TestRequiredBetas_DefaultTTLDoesNotTriggerHeader(t *testing.T) {
	// Default 5m ttl is GA; only non-default ttl values need the beta header.
	cc := map[string]any{"type": "ephemeral", "ttl": "5m"}
	betas := requiredBetas("claude-3-5-sonnet-20241022", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{
			Role: "user",
			Content: []provider.ContentPart{
				{Type: "text", Text: "x", CacheControl: cc},
			},
		}},
	})
	if strings.Contains(betas, "extended-cache-ttl") {
		t.Fatalf("default ttl=5m must not trigger extended-cache-ttl beta: %q", betas)
	}
}

func TestConvertResponse_ExtractsRedactedThinking(t *testing.T) {
	resp := &response{
		ID:    "m_1",
		Model: "claude-opus-4-7",
		Content: []responseBlock{
			{Type: "redacted_thinking", Data: "ENCRYPTED_BLOB_1"},
			{Type: "text", Text: "answer"},
			{Type: "redacted_thinking", Data: "ENCRYPTED_BLOB_2"},
		},
		StopReason: "end_turn",
	}
	out := convertResponse(resp, nil, "")
	msg := out.Choices[0].Message
	if len(msg.RedactedThinking) != 2 {
		t.Fatalf("expected 2 redacted blobs, got %d", len(msg.RedactedThinking))
	}
	if msg.RedactedThinking[0] != "ENCRYPTED_BLOB_1" || msg.RedactedThinking[1] != "ENCRYPTED_BLOB_2" {
		t.Fatalf("redacted blobs lost or reordered: %+v", msg.RedactedThinking)
	}
}

func TestBuildAssistantMessage_RoundTripsRedactedThinking(t *testing.T) {
	out := buildAssistantMessage(provider.Message{
		Role:             "assistant",
		Content:          "ok",
		RedactedThinking: []string{"BLOB1", "BLOB2"},
	})
	blocks, ok := out.Content.([]contentBlock)
	if !ok {
		t.Fatalf("expected []contentBlock when redacted thinking present, got %T", out.Content)
	}
	redactedCount := 0
	for _, b := range blocks {
		if b.Type == "redacted_thinking" {
			redactedCount++
			if b.Data == "" {
				t.Fatalf("redacted_thinking block must carry its data: %+v", b)
			}
		}
	}
	if redactedCount != 2 {
		t.Fatalf("expected 2 redacted_thinking blocks, got %d", redactedCount)
	}
}

func TestConvertResponse_ExtractsThinkingSignature(t *testing.T) {
	resp := &response{
		ID:    "m_1",
		Model: "claude-opus-4-7",
		Content: []responseBlock{
			{Type: "thinking", Thinking: "let me think", Signature: "sig123"},
			{Type: "text", Text: "answer"},
		},
		StopReason: "end_turn",
	}
	out := convertResponse(resp, nil, "")
	if len(out.Choices) == 0 || out.Choices[0].Message == nil {
		t.Fatalf("response has no message")
	}
	msg := out.Choices[0].Message
	if msg.ReasoningContent == nil || *msg.ReasoningContent != "let me think" {
		t.Fatalf("reasoning_content not extracted: %+v", msg.ReasoningContent)
	}
	if msg.ReasoningContentSignature == nil || *msg.ReasoningContentSignature != "sig123" {
		t.Fatalf("reasoning_content_signature not extracted: %+v", msg.ReasoningContentSignature)
	}
}

func TestBuildAssistantMessage_RoundTripsThinkingSignature(t *testing.T) {
	// Critical compat path: extended thinking + tool_use multi-turn requires
	// the original thinking block (text + signature) to be echoed back. The
	// gateway preserves the signature via Message.ReasoningContentSignature.
	thinking := "Let me work this out..."
	sig := "EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkz..."
	idx := 0
	out := buildAssistantMessage(provider.Message{
		Role:                      "assistant",
		Content:                   "ok, calling tool",
		ReasoningContent:          &thinking,
		ReasoningContentSignature: &sig,
		ToolCalls: []provider.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: provider.ToolCallFunction{Name: "f", Arguments: "{}"},
			Index:    &idx,
		}},
	})
	blocks, ok := out.Content.([]contentBlock)
	if !ok {
		t.Fatalf("expected []contentBlock with thinking + tool_use, got %T", out.Content)
	}
	// First block should be thinking with the signature attached.
	if blocks[0].Type != "thinking" {
		t.Fatalf("first block must be thinking, got %s", blocks[0].Type)
	}
	if blocks[0].Thinking != thinking {
		t.Fatalf("thinking text lost: %s", blocks[0].Thinking)
	}
	if blocks[0].Signature != sig {
		t.Fatalf("signature lost: %s", blocks[0].Signature)
	}
}

func TestBuildAssistantMessage_PreservesCacheControlOnContent(t *testing.T) {
	// Long assistant responses can be cached for multi-turn re-use; the cache
	// hint must survive to the Anthropic content block, not be flattened to
	// a string.
	cc := map[string]any{"type": "ephemeral"}
	out := buildAssistantMessage(provider.Message{
		Role: "assistant",
		Content: []provider.ContentPart{
			{Type: "text", Text: "long answer", CacheControl: cc},
		},
	})
	blocks, ok := out.Content.([]contentBlock)
	if !ok {
		t.Fatalf("cache-marked assistant must serialize as []contentBlock, got %T", out.Content)
	}
	if len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].CacheControl == nil {
		t.Fatalf("cache_control lost: %+v", blocks)
	}
}

func TestBuildAssistantMessage_NoSignatureNoThinkingBlock(t *testing.T) {
	// Without a signature, we must NOT emit a thinking block (would be rejected
	// by Anthropic). Plain echo behavior preserved for messages without
	// thinking metadata.
	thinking := "thoughts without signature"
	idx := 0
	out := buildAssistantMessage(provider.Message{
		Role:             "assistant",
		Content:          "ok",
		ReasoningContent: &thinking, // signature missing
		ToolCalls: []provider.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: provider.ToolCallFunction{Name: "f", Arguments: "{}"},
			Index:    &idx,
		}},
	})
	blocks, ok := out.Content.([]contentBlock)
	if !ok {
		t.Fatalf("expected []contentBlock for tool_calls path, got %T", out.Content)
	}
	for _, b := range blocks {
		if b.Type == "thinking" {
			t.Fatalf("must not emit thinking block without signature, got %+v", b)
		}
	}
}

func TestBuildToolResultBlock_PreservesImages(t *testing.T) {
	// OpenAI tool messages can return image results (screenshot from a code
	// execution tool, for example). The Anthropic side accepts nested content
	// blocks; collapsing to a string would drop the images.
	msg := provider.Message{
		Role:       "tool",
		ToolCallID: "call_42",
		Content: []provider.ContentPart{
			{Type: "text", Text: "rendered"},
			{Type: "image_url", ImageURL: &provider.ImageURL{URL: "data:image/png;base64,YWJj"}},
		},
	}
	block := buildToolResultBlock(msg)
	if block.Type != "tool_result" || block.ToolUseID != "call_42" {
		t.Fatalf("envelope wrong: %+v", block)
	}
	blocks, ok := block.Content.([]contentBlock)
	if !ok {
		t.Fatalf("multimodal tool_result must serialize as nested content blocks, got %T: %+v", block.Content, block.Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("want 2 nested blocks, got %d", len(blocks))
	}
	if blocks[0].Type != "text" || blocks[0].Text != "rendered" {
		t.Fatalf("text block wrong: %+v", blocks[0])
	}
	if blocks[1].Type != "image" || blocks[1].Source == nil {
		t.Fatalf("image block wrong: %+v", blocks[1])
	}
}

func TestBuildToolResultBlock_TextOnlyKeepsString(t *testing.T) {
	// Plain-text tool results stay as a string for compactness; this is the
	// common case and the JSON wire bytes should match the legacy path.
	msg := provider.Message{
		Role:       "tool",
		ToolCallID: "call_42",
		Content:    "simple string",
	}
	block := buildToolResultBlock(msg)
	if s, ok := block.Content.(string); !ok || s != "simple string" {
		t.Fatalf("text-only tool_result should stay a string, got %T %v", block.Content, block.Content)
	}
}

func TestBuildRequest_SystemMessageMerging(t *testing.T) {
	req := &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "system", Content: "first"},
			{Role: "system", Content: "second"},
			{Role: "user", Content: "hi"},
		},
	}
	r, _ := buildRequest("claude-opus-4-7", req)
	sys, ok := r.System.(string)
	if !ok {
		t.Fatalf("system should be a string, got %T", r.System)
	}
	if !strings.Contains(sys, "first") || !strings.Contains(sys, "second") {
		t.Fatalf("both system messages must be merged, got %q", sys)
	}
}
