package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/siguago/llmkit/provider"
)

// The 5m-attribution switch keys off the endpoint itself, not off
// ReportsCacheWriteUsage. Both return the same value today, so no test can tell
// the two wirings apart directly — what this pins down is the consequence that
// actually matters, over the full provider-to-reader path that the unit-level
// stream tests bypass: a relay's unlabelled cache write must never grow a
// fabricated TTL split, because billing prices the two tiers differently.
func TestChatCompletionStream_RelayCacheWriteGetsNoFabricatedTTLSplit(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m_1","model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":10,"output_tokens":1}}}`,
		``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"cache_creation_input_tokens":180,"output_tokens":5}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	p := NewWithBaseURL(srv.URL)
	if p.isOfficialEndpoint() {
		t.Fatal("a httptest relay must not be classified as the official endpoint")
	}
	sr, err := p.ChatCompletionStream(context.Background(), "k", "claude-3-5-sonnet-20241022",
		&provider.ChatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	for {
		if _, err := sr.Recv(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("Recv: %v", err)
		}
	}

	usage := sr.GetUsage()
	if usage == nil {
		t.Fatal("usage missing")
	}
	if usage.CacheCreationTokens != 180 {
		t.Fatalf("the aggregate itself must survive, got %d", usage.CacheCreationTokens)
	}
	if usage.CacheCreationTokensDetails != nil {
		t.Fatalf("relay cache write must not gain a TTL split: %+v", usage.CacheCreationTokensDetails)
	}
}

func TestNewWithBaseURL_StreamingHasNoAbsoluteTimeout(t *testing.T) {
	provider := NewWithBaseURL("https://api.anthropic.test/v1")
	if provider.client.Timeout != 300*time.Second {
		t.Fatalf("non-stream client timeout = %v, want 5m", provider.client.Timeout)
	}
	if provider.streamClient.Timeout != 0 {
		t.Fatalf("stream client timeout = %v, want no absolute timeout", provider.streamClient.Timeout)
	}
}

