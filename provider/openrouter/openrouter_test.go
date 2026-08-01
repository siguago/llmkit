package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestListModelsWithTaskTypes_UsesOutputModalities(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.URL.Query().Get("output_modalities"); got != "all" {
			t.Errorf("output_modalities = %q, want all", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[
			{"id":"text/model","name":"Text","architecture":{"output_modalities":["text"]}},
			{"id":"image/model","architecture":{"output_modalities":["text","image"]}},
			{"id":"pure-image/model","architecture":{"output_modalities":["image"]}},
			{"id":"video/model","architecture":{"output_modalities":["video"]}},
			{"id":"chat/model","architecture":{"output_modalities":["chat","text"]}},
			{"id":"audio/model","architecture":{"output_modalities":["audio"]}},
			{"id":"embedding/model","architecture":{"output_modalities":["embeddings"]}},
			{"id":"rerank/model","architecture":{"output_modalities":["rerank"]}},
			{"id":"speech/model","architecture":{"output_modalities":["speech"]}},
			{"id":"transcription/model","architecture":{"output_modalities":["transcription"]}},
			{"id":"future/model","architecture":{"output_modalities":["hologram"]}},
			{"id":"empty/model","architecture":{"output_modalities":[]}},
			{"id":"legacy/model"}
		]}`)
	}))
	defer srv.Close()

	models, taskTypes, err := NewWithBaseURL(srv.URL).ListModelsWithTaskTypes(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListModelsWithTaskTypes: %v", err)
	}
	if calls != 1 {
		t.Fatalf("catalog requests = %d, want exactly one", calls)
	}
	if len(models) != 6 {
		t.Fatalf("models = %+v, want five supported/unknown entries plus metadata-less legacy entry", models)
	}
	if got := strings.Join(taskTypes["text/model"], ","); got != provider.RemoteModelTaskChat {
		t.Errorf("text tasks = %q", got)
	}
	wantImage := provider.RemoteModelTaskChat + "," + provider.RemoteModelTaskImageGenerate
	if got := strings.Join(taskTypes["image/model"], ","); got != wantImage {
		t.Errorf("image tasks = %q, want %q", got, wantImage)
	}
	if _, ok := taskTypes["pure-image/model"]; ok {
		t.Error("pure image model must remain unknown until the dedicated /images route is implemented")
	}
	if got := strings.Join(taskTypes["video/model"], ","); got != provider.RemoteModelTaskVideoGenerate {
		t.Errorf("video tasks = %q", got)
	}
	if got := strings.Join(taskTypes["chat/model"], ","); got != provider.RemoteModelTaskChat {
		t.Errorf("chat alias should deduplicate to chat, got %q", got)
	}
	if _, ok := taskTypes["legacy/model"]; ok {
		t.Error("a model with genuinely missing modality metadata must remain unknown")
	}
	gotModels := make(map[string]bool, len(models))
	for _, model := range models {
		gotModels[model.ModelID] = true
	}
	for _, id := range []string{
		"audio/model", "embedding/model", "rerank/model", "speech/model",
		"transcription/model", "future/model", "empty/model",
	} {
		if gotModels[id] {
			t.Errorf("explicitly unsupported catalog entry %s must be filtered", id)
		}
	}
}

func TestListModels_PreservesLegacyRequestAndResults(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"data":[
			{"id":"text/model","architecture":{"output_modalities":["text"]}},
			{"id":"audio/model","architecture":{"output_modalities":["audio"]}},
			{"id":"legacy/model"}
		]}`)
	}))
	defer srv.Close()

	models, err := NewWithBaseURL(srv.URL).ListModels(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if query != "" {
		t.Errorf("legacy ListModels added query %q; want the historical bare /models request", query)
	}
	if len(models) != 3 {
		t.Fatalf("legacy ListModels filtered catalog entries: %+v", models)
	}
}

func TestListModels_EmptyCatalogPreservesNilSlice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()

	models, err := NewWithBaseURL(srv.URL).ListModels(context.Background(), "k")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if models != nil {
		t.Fatalf("legacy empty catalog = %#v, want nil slice", models)
	}
}

func TestNormalizeReasoningOnResponse_LiftsStringField(t *testing.T) {
	// OpenRouter ships reasoning as message.reasoning (string) for OpenAI-shaped
	// clients. The unified Message struct has reasoning_content, not reasoning.
	raw := []byte(`{
		"id": "x",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "answer",
				"reasoning": "let me think..."
			},
			"finish_reason": "stop"
		}]
	}`)
	var resp provider.ChatCompletionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	normalizeReasoningOnResponse(raw, &resp)
	if len(resp.Choices) == 0 || resp.Choices[0].Message == nil {
		t.Fatalf("choices/message missing")
	}
	rc := resp.Choices[0].Message.ReasoningContent
	if rc == nil || *rc != "let me think..." {
		t.Fatalf("reasoning_content not normalized: %+v", rc)
	}
}

