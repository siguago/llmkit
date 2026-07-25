package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestBuildRequest_GoogleSearchToolBecomesBuiltinMarker(t *testing.T) {
	r, err := buildRequest(&provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Tools: []provider.Tool{
			{Type: "google_search"},
			{Type: "function", Function: provider.ToolFunction{
				Name:        "get_weather",
				Description: "fetch weather",
			}},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	// Expect 2 tool entries: one with FunctionDeclarations, one with GoogleSearch.
	if len(r.Tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2; got %+v", len(r.Tools), r.Tools)
	}
	// Order in code: function declarations first, then google search.
	if len(r.Tools[0].FunctionDeclarations) != 1 ||
		r.Tools[0].FunctionDeclarations[0].Name != "get_weather" {
		t.Errorf("function declaration not preserved: %+v", r.Tools[0])
	}
	if r.Tools[1].GoogleSearch == nil {
		t.Errorf("google_search marker missing: %+v", r.Tools[1])
	}
	// Verify the marker serializes as {} (not null) so the upstream sees the
	// canonical "feature on" shape.
	bytes, _ := json.Marshal(r.Tools[1])
	if string(bytes) != `{"googleSearch":{}}` {
		t.Errorf("googleSearch tool marker shape: %s", string(bytes))
	}
}

func TestBuildRequest_UnknownToolTypesSilentlyDropped(t *testing.T) {
	r, err := buildRequest(&provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Tools: []provider.Tool{
			{Type: "web_search"},   // OpenAI built-in — Gemini has no equivalent
			{Type: "file_search"},  // OpenAI built-in
			{Type: "computer_use"}, // OpenAI built-in
		},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(r.Tools) != 0 {
		t.Errorf("unknown tool types must be dropped; got tools=%+v", r.Tools)
	}
}

func TestBuildGroundingAnnotations_FlattensSupportsToUrlCitations(t *testing.T) {
	gm := &groundingMetadata{
		GroundingChunks: []groundingChunk{
			{Web: &groundingChunkWeb{URI: "https://a.example.com", Title: "A"}},
			{Web: &groundingChunkWeb{URI: "https://b.example.com", Title: "B"}},
		},
		GroundingSupports: []groundingSupport{
			{
				Segment:               &groundingSegment{StartIndex: 0, EndIndex: 50, Text: "first claim"},
				GroundingChunkIndices: []int{0, 1}, // cites both chunks
			},
			{
				Segment:               &groundingSegment{StartIndex: 60, EndIndex: 80},
				GroundingChunkIndices: []int{1},
			},
		},
	}
	got := buildGroundingAnnotations(gm)
	if len(got) != 3 {
		t.Fatalf("len(annotations) = %d, want 3; got %+v", len(got), got)
	}
	first, _ := got[0]["url_citation"].(map[string]any)
	if first["url"] != "https://a.example.com" || first["title"] != "A" || first["start_index"] != 0 || first["end_index"] != 50 {
		t.Errorf("first annotation wrong: %+v", first)
	}
}

func TestBuildGroundingAnnotations_EmptyMetadataReturnsNil(t *testing.T) {
	if got := buildGroundingAnnotations(nil); got != nil {
		t.Errorf("nil metadata must return nil, got %+v", got)
	}
	if got := buildGroundingAnnotations(&groundingMetadata{}); got != nil {
		t.Errorf("empty metadata must return nil, got %+v", got)
	}
}

func TestBuildGroundingAnnotations_InvalidIndicesIgnored(t *testing.T) {
	gm := &groundingMetadata{
		GroundingChunks: []groundingChunk{
			{Web: &groundingChunkWeb{URI: "https://a.example.com"}},
		},
		GroundingSupports: []groundingSupport{
			{
				Segment:               &groundingSegment{StartIndex: 0, EndIndex: 10},
				GroundingChunkIndices: []int{0, 99, -1}, // 99 and -1 are out of range
			},
		},
	}
	got := buildGroundingAnnotations(gm)
	if len(got) != 1 {
		t.Errorf("expected 1 valid annotation (others should be skipped), got %d", len(got))
	}
}

func TestBuildRequest_URLContextToolBecomesBuiltinMarker(t *testing.T) {
	r, err := buildRequest(&provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "summarize https://x.com/a"}},
		Tools: []provider.Tool{
			{Type: "url_context"},
			{Type: "google_search"},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(r.Tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2 (urlContext + googleSearch); got %+v", len(r.Tools), r.Tools)
	}
	// Order in code: googleSearch first, urlContext second.
	if r.Tools[0].GoogleSearch == nil {
		t.Errorf("google_search marker missing at index 0: %+v", r.Tools[0])
	}
	if r.Tools[1].URLContext == nil {
		t.Errorf("url_context marker missing at index 1: %+v", r.Tools[1])
	}
	bytes, _ := json.Marshal(r.Tools[1])
	if string(bytes) != `{"urlContext":{}}` {
		t.Errorf("urlContext tool marker shape: %s", string(bytes))
	}
}

func TestBuildRequest_CodeExecutionToolBecomesBuiltinMarker(t *testing.T) {
	r, err := buildRequest(&provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "compute fibonacci 20"}},
		Tools: []provider.Tool{
			{Type: "code_execution"},
			{Type: "google_search"},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(r.Tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2 (googleSearch + codeExecution); got %+v", len(r.Tools), r.Tools)
	}
	// Order in code: googleSearch first (set first in loop iteration order
	// matters), codeExecution last.
	if r.Tools[0].GoogleSearch == nil {
		t.Errorf("google_search marker missing at index 0: %+v", r.Tools[0])
	}
	if r.Tools[1].CodeExecution == nil {
		t.Errorf("code_execution marker missing at index 1: %+v", r.Tools[1])
	}
	bytes, _ := json.Marshal(r.Tools[1])
	if string(bytes) != `{"codeExecution":{}}` {
		t.Errorf("codeExecution tool marker shape: %s", string(bytes))
	}
}

func TestBuildRequest_AllThreeBuiltinToolsCanCoexist(t *testing.T) {
	// Per Gemini docs, code_execution can be combined with google_search;
	// url_context also coexists with the others on Gemini 3.
	r, err := buildRequest(&provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Tools: []provider.Tool{
			{Type: "google_search"},
			{Type: "url_context"},
			{Type: "code_execution"},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(r.Tools) != 3 {
		t.Fatalf("expected 3 builtin-tool entries, got %d", len(r.Tools))
	}
}

func TestBuildRequest_JSONSchemaRoutesThroughResponseJSONSchema(t *testing.T) {
	// OpenAI strict json_schema typically uses $defs/$ref which OpenAPI-subset
	// (responseSchema) rejects. The gateway routes json_schema through
	// responseJsonSchema so the full spec lands on Gemini 2.5+ verbatim.
	strict := true
	r, err := buildRequest(&provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		ResponseFormat: &provider.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &provider.JSONSchemaSpec{
				Name:   "complex",
				Strict: &strict,
				Schema: map[string]any{
					"type": "object",
					"$defs": map[string]any{
						"Address": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"city": map[string]any{"type": "string"},
							},
						},
					},
					"properties": map[string]any{
						"home": map[string]any{"$ref": "#/$defs/Address"},
					},
					"additionalProperties": false,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if r.GenerationConfig == nil {
		t.Fatal("generation_config not set")
	}
	if r.GenerationConfig.ResponseSchema != nil {
		t.Errorf("must not populate responseSchema for json_schema (would lose $defs/$ref); got %+v", r.GenerationConfig.ResponseSchema)
	}
	if r.GenerationConfig.ResponseJSONSchema == nil {
		t.Fatal("responseJsonSchema not populated — Gemini will reject $defs/$ref")
	}
	bytes, _ := json.Marshal(r.GenerationConfig)
	if !strings.Contains(string(bytes), `"responseJsonSchema"`) {
		t.Errorf("wire field name wrong: %s", string(bytes))
	}
	// Critical: complex constructs must survive verbatim — no sanitization.
	if !strings.Contains(string(bytes), `"$ref"`) || !strings.Contains(string(bytes), `"$defs"`) {
		t.Errorf("$ref / $defs lost on the way out: %s", string(bytes))
	}
	if !strings.Contains(string(bytes), `"responseMimeType":"application/json"`) {
		t.Errorf("responseMimeType not set alongside json_schema: %s", string(bytes))
	}
}

func TestBuildRequest_JSONObjectStillUsesResponseMimeType(t *testing.T) {
	// json_object (no schema) still goes through responseMimeType only —
	// don't accidentally populate either schema field.
	r, err := buildRequest(&provider.ChatCompletionRequest{
		Messages:       []provider.Message{{Role: "user", Content: "x"}},
		ResponseFormat: &provider.ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if r.GenerationConfig.ResponseMimeType != "application/json" {
		t.Errorf("responseMimeType missing: %+v", r.GenerationConfig)
	}
	if r.GenerationConfig.ResponseSchema != nil || r.GenerationConfig.ResponseJSONSchema != nil {
		t.Errorf("json_object must not populate either schema field: %+v", r.GenerationConfig)
	}
}

func TestBuildRequest_LabelsForwarded(t *testing.T) {
	r, err := buildRequest(&provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
		Labels:   map[string]string{"team": "platform", "env": "prod"},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(r.Labels) != 2 {
		t.Fatalf("labels lost: got %+v", r.Labels)
	}
	if r.Labels["team"] != "platform" || r.Labels["env"] != "prod" {
		t.Errorf("labels values corrupted: %+v", r.Labels)
	}
	bytes, _ := json.Marshal(r)
	if !strings.Contains(string(bytes), `"labels":{`) {
		t.Errorf("labels not at request top level: %s", string(bytes))
	}
}

func TestBuildRequest_NoLabelsOmitsField(t *testing.T) {
	r, err := buildRequest(&provider.ChatCompletionRequest{
		Messages: []provider.Message{{Role: "user", Content: "x"}},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	bytes, _ := json.Marshal(r)
	if strings.Contains(string(bytes), `"labels"`) {
		t.Errorf("absent labels must not appear in body: %s", string(bytes))
	}
}

func TestBuildRequest_CachedContentForwarded(t *testing.T) {
	r, err := buildRequest(&provider.ChatCompletionRequest{
		Messages:      []provider.Message{{Role: "user", Content: "x"}},
		CachedContent: "cachedContents/abc123",
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if r.CachedContent != "cachedContents/abc123" {
		t.Errorf("cachedContent = %q, want cachedContents/abc123", r.CachedContent)
	}
}