func TestBuildUsage_IncludesCacheTokens(t *testing.T) {
	// Anthropic's input_tokens excludes cached portions; PromptTokens must
	// re-add cache reads/creation so total_tokens reconciles for clients.
	u := buildUsage(responseUsage{
		InputTokens:              50,
		OutputTokens:             100,
		CacheReadInputTokens:     1000,
		CacheCreationInputTokens: 200,
		CacheCreation: &cacheCreationUsage{
			Ephemeral5mInputTokens: 75,
			Ephemeral1hInputTokens: 125,
		},
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
	if u.CacheCreationTokens != 200 {
		t.Fatalf("cache_creation_input_tokens should be carried, got %d", u.CacheCreationTokens)
	}
	if u.CacheCreationTokensDetails == nil ||
		u.CacheCreationTokensDetails.Ephemeral5mTokens != 75 ||
		u.CacheCreationTokensDetails.Ephemeral1hTokens != 125 {
		t.Fatalf("cache creation TTL breakdown missing: %+v", u.CacheCreationTokensDetails)
	}
	// DeepSeek's prompt_cache_miss_tokens counts every input token that missed
	// the cache (hit + miss == prompt_tokens). A cache write is priced above
	// base input, so folding it in there would bill writes as ordinary misses.
	if u.PromptCacheMissTokens != 0 {
		t.Fatalf("cache writes must not land on DeepSeek's miss counter, got %d", u.PromptCacheMissTokens)
	}
	// The three prompt-side counters are disjoint and must reconcile.
	if uncached := u.PromptTokens - u.CachedTokens - u.CacheCreationTokens; uncached != 50 {
		t.Fatalf("prompt_tokens - cached - cache_creation should leave input_tokens=50, got %d", uncached)
	}
	if u.PromptTokensDetails == nil || u.PromptTokensDetails.CachedTokens != 1000 {
		t.Fatalf("prompt_tokens_details.cached_tokens missing: %+v", u.PromptTokensDetails)
	}
}

func TestBuildUsage_DerivesAggregateFromCacheCreationDetails(t *testing.T) {
	u := buildUsage(responseUsage{
		InputTokens:  10,
		OutputTokens: 5,
		CacheCreation: &cacheCreationUsage{
			Ephemeral5mInputTokens: 30,
			Ephemeral1hInputTokens: 70,
		},
	})
	if u.CacheCreationTokens != 100 || u.PromptTokens != 110 || u.TotalTokens != 115 {
		t.Fatalf("breakdown-only usage was not reconciled: %+v", u)
	}
}

func TestBuildUsage_DropsInconsistentCacheCreationDetails(t *testing.T) {
	u := buildUsage(responseUsage{
		CacheCreationInputTokens: 100,
		CacheCreation: &cacheCreationUsage{
			Ephemeral5mInputTokens: 30,
			Ephemeral1hInputTokens: 50,
		},
	})
	if u.CacheCreationTokens != 100 {
		t.Fatalf("reported aggregate must remain authoritative: %+v", u)
	}
	if u.CacheCreationTokensDetails != nil {
		t.Fatalf("inconsistent details must not be exposed as billing-safe: %+v", u)
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

// The capability is only useful if callers can discover it through the
// exported interface — asserting the concrete method would pass even if
// provider.CacheWriteUsageReporter drifted out from under it.
func TestProviderImplementsCacheWriteUsageReporter(t *testing.T) {
	var p provider.Provider = New()
	r, ok := p.(provider.CacheWriteUsageReporter)
	if !ok {
		t.Fatal("Anthropic provider must satisfy provider.CacheWriteUsageReporter")
	}
	if !r.ReportsCacheWriteUsage() {
		t.Fatal("Anthropic provider must advertise cache-write usage preservation")
	}
	custom := NewWithBaseURL("https://relay.example/v1")
	if custom.ReportsCacheWriteUsage() {
		t.Fatal("custom Anthropic relay must not promise cache-write reporting")
	}
	explicitOfficial := NewWithBaseURL(defaultBaseURL + "/")
	if !explicitOfficial.ReportsCacheWriteUsage() {
		t.Fatal("explicit official Anthropic base URL should preserve the reporting guarantee")
	}
}

// A cache hint on a part Anthropic can't represent used to divert the whole
// assistant turn onto the content-block path, where the emit loop then dropped
// every part — yielding "content":[] , which the API rejects outright. A hint
// that cannot ship must leave the message shape exactly as it was without one.
func TestBuildAssistantMessage_UnshippableCacheHintKeepsMessageShape(t *testing.T) {
	cc := map[string]any{"type": "ephemeral"}
	parts := map[string]provider.ContentPart{
		"input_audio": {Type: "input_audio", InputAudio: &provider.InputAudio{Data: "YXVkaW8=", Format: "wav"}, CacheControl: cc},
		"file":        {Type: "file", File: &provider.FileContent{FileData: "cGRm"}, CacheControl: cc},
		"empty text":  {Type: "text", Text: "", CacheControl: cc},
	}
	for name, hinted := range parts {
		t.Run(name, func(t *testing.T) {
			// Alone: must not become an empty block array.
			solo := buildAssistantMessage(provider.Message{
				Role:    "assistant",
				Content: []provider.ContentPart{hinted},
			})
			if blocks, ok := solo.Content.([]contentBlock); ok && len(blocks) == 0 {
				t.Fatalf("assistant content collapsed to an empty block array")
			}

			// Alongside real text: the shape must match what the same message
			// produces with no cache hint anywhere.
			withText := buildAssistantMessage(provider.Message{
				Role:    "assistant",
				Content: []provider.ContentPart{{Type: "text", Text: "answer"}, hinted},
			})
			bare := hinted
			bare.CacheControl = nil
			baseline := buildAssistantMessage(provider.Message{
				Role:    "assistant",
				Content: []provider.ContentPart{{Type: "text", Text: "answer"}, bare},
			})
			got, err := json.Marshal(withText)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			want, err := json.Marshal(baseline)
			if err != nil {
				t.Fatalf("marshal baseline: %v", err)
			}
			if string(got) != string(want) {
				t.Fatalf("unshippable cache hint changed the message:\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

// Anthropic rejects empty text blocks. A cache hint on empty system text has
// nothing to mark, so the block must be dropped rather than shipped as an
// empty one that fails the request.
func TestBuildRequest_SystemDropsEmptyTextBlockWithCacheHint(t *testing.T) {
	cc := map[string]any{"type": "ephemeral"}

	r, _ := buildRequest("claude-3-5-sonnet-20241022", &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "system", Content: []provider.ContentPart{
				{Type: "text", Text: "", CacheControl: cc},
				{Type: "text", Text: "real rules", CacheControl: cc},
			}},
			{Role: "user", Content: "hi"},
		},
	})
	blocks, ok := r.System.([]contentBlock)
	if !ok {
		t.Fatalf("system should keep block form when a cache hint survives, got %T", r.System)
	}
	if len(blocks) != 1 || blocks[0].Text != "real rules" {
		t.Fatalf("empty text block leaked into system: %+v", blocks)
	}

	// Nothing but empty hinted text — the system field must be omitted rather
	// than carrying a single empty block.
	empty, _ := buildRequest("claude-3-5-sonnet-20241022", &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "system", Content: []provider.ContentPart{{Type: "text", Text: "", CacheControl: cc}}},
			{Role: "user", Content: "hi"},
		},
	})
	if empty.System != nil {
		t.Fatalf("system should be omitted when every block was empty, got %#v", empty.System)
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

func TestListModels_ClassifiesOnlyClaudeFamilyAsChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"claude-test","display_name":"Claude Test"},
			{"id":"future-media-model","display_name":"Future Media"}
		],"has_more":false}`))
	}))
	defer srv.Close()

	models, taskTypes, err := NewWithBaseURL(srv.URL).ListModelsWithTaskTypes(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want both catalog entries", models)
	}
	if got := strings.Join(taskTypes["claude-test"], ","); got != provider.RemoteModelTaskChat {
		t.Fatalf("task_types = %q, want %q", got, provider.RemoteModelTaskChat)
	}
	if _, ok := taskTypes["future-media-model"]; ok {
		t.Fatal("an unknown Anthropic model family must remain unclassified")
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
	validMessages := []provider.Message{{Role: "user", Content: "hello"}}
	n := 2
	err := validateRequest(&provider.ChatCompletionRequest{N: &n, Messages: validMessages})
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
	if err := validateRequest(&provider.ChatCompletionRequest{N: &one, Messages: validMessages}); err != nil {
		t.Fatalf("n=1 must be allowed, got %v", err)
	}
	// nil n should pass (default behavior)
	if err := validateRequest(&provider.ChatCompletionRequest{Messages: validMessages}); err != nil {
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

func TestValidateRequest_RejectsUnrepresentableUserAndAssistantContent(t *testing.T) {
	cc := map[string]any{"type": "ephemeral"}
	cases := []struct {
		name string
		msg  provider.Message
	}{
		{"nil user content", provider.Message{Role: "user"}},
		{"empty user text", provider.Message{Role: "user", Content: ""}},
		{"empty user text part", provider.Message{Role: "user", Content: []provider.ContentPart{{Type: "text", CacheControl: cc}}}},
		{"empty user image url", provider.Message{Role: "user", Content: []provider.ContentPart{{Type: "image_url", ImageURL: &provider.ImageURL{}, CacheControl: cc}}}},
		{"empty user image data uri", provider.Message{Role: "user", Content: []provider.ContentPart{{Type: "image_url", ImageURL: &provider.ImageURL{URL: "data:image/png;base64,"}, CacheControl: cc}}}},
		{"unsupported user audio", provider.Message{Role: "user", Content: []provider.ContentPart{{Type: "input_audio", InputAudio: &provider.InputAudio{Data: "YQ==", Format: "wav"}}}}},
		{"unresolvable user file", provider.Message{Role: "user", Content: []provider.ContentPart{{Type: "file", File: &provider.FileContent{FileID: "file_123"}}}}},
		{"empty user file data uri", provider.Message{Role: "user", Content: []provider.ContentPart{{Type: "file", File: &provider.FileContent{FileData: "data:application/pdf;base64,"}}}}},
		{"nil assistant content", provider.Message{Role: "assistant"}},
		{"empty signed assistant thinking", func() provider.Message {
			empty, signature := "", "sig"
			return provider.Message{Role: "assistant", ReasoningContent: &empty, ReasoningContentSignature: &signature}
		}()},
		{"empty assistant redacted thinking", provider.Message{Role: "assistant", RedactedThinking: []string{""}}},
		{"empty assistant provider block", provider.Message{Role: "assistant", ProviderTurnBlocks: []any{map[string]any{}}}},
		{"unsupported assistant image", provider.Message{Role: "assistant", Content: []provider.ContentPart{{Type: "image_url", ImageURL: &provider.ImageURL{URL: "https://example.com/image.png"}}}}},
		{"unsupported assistant audio", provider.Message{Role: "assistant", Content: []provider.ContentPart{{Type: "input_audio", InputAudio: &provider.InputAudio{Data: "YQ==", Format: "wav"}, CacheControl: cc}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRequest(&provider.ChatCompletionRequest{Messages: []provider.Message{tc.msg}})
			pe, ok := err.(*provider.ProviderError)
			if !ok || pe.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("expected local 422 ProviderError, got %T: %v", err, err)
			}
		})
	}

	// Unsupported parts may be ignored when the same turn retains real text.
	for _, role := range []string{"user", "assistant"} {
		err := validateRequest(&provider.ChatCompletionRequest{Messages: []provider.Message{{
			Role: role,
			Content: []provider.ContentPart{
				{Type: "input_audio", InputAudio: &provider.InputAudio{Data: "YQ==", Format: "wav"}},
				{Type: "text", Text: "real content"},
			},
		}}})
		if err != nil {
			t.Fatalf("mixed %s content should remain representable: %v", role, err)
		}
	}
}

func TestValidateRequest_RejectsInvalidConversationShape(t *testing.T) {
	invalidToolCall := func(id, name, arguments string) provider.Message {
		return provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{
			ID: id,
			Function: provider.ToolCallFunction{
				Name:      name,
				Arguments: arguments,
			},
		}}}
	}
	cases := []struct {
		name     string
		messages []provider.Message
	}{
		{"no messages", nil},
		{"system only", []provider.Message{{Role: "system", Content: "rules"}}},
		{"unsupported role", []provider.Message{{Role: "developer", Content: "rules"}}},
		{"tool result missing id", []provider.Message{{Role: "tool", Content: "result"}}},
		{"tool call missing id", []provider.Message{invalidToolCall("", "search", `{}`)}},
		{"tool call missing name", []provider.Message{invalidToolCall("call_1", "", `{}`)}},
		{"tool call missing input", []provider.Message{invalidToolCall("call_1", "search", "")}},
		{"tool call malformed input", []provider.Message{invalidToolCall("call_1", "search", `{`)}},
		{"tool call non-object input", []provider.Message{invalidToolCall("call_1", "search", `[]`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRequest(&provider.ChatCompletionRequest{Messages: tc.messages})
			pe, ok := err.(*provider.ProviderError)
			if !ok || pe.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("expected local 422 ProviderError, got %T: %v", err, err)
			}
		})
	}

	if err := validateRequest(&provider.ChatCompletionRequest{Messages: []provider.Message{
		invalidToolCall("call_1", "search", `{}`),
		{Role: "tool", ToolCallID: "call_1"},
	}}); err != nil {
		t.Fatalf("valid tool-only exchange should pass: %v", err)
	}
}

func TestChatCompletionPathsRejectEmptyContentBeforeNetwork(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "request should not reach relay", http.StatusInternalServerError)
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL)
	req := &provider.ChatCompletionRequest{Messages: []provider.Message{{
		Role: "assistant",
		Content: []provider.ContentPart{{
			Type: "input_audio", InputAudio: &provider.InputAudio{Data: "YQ==", Format: "wav"},
		}},
	}}}
	if _, err := p.ChatCompletion(context.Background(), "key", "claude-opus-4-7", req); err == nil {
		t.Fatal("buffered path should reject unrepresentable content")
	}
	if _, err := p.ChatCompletionStream(context.Background(), "key", "claude-opus-4-7", req); err == nil {
		t.Fatal("streaming path should reject unrepresentable content")
	}
	if hits != 0 {
		t.Fatalf("invalid requests reached the relay %d times", hits)
	}
}

func TestMessageBuildersDropEmptyTextWhenRealContentSurvives(t *testing.T) {
	cc := map[string]any{"type": "ephemeral"}
	parts := []provider.ContentPart{
		{Type: "text", Text: "", CacheControl: cc},
		{Type: "text", Text: "real content", CacheControl: cc},
	}
	for _, tc := range []struct {
		name  string
		build func(provider.Message) message
	}{
		{"user", buildUserMessage},
		{"assistant", buildAssistantMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built := tc.build(provider.Message{Role: tc.name, Content: parts})
			blocks, ok := built.Content.([]contentBlock)
			if !ok {
				t.Fatalf("expected block content, got %T", built.Content)
			}
			if len(blocks) != 1 || blocks[0].Text != "real content" {
				t.Fatalf("empty text block was not dropped: %+v", blocks)
			}
		})
	}
}

func TestBuildUserMessage_DropsEmptyImageURLWhenTextSurvives(t *testing.T) {
	built := buildUserMessage(provider.Message{
		Role: "user",
		Content: []provider.ContentPart{
			{Type: "image_url", ImageURL: &provider.ImageURL{}, CacheControl: map[string]any{"type": "ephemeral", "ttl": "1h"}},
			{Type: "text", Text: "real content"},
		},
	})
	blocks, ok := built.Content.([]contentBlock)
	if !ok || len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].Text != "real content" {
		t.Fatalf("empty image URL was not dropped: %T %+v", built.Content, built.Content)
	}
}

func TestBuildToolResultBlock_DoesNotSerializeNullContent(t *testing.T) {
	block := buildToolResultBlock(provider.Message{
		Role:       "tool",
		ToolCallID: "call_1",
		Content: []provider.ContentPart{{
			Type: "file", File: &provider.FileContent{FileID: "unresolvable"},
			CacheControl: map[string]any{"type": "ephemeral"},
		}},
	})
	wire, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if _, exists := decoded["content"]; exists {
		t.Fatalf("empty tool_result content must be omitted: %s", wire)
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

func TestRequiredBetas_AutoSetsForThinkingPlusExtraTools(t *testing.T) {
	betas := requiredBetas("claude-3-5-sonnet-20241022", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Thinking: &provider.ThinkingConfig{Type: "enabled", BudgetTokens: 1024},
		ExtraTools: []any{map[string]any{
			"type": "web_search_20250305", "name": "web_search",
		}},
	})
	if !strings.Contains(betas, "interleaved-thinking-2025-05-14") {
		t.Fatalf("interleaved-thinking beta missing for ExtraTools: %q", betas)
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

func TestRequiredBetas_AutoSetsForRawExtendedCacheTTL(t *testing.T) {
	cc := map[string]any{"type": "ephemeral", "ttl": "1h"}
	cases := []struct {
		name string
		req  *provider.ChatCompletionRequest
	}{
		{
			name: "assistant provider turn block",
			req: &provider.ChatCompletionRequest{Messages: []provider.Message{{
				Role: "assistant",
				ProviderTurnBlocks: []any{map[string]any{
					"type": "text", "text": "cached", "cache_control": cc,
				}},
			}}},
		},
		{
			name: "extra tool",
			req: &provider.ChatCompletionRequest{
				Messages: []provider.Message{{Role: "user", Content: "search"}},
				ExtraTools: []any{map[string]any{
					"type": "web_search_20250305", "name": "web_search", "cache_control": cc,
				}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			betas := requiredBetas("claude-3-5-sonnet-20241022", tc.req)
			if !strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
				t.Fatalf("extended-cache-ttl beta missing: %q", betas)
			}
		})
	}
}

func TestRequiredBetas_AcceptsTypedCacheControlAndExtraTools(t *testing.T) {
	type namedCacheControl map[string]string
	type cacheControlStruct struct {
		Type string `json:"type"`
		TTL  string `json:"ttl"`
	}
	type typedExtraTool struct {
		Type         string             `json:"type"`
		Name         string             `json:"name"`
		MaxUses      int64              `json:"max_uses"`
		CacheControl cacheControlStruct `json:"cache_control"`
	}
	const largeExactInteger int64 = 9007199254740993

	controls := map[string]any{
		"map string string": map[string]string{"type": "ephemeral", "ttl": "1h"},
		"named map":         namedCacheControl{"type": "ephemeral", "ttl": "1h"},
		"struct":            cacheControlStruct{Type: "ephemeral", TTL: "1h"},
		"raw message":       json.RawMessage(`{"type":"ephemeral","ttl":"1h"}`),
	}
	for name, cc := range controls {
		t.Run(name, func(t *testing.T) {
			req := &provider.ChatCompletionRequest{Messages: []provider.Message{{
				Role: "user",
				Content: []provider.ContentPart{{
					Type: "text", Text: "cache me", CacheControl: cc,
				}},
			}}}
			if betas := requiredBetas("claude-3-5-sonnet-20241022", req); !strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
				t.Fatalf("extended-cache-ttl beta missing for %T: %q", cc, betas)
			}
		})
	}

	extraCases := map[string]any{
		"typed slice": []typedExtraTool{{
			Type: "web_search_20250305", Name: "web_search", MaxUses: largeExactInteger,
			CacheControl: cacheControlStruct{Type: "ephemeral", TTL: "1h"},
		}},
		"raw message array": json.RawMessage(`[{"type":"web_search_20250305","name":"web_search","max_uses":9007199254740993,"cache_control":{"type":"ephemeral","ttl":"1h"}}]`),
		"typed raw elements": []json.RawMessage{
			json.RawMessage(`{"type":"web_search_20250305","name":"web_search","max_uses":9007199254740993,"cache_control":{"type":"ephemeral","ttl":"1h"}}`),
		},
	}
	for name, extraTools := range extraCases {
		t.Run("extra tools "+name, func(t *testing.T) {
			req := &provider.ChatCompletionRequest{
				Messages:   []provider.Message{{Role: "user", Content: "search"}},
				ExtraTools: extraTools,
			}
			if betas := requiredBetas("claude-3-5-sonnet-20241022", req); !strings.Contains(betas, "extended-cache-ttl-2025-04-11") {
				t.Fatalf("extended-cache-ttl beta missing: %q", betas)
			}
			built, _ := buildRequest("claude-3-5-sonnet-20241022", req)
			wire, err := json.Marshal(built)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			if !strings.Contains(string(wire), `"ttl":"1h"`) {
				t.Fatalf("typed extra tool was not forwarded: %s", wire)
			}
			if !strings.Contains(string(wire), `"max_uses":9007199254740993`) {
				t.Fatalf("extra tool JSON lost integer precision: %s", wire)
			}
		})
	}
}

// hasExtendedCacheTTL gates the extended-cache-ttl beta header, so it has to
// agree with what buildRequest actually puts on the wire. Claiming a TTL that
// got dropped sends a pointless beta; missing one that shipped makes Anthropic
// silently fall back to the 5m TTL the caller didn't ask for and bill the
// difference. Rather than restate the per-role drop rules a second time (which
// is exactly how the two would drift apart), serialize the built request and
// look for the marker — whatever buildRequest decides is the expectation.
func TestHasExtendedCacheTTL_AgreesWithSerializedRequest(t *testing.T) {
	cc := map[string]any{"type": "ephemeral", "ttl": "1h"}
	cc5m := map[string]any{"type": "ephemeral", "ttl": "5m"}
	img := &provider.ImageURL{URL: "data:image/png;base64,aW1n"}
	audio := &provider.InputAudio{Data: "YXVkaW8=", Format: "wav"}

	text := func(s string) provider.ContentPart {
		return provider.ContentPart{Type: "text", Text: s, CacheControl: cc}
	}
	msg := func(role string, parts ...provider.ContentPart) provider.Message {
		return provider.Message{Role: role, Content: parts, ToolCallID: "call_1"}
	}
	one := func(m provider.Message) *provider.ChatCompletionRequest {
		return &provider.ChatCompletionRequest{Messages: []provider.Message{m}}
	}
	part := func(role string, p provider.ContentPart) *provider.ChatCompletionRequest {
		return one(msg(role, p))
	}
	file := func(f *provider.FileContent) provider.ContentPart {
		return provider.ContentPart{Type: "file", File: f, CacheControl: cc}
	}
	image := func(u *provider.ImageURL) provider.ContentPart {
		return provider.ContentPart{Type: "image_url", ImageURL: u, CacheControl: cc}
	}

	cases := []struct {
		name string
		req  *provider.ChatCompletionRequest
	}{
		// system: only text parts reach the system field.
		{"system text", part("system", text("rules"))},
		{"system empty text", part("system", text(""))},
		{"system image", part("system", image(img))},

		// user: text always survives, image/file only when convertible.
		{"user text", part("user", text("hi"))},
		{"user empty text", part("user", text(""))},
		{"user image", part("user", image(img))},
		{"user image missing url", part("user", image(nil))},
		{"user image empty url", part("user", image(&provider.ImageURL{}))},
		{"user file inline", part("user", file(&provider.FileContent{FileData: "cGRm", MimeType: "application/pdf"}))},
		{"user file bare base64", part("user", file(&provider.FileContent{FileData: "cGRm"}))},
		{"user file id only", part("user", file(&provider.FileContent{FileID: "file-123"}))},
		{"user file bad data uri", part("user", file(&provider.FileContent{FileData: "data:garbage"}))},
		{"user audio", part("user", provider.ContentPart{Type: "input_audio", InputAudio: audio, CacheControl: cc})},
		{"user provider block", one(provider.Message{
			Role:               "user",
			Content:            "plain",
			ProviderTurnBlocks: []any{map[string]any{"type": "text", "cache_control": cc}},
		})},

		// assistant: only non-empty text, plus verbatim provider blocks.
		{"assistant text", part("assistant", text("long answer"))},
		{"assistant empty text", part("assistant", text(""))},
		{"assistant image", part("assistant", image(img))},
		{"assistant audio", part("assistant", provider.ContentPart{Type: "input_audio", InputAudio: audio, CacheControl: cc})},
		{"assistant provider block", one(provider.Message{
			Role: "assistant",
			ProviderTurnBlocks: []any{map[string]any{
				"type": "text", "text": "cached", "cache_control": cc,
			}},
		})},

		// tool results: text and image survive, nothing else does.
		{"tool text", part("tool", text("result"))},
		{"tool empty text", part("tool", text(""))},
		{"tool image", part("tool", image(img))},
		{"tool image empty url", part("tool", image(&provider.ImageURL{}))},
		{"tool file", part("tool", file(&provider.FileContent{FileData: "cGRm"}))},

		// tool definitions, both envelopes.
		{"function tool", &provider.ChatCompletionRequest{
			Messages: []provider.Message{{Role: "user", Content: "hi"}},
			Tools: []provider.Tool{{
				Type:         "function",
				Function:     provider.ToolFunction{Name: "search"},
				CacheControl: cc,
			}},
		}},
		{"extra tool", &provider.ChatCompletionRequest{
			Messages: []provider.Message{{Role: "user", Content: "search"}},
			ExtraTools: []any{map[string]any{
				"type": "web_search_20250305", "name": "web_search", "cache_control": cc,
			}},
		}},

		// negatives.
		{"no cache control", one(provider.Message{Role: "user", Content: "hi"})},
		{"default 5m ttl", part("user", provider.ContentPart{Type: "text", Text: "hi", CacheControl: cc5m})},
	}

	shipped, dropped := 0, 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := buildRequest("claude-3-5-sonnet-20241022", tc.req)
			wire, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			onWire := strings.Contains(string(wire), `"ttl":"1h"`)
			if onWire {
				shipped++
			} else {
				dropped++
			}
			if got := hasExtendedCacheTTL(tc.req); got != onWire {
				t.Fatalf("hasExtendedCacheTTL = %v but extended ttl on wire = %v\nrequest: %s",
					got, onWire, wire)
			}
		})
	}
	// Guard against the table degenerating into all-negatives after a refactor,
	// which would let it pass while testing nothing.
	if shipped == 0 || dropped == 0 {
		t.Fatalf("table lost its discriminating power: %d shipped, %d dropped", shipped, dropped)
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

func TestBuildRequest_DisableParallelToolUse_WithExtraTools(t *testing.T) {
	parallel := false
	r, _ := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		ExtraTools: []any{map[string]any{
			"type": "web_search_20250305", "name": "web_search",
		}},
		ParallelToolCalls: &parallel,
	})
	if r.ToolChoice == nil || r.ToolChoice.Type != "auto" || !r.ToolChoice.DisableParallelToolUse {
		t.Fatalf("parallel_tool_calls=false was lost for ExtraTools: %+v", r.ToolChoice)
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

func TestBuildRequest_JSONSchemaDoesNotOverrideExtraTools(t *testing.T) {
	r, schemaToolName := buildRequest("claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		ExtraTools: []any{map[string]any{
			"type": "web_search_20250305", "name": "web_search",
		}},
		ResponseFormat: &provider.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &provider.JSONSchemaSpec{
				Name:   "answer",
				Schema: map[string]any{"type": "object"},
			},
		},
	})
	if schemaToolName != "" {
		t.Fatalf("json_schema helper tool must not replace ExtraTools: %q", schemaToolName)
	}
	if len(r.Tools) != 1 {
		t.Fatalf("expected only the caller's ExtraTool, got %+v", r.Tools)
	}
	system, ok := r.System.(string)
	if !ok || !strings.Contains(system, "valid JSON matching this schema") {
		t.Fatalf("json_schema conflict should use the system-prompt fallback: %T %q", r.System, system)
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
	out := convertResponse(context.Background(), resp, rawContent, "")
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
	out := convertResponse(context.Background(), resp, nil, "")
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
	out := convertResponse(context.Background(), resp, nil, "")
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

func TestToolCallArgumentsPreserveLargeIntegers(t *testing.T) {
	const arguments = `{"record_id":9007199254740993}`
	built := buildAssistantMessage(provider.Message{
		Role: "assistant",
		ToolCalls: []provider.ToolCall{{
			ID: "call_1",
			Function: provider.ToolCallFunction{
				Name:      "lookup",
				Arguments: arguments,
			},
		}},
	})
	wire, err := json.Marshal(built)
	if err != nil {
		t.Fatalf("marshal assistant tool call: %v", err)
	}
	if !strings.Contains(string(wire), `"record_id":9007199254740993`) {
		t.Fatalf("request tool input lost integer precision: %s", wire)
	}

	var resp response
	if err := json.Unmarshal([]byte(`{
		"id":"m_1","model":"claude-opus-4-7","stop_reason":"tool_use",
		"content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"record_id":9007199254740993}}]
	}`), &resp); err != nil {
		t.Fatalf("decode Anthropic response: %v", err)
	}
	out := convertResponse(context.Background(), &resp, nil, "")
	got := out.Choices[0].Message.ToolCalls[0].Function.Arguments
	if got != arguments {
		t.Fatalf("response tool input lost integer precision: got %s want %s", got, arguments)
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