func TestNormalizeReasoningOnResponse_DoesNotOverrideExistingReasoningContent(t *testing.T) {
	existing := "preserved"
	resp := &provider.ChatCompletionResponse{
		Choices: []provider.Choice{{
			Index: 0,
			Message: &provider.Message{
				Role:             "assistant",
				Content:          "answer",
				ReasoningContent: &existing,
			},
		}},
	}
	raw := []byte(`{"choices":[{"message":{"reasoning":"new"}}]}`)
	normalizeReasoningOnResponse(raw, resp)
	if *resp.Choices[0].Message.ReasoningContent != "preserved" {
		t.Fatalf("existing reasoning_content must not be clobbered, got %s", *resp.Choices[0].Message.ReasoningContent)
	}
}

func TestNormalizeReasoningOnChunk_LiftsDeltaReasoning(t *testing.T) {
	raw := []byte(`{
		"choices": [{
			"index": 0,
			"delta": {
				"role": "assistant",
				"reasoning": "step 1"
			}
		}]
	}`)
	var chunk provider.ChatCompletionChunk
	if err := json.Unmarshal(raw, &chunk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	normalizeReasoningOnChunk(raw, &chunk)
	if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
		t.Fatalf("choices/delta missing")
	}
	rc := chunk.Choices[0].Delta.ReasoningContent
	if rc == nil || *rc != "step 1" {
		t.Fatalf("delta.reasoning_content not normalized: %+v", rc)
	}
}

func TestBuildRequest_EchoesReasoningContentToReasoning(t *testing.T) {
	rc := "I considered the alternatives"
	body := buildRequest("anthropic/claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "answer", ReasoningContent: &rc},
		},
	})
	msgs, ok := body["messages"].([]map[string]any)
	if !ok || len(msgs) < 2 {
		t.Fatalf("messages not built: %+v", body["messages"])
	}
	if msgs[1]["reasoning"] != rc {
		t.Fatalf("assistant reasoning should be echoed for OpenRouter, got %+v", msgs[1])
	}
}

func TestDetectOpenRouterStreamError_ErrorEnvelope(t *testing.T) {
	pe, ok := detectOpenRouterStreamError([]byte(`{"error":{"message":"upstream timeout","code":504}}`))
	if !ok {
		t.Fatalf("must detect error envelope")
	}
	if pe.StatusCode != 504 {
		t.Fatalf("status from error.code mismatch: %d", pe.StatusCode)
	}
	if !strings.Contains(pe.Message, "openrouter api error (status 504): ") {
		t.Fatalf("message must follow NewProviderError format for unwrap path: %s", pe.Message)
	}
}

func TestDetectOpenRouterStreamError_ExtractsRetryAfter(t *testing.T) {
	pe, ok := detectOpenRouterStreamError([]byte(`{"error":{"message":"slow","code":429,"retry_after":15}}`))
	if !ok {
		t.Fatalf("must detect error envelope")
	}
	if pe.RetryAfter != "15" {
		t.Fatalf("retry_after numeric must be captured, got %q", pe.RetryAfter)
	}
}

func TestBuildReasoningDetails_ContainsRedactedThinking(t *testing.T) {
	rc := "thinking text"
	sig := "sig123"
	msg := provider.Message{
		Role:                      "assistant",
		ReasoningContent:          &rc,
		ReasoningContentSignature: &sig,
		RedactedThinking:          []string{"BLOB1", "BLOB2"},
	}
	details := buildReasoningDetails(msg)
	if len(details) != 3 {
		t.Fatalf("expected 3 entries (1 text + 2 encrypted), got %d: %+v", len(details), details)
	}
	if details[0]["type"] != "reasoning.text" {
		t.Fatalf("first entry must be reasoning.text, got %+v", details[0])
	}
	if details[1]["type"] != "reasoning.encrypted" || details[1]["data"] != "BLOB1" {
		t.Fatalf("second entry must be encrypted with BLOB1: %+v", details[1])
	}
	if details[2]["type"] != "reasoning.encrypted" || details[2]["data"] != "BLOB2" {
		t.Fatalf("third entry must be encrypted with BLOB2: %+v", details[2])
	}
	// Indices must be sequential so OpenRouter reconstructs ordering.
	for i, d := range details {
		if d["index"] != i {
			t.Fatalf("entry %d should have index=%d, got %v", i, i, d["index"])
		}
	}
}

func TestNormalizeReasoningOnResponse_LiftsEncryptedRedactedThinking(t *testing.T) {
	raw := []byte(`{
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "answer",
				"reasoning_details": [
					{"type":"reasoning.encrypted","data":"REDACTED_BLOB","format":"anthropic-claude-v1","index":0}
				]
			}
		}]
	}`)
	var resp provider.ChatCompletionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	normalizeReasoningOnResponse(raw, &resp)
	msg := resp.Choices[0].Message
	if len(msg.RedactedThinking) != 1 || msg.RedactedThinking[0] != "REDACTED_BLOB" {
		t.Fatalf("redacted_thinking not lifted from reasoning.encrypted: %+v", msg.RedactedThinking)
	}
}

func TestDetectOpenRouterStreamError_NormalChunkIgnored(t *testing.T) {
	if _, ok := detectOpenRouterStreamError([]byte(`{"id":"x","choices":[{"delta":{"content":"hi"}}]}`)); ok {
		t.Fatalf("normal chunk must not be flagged as error")
	}
}

func TestBuildRequest_PrefillForwardsBothNames(t *testing.T) {
	// OpenRouter is a routing layer; underlying models use different field
	// names for assistant prefill (DeepSeek "prefix", Kimi "partial").
	// Forward both so the routed-to model picks whichever it recognizes —
	// otherwise users routing through OpenRouter lose multi-turn prefill.
	on := true
	body := buildRequest("anthropic/claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "user", Content: "ack:"},
			{Role: "assistant", Content: "ack:", Prefix: &on},
		},
	})
	msgs, ok := body["messages"].([]map[string]any)
	if !ok || len(msgs) < 2 {
		t.Fatalf("messages malformed: %+v", body["messages"])
	}
	if msgs[1]["prefix"] != true {
		t.Fatalf("prefix (DeepSeek style) must be forwarded: %+v", msgs[1])
	}
	if msgs[1]["partial"] != true {
		t.Fatalf("partial (Kimi style) must be forwarded: %+v", msgs[1])
	}
	// Non-assistant role with Prefix is malformed — must NOT leak.
	user := msgs[0]
	if _, has := user["prefix"]; has {
		t.Fatalf("prefix must not appear on user role: %+v", user)
	}
	if _, has := user["partial"]; has {
		t.Fatalf("partial must not appear on user role: %+v", user)
	}
}

func TestBuildRequest_PrefillNilOmitted(t *testing.T) {
	// Without explicit Prefix, neither field appears (no false positives).
	body := buildRequest("anthropic/claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "assistant", Content: "answer"},
		},
	})
	msgs := body["messages"].([]map[string]any)
	if _, has := msgs[0]["prefix"]; has {
		t.Fatalf("prefix must not be emitted when Prefix is nil")
	}
	if _, has := msgs[0]["partial"]; has {
		t.Fatalf("partial must not be emitted when Prefix is nil")
	}
}

func TestBuildRequest_AttachesReasoningDetailsForSignatureEcho(t *testing.T) {
	// When the assistant turn carries both reasoning_content and
	// reasoning_content_signature, OpenRouter must receive them as
	// reasoning_details[*]{text,signature} so the underlying Anthropic backend
	// can verify the signed thinking block.
	rc := "let me think"
	sig := "EqQB1234"
	body := buildRequest("anthropic/claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages: []provider.Message{
			{Role: "user", Content: "hi"},
			{
				Role:                      "assistant",
				Content:                   "ok",
				ReasoningContent:          &rc,
				ReasoningContentSignature: &sig,
			},
		},
	})
	msgs, ok := body["messages"].([]map[string]any)
	if !ok {
		t.Fatalf("messages slice missing: %+v", body["messages"])
	}
	det, ok := msgs[1]["reasoning_details"].([]map[string]any)
	if !ok || len(det) == 0 {
		t.Fatalf("reasoning_details missing on echo: %+v", msgs[1])
	}
	if det[0]["text"] != "let me think" {
		t.Fatalf("reasoning text lost: %+v", det[0])
	}
	if det[0]["signature"] != "EqQB1234" {
		t.Fatalf("signature lost: %+v", det[0])
	}
}

func TestNormalizeReasoningOnResponse_LiftsSignatureFromDetails(t *testing.T) {
	raw := []byte(`{
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "answer",
				"reasoning": "thinking text",
				"reasoning_details": [
					{"type":"reasoning.text","text":"thinking text","signature":"sig123","format":"anthropic-claude-v1","index":0}
				]
			}
		}]
	}`)
	var resp provider.ChatCompletionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	normalizeReasoningOnResponse(raw, &resp)
	msg := resp.Choices[0].Message
	if msg.ReasoningContentSignature == nil || *msg.ReasoningContentSignature != "sig123" {
		t.Fatalf("signature not lifted from reasoning_details: %+v", msg.ReasoningContentSignature)
	}
}

func TestBuildRequest_ForwardsProviderRouting(t *testing.T) {
	// OpenRouter routes via a `provider` object: order, allow_fallbacks, etc.
	// Power users need to control this; other providers ignore the field.
	body := buildRequest("anthropic/claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		ProviderRouting: map[string]any{
			"order":              []string{"openai", "anthropic"},
			"allow_fallbacks":    true,
			"require_parameters": true,
			"data_collection":    "deny",
		},
	})
	prov, ok := body["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider field must be forwarded, got %#v", body["provider"])
	}
	if prov["allow_fallbacks"] != true {
		t.Fatalf("allow_fallbacks lost: %+v", prov)
	}
	if prov["data_collection"] != "deny" {
		t.Fatalf("data_collection lost: %+v", prov)
	}
}

func TestBuildRequest_ForwardsModalitiesForImageGeneration(t *testing.T) {
	body := buildRequest("openrouter/x", &provider.ChatCompletionRequest{
		Messages:   []provider.Message{{Role: "user", Content: "draw a cat"}},
		Modalities: []string{"image", "text"},
	})
	mods, ok := body["modalities"].([]string)
	if !ok || len(mods) != 2 || mods[0] != "image" || mods[1] != "text" {
		t.Fatalf("modalities not forwarded: %+v", body["modalities"])
	}
}

func TestBuildRequest_ForwardsImageConfig(t *testing.T) {
	cfg := map[string]any{
		"image_size":   "1024x1024",
		"aspect_ratio": "1:1",
	}
	body := buildRequest("openrouter/x", &provider.ChatCompletionRequest{
		Messages:    []provider.Message{{Role: "user", Content: "draw"}},
		ImageConfig: cfg,
	})
	got, ok := body["image_config"].(map[string]any)
	if !ok || got["image_size"] != "1024x1024" || got["aspect_ratio"] != "1:1" {
		t.Fatalf("image_config not forwarded: %+v", body["image_config"])
	}
}

func TestBuildRequest_OmitsModalitiesWhenUnset(t *testing.T) {
	body := buildRequest("openrouter/x", &provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if _, ok := body["modalities"]; ok {
		t.Fatalf("modalities should not be present when client did not request output modalities")
	}
	if _, ok := body["image_config"]; ok {
		t.Fatalf("image_config should not be present when client did not provide one")
	}
}

func TestExtractMediaAssets_FromMessageImagesDataURL(t *testing.T) {
	raw := []byte(`{
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"images": [
					{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}
				]
			}
		}]
	}`)
	assets := extractMediaAssets(raw)
	if len(assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(assets))
	}
	a := assets[0]
	if a.MimeType != "image/png" || a.B64JSON != "aGVsbG8=" {
		t.Fatalf("expected data URL split into mime+b64, got %+v", a)
	}
	if a.DataURL == "" {
		t.Fatalf("expected data_url preserved")
	}
	if a.URL != "" {
		t.Fatalf("data URL must not be treated as remote URL, got URL=%q", a.URL)
	}
}

func TestExtractMediaAssets_FromMessageImagesHTTPSURL(t *testing.T) {
	raw := []byte(`{
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "here it is",
				"images": [
					{"type":"image_url","image_url":{"url":"https://cdn.example.com/x.png"}}
				]
			}
		}]
	}`)
	assets := extractMediaAssets(raw)
	if len(assets) != 1 || assets[0].URL != "https://cdn.example.com/x.png" {
		t.Fatalf("expected https URL preserved: %+v", assets)
	}
}

func TestExtractMediaAssets_FromDeltaImages(t *testing.T) {
	raw := []byte(`{
		"choices": [{
			"index": 0,
			"delta": {
				"images": [
					{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,Zm9v"}}
				]
			}
		}]
	}`)
	assets := extractMediaAssets(raw)
	if len(assets) != 1 || assets[0].MimeType != "image/jpeg" || assets[0].B64JSON != "Zm9v" {
		t.Fatalf("delta.images not normalized: %+v", assets)
	}
}

func TestExtractMediaAssets_EmptyWhenNoImages(t *testing.T) {
	raw := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)
	if got := extractMediaAssets(raw); len(got) != 0 {
		t.Fatalf("expected no assets, got %+v", got)
	}
}

func TestBuildRequest_ReasoningObjectFromEffortAndBudget(t *testing.T) {
	effort := "high"
	body := buildRequest("anthropic/claude-opus-4-7", &provider.ChatCompletionRequest{
		Messages:        []provider.Message{{Role: "user", Content: "hi"}},
		ReasoningEffort: &effort,
		Thinking: &provider.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: 4096,
		},
	})
	r, ok := body["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning object missing: %+v", body["reasoning"])
	}
	if r["effort"] != "high" {
		t.Fatalf("reasoning.effort missing/wrong: %+v", r)
	}
	if r["max_tokens"] != 4096 {
		t.Fatalf("reasoning.max_tokens missing/wrong: %+v", r)
	}
}
