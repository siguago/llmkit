package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/siguago/llmkit/internal/httpx"
	"github.com/siguago/llmkit/internal/logging"
	"github.com/siguago/llmkit/internal/safehttp"
	"github.com/siguago/llmkit/provider"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

type Provider struct {
	// baseURL is the API root WITHOUT the trailing /models segment, e.g.
	// "https://generativelanguage.googleapis.com/v1beta". Operation polling
	// (videos) resolves relative operation names against this root, while chat
	// and image calls append "/models".
	baseURL   string
	modelsURL string // baseURL + "/models"

	client       *http.Client // non-streaming requests (with timeout)
	streamClient *http.Client // streaming requests (no global timeout)
}

// New constructs a Gemini provider pointed at the official API.
func New() *Provider { return NewWithBaseURL("") }

// NewWithBaseURL constructs a Gemini provider against a custom API root
// (relay/proxy deployments). Pass an empty string for the official endpoint.
// The root must include the version segment and no trailing slash, e.g.
// "https://generativelanguage.googleapis.com/v1beta".
func NewWithBaseURL(baseURL string) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	outboundTransport := httpx.NewOutbound()
	return &Provider{
		baseURL:   baseURL,
		modelsURL: baseURL + "/models",
		client: &http.Client{
			Timeout:   300 * time.Second,
			Transport: outboundTransport,
		},
		streamClient: &http.Client{
			Timeout:   900 * time.Second, // generous safety-net; WriteTimeout (600s) handles normal cutoff
			Transport: outboundTransport,
		},
	}
}

func (p *Provider) Name() string {
	return "gemini"
}

func (p *Provider) ChatCompletion(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
	geminiReq, err := buildRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	jsonBody, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/%s:generateContent", p.modelsURL, model)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		return nil, provider.NewProviderErrorFromResponse(resp, "gemini", respBody)
	}

	var geminiResp response
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return nil, err
	}

	return convertResponse(&geminiResp, model), nil
}

func (p *Provider) ChatCompletionStream(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	geminiReq, err := buildRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	jsonBody, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/%s:streamGenerateContent?alt=sse", p.modelsURL, model)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", apiKey)

	resp, err := p.streamClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		return nil, provider.NewProviderErrorFromResponse(resp, "gemini", respBody)
	}

	return NewStreamReader(ctx, resp.Body, model), nil
}

// ListModels fetches available models from the Gemini API.
func (p *Provider) ListModels(ctx context.Context, apiKey string) ([]provider.RemoteModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var allModels []provider.RemoteModel
	pageToken := ""

	for {
		url := fmt.Sprintf("%s?pageSize=1000", p.modelsURL)
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}

		httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("x-goog-api-key", apiKey)

		resp, err := p.client.Do(httpReq)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
			resp.Body.Close()
			return nil, provider.NewProviderErrorFromResponse(resp, "gemini", respBody)
		}

		var result struct {
			Models []struct {
				Name                       string   `json:"name"`
				DisplayName                string   `json:"displayName"`
				InputTokenLimit            int      `json:"inputTokenLimit"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		for _, m := range result.Models {
			supportsGenerate := false
			for _, method := range m.SupportedGenerationMethods {
				if method == "generateContent" {
					supportsGenerate = true
					break
				}
			}
			if !supportsGenerate {
				continue
			}

			modelID := m.Name
			if len(modelID) > 7 && modelID[:7] == "models/" {
				modelID = modelID[7:]
			}

			allModels = append(allModels, provider.RemoteModel{
				ModelID:       modelID,
				DisplayName:   m.DisplayName,
				ContextWindow: m.InputTokenLimit,
			})
		}

		if result.NextPageToken == "" {
			break
		}
		pageToken = result.NextPageToken
	}

	return allModels, nil
}

func buildRequest(ctx context.Context, req *provider.ChatCompletionRequest) (*request, error) {
	r := &request{}

	// Build tool_call_id → function_name map for Gemini functionResponse Name field.
	// OpenAI tool messages have tool_call_id but may not have Name; Gemini requires Name.
	toolCallIDToName := make(map[string]string)
	for _, msg := range req.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" && tc.Function.Name != "" {
				toolCallIDToName[tc.ID] = tc.Function.Name
			}
		}
	}

	// Accumulate tool result parts for merging consecutive tool messages
	var pendingFunctionResponses []part
	var systemTexts []string

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			systemTexts = append(systemTexts, provider.ContentToString(msg.Content))
			continue
		}

		role := msg.Role
		if role == "assistant" {
			role = "model"
		}

		if msg.Role == "tool" {
			// Accumulate function responses to merge into a single user content
			var respData any
			contentStr := provider.ContentToString(msg.Content)
			if err := json.Unmarshal([]byte(contentStr), &respData); err != nil {
				respData = map[string]any{"result": contentStr}
			}
			// Gemini requires response to be a Struct (JSON object)
			if _, ok := respData.(map[string]any); !ok {
				respData = map[string]any{"result": respData}
			}
			// Resolve function name: prefer msg.Name, fall back to tool_call_id lookup
			funcName := msg.Name
			if funcName == "" && msg.ToolCallID != "" {
				funcName = toolCallIDToName[msg.ToolCallID]
			}
			pendingFunctionResponses = append(pendingFunctionResponses, part{
				FunctionResponse: &functionResponse{
					Name:     funcName,
					Response: respData,
				},
			})
			continue
		}

		// Flush pending function responses before a non-tool message
		if len(pendingFunctionResponses) > 0 {
			r.Contents = append(r.Contents, content{
				Role:  "user",
				Parts: pendingFunctionResponses,
			})
			pendingFunctionResponses = nil
		}

		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// Assistant message with tool calls → model content with functionCall parts
			parts := buildModelPartsWithToolCalls(msg)
			attachThoughtSignature(parts, msg)
			r.Contents = append(r.Contents, content{
				Role:  "model",
				Parts: parts,
			})
			continue
		}

		// Regular message — handle multimodal content
		parts, err := buildGeminiParts(msg)
		if err != nil {
			return nil, err
		}
		if msg.Role == "assistant" {
			attachThoughtSignature(parts, msg)
		}
		r.Contents = append(r.Contents, content{
			Role:  role,
			Parts: parts,
		})
	}

	// Flush any remaining function responses
	if len(pendingFunctionResponses) > 0 {
		r.Contents = append(r.Contents, content{
			Role:  "user",
			Parts: pendingFunctionResponses,
		})
	}

	// Build system instruction from accumulated system messages
	if len(systemTexts) > 0 {
		combined := systemTexts[0]
		for i := 1; i < len(systemTexts); i++ {
			combined += "\n\n" + systemTexts[i]
		}
		r.SystemInstruction = &content{
			Parts: []part{{Text: combined}},
		}
	}

	// Build generation config
	genCfg := &generationConfig{}
	hasConfig := false

	if req.MaxCompletionTokens != nil {
		genCfg.MaxOutputTokens = req.MaxCompletionTokens
		hasConfig = true
	} else if req.MaxTokens != nil {
		genCfg.MaxOutputTokens = req.MaxTokens
		hasConfig = true
	}
	if req.Temperature != nil {
		genCfg.Temperature = req.Temperature
		hasConfig = true
	}
	if req.TopP != nil {
		genCfg.TopP = req.TopP
		hasConfig = true
	}
	if req.TopK != nil {
		genCfg.TopK = req.TopK
		hasConfig = true
	}
	if req.Stop != nil {
		switch v := req.Stop.(type) {
		case string:
			genCfg.StopSequences = []string{v}
			hasConfig = true
		case []string:
			genCfg.StopSequences = append(genCfg.StopSequences, v...)
			if len(genCfg.StopSequences) > 0 {
				hasConfig = true
			}
		case []any:
			for _, s := range v {
				if str, ok := s.(string); ok {
					genCfg.StopSequences = append(genCfg.StopSequences, str)
				}
			}
			if len(genCfg.StopSequences) > 0 {
				hasConfig = true
			}
		}
	}

	// P0: Structured Output. Gemini exposes two parallel knobs:
	//   - responseSchema      — OpenAPI 3 subset (no $ref/$defs/allOf/oneOf)
	//   - responseJsonSchema  — full JSON Schema spec (Gemini 2.5+)
	// OpenAI's strict json_schema typically uses $ref / $defs / additionalProperties
	// patterns that the OpenAPI subset rejects. Routing json_schema through
	// responseJsonSchema avoids the otherwise-common 400 "field not allowed"
	// the upstream emits when an OpenAI strict schema reaches it unmodified.
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case "json_object":
			genCfg.ResponseMimeType = "application/json"
			hasConfig = true
		case "json_schema":
			genCfg.ResponseMimeType = "application/json"
			if req.ResponseFormat.JSONSchema != nil {
				genCfg.ResponseJSONSchema = req.ResponseFormat.JSONSchema.Schema
			}
			hasConfig = true
		}
	}

	// P1: Thinking — includeThoughts must be set or the API will silently swallow
	// the model's thought summary parts.
	if req.Thinking != nil && req.Thinking.Type == "enabled" && req.Thinking.BudgetTokens > 0 {
		genCfg.ThinkingConfig = &geminiThinkingConfig{
			IncludeThoughts: true,
			ThinkingBudget:  req.Thinking.BudgetTokens,
		}
		hasConfig = true
	}

	// P1: Extra params
	if req.FrequencyPenalty != nil {
		genCfg.FrequencyPenalty = req.FrequencyPenalty
		hasConfig = true
	}
	if req.PresencePenalty != nil {
		genCfg.PresencePenalty = req.PresencePenalty
		hasConfig = true
	}
	if req.Seed != nil {
		genCfg.Seed = req.Seed
		hasConfig = true
	}
	if req.N != nil {
		genCfg.CandidateCount = req.N
		hasConfig = true
	}
	if req.LogProbs != nil {
		genCfg.ResponseLogprobs = req.LogProbs
		hasConfig = true
	}
	if req.TopLogProbs != nil {
		genCfg.Logprobs = req.TopLogProbs
		hasConfig = true
	}

	if hasConfig {
		r.GenerationConfig = genCfg
	}

	// P0: Tools — split into function declarations (regular tools) and
	// built-in tool markers (google_search grounding). Gemini sees them in
	// the same `tools` array but with different shapes: function tools as
	// `functionDeclarations`, search as a `googleSearch: {}` marker entry.
	if len(req.Tools) > 0 {
		var declarations []functionDeclaration
		enableGoogleSearch := false
		enableURLContext := false
		enableCodeExecution := false
		for _, t := range req.Tools {
			switch t.Type {
			case "google_search":
				enableGoogleSearch = true
			case "url_context":
				enableURLContext = true
			case "code_execution":
				enableCodeExecution = true
			case "function", "":
				if t.Function.Name == "" {
					logging.From(ctx).Warn("llmkit: dropping function tool with empty name",
						"tool_index", len(declarations))
					continue
				}
				declarations = append(declarations, functionDeclaration{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					Parameters:  t.Function.Parameters,
				})
			default:
				// Unknown tool types (web_search/file_search/computer_use/custom)
				// are silently dropped — Gemini has no equivalent built-in and
				// dropping is preferable to an opaque upstream 400. Log so
				// operators can spot misconfigured clients in the gateway logs.
				logging.From(ctx).Warn("llmkit: dropping unsupported tool type",
					"provider", "gemini", "type", t.Type)
			}
		}
		if len(declarations) > 0 {
			r.Tools = append(r.Tools, geminiTool{FunctionDeclarations: declarations})
		}
		if enableGoogleSearch {
			r.Tools = append(r.Tools, geminiTool{GoogleSearch: &googleSearchTool{}})
		}
		if enableURLContext {
			r.Tools = append(r.Tools, geminiTool{URLContext: &urlContextTool{}})
		}
		if enableCodeExecution {
			r.Tools = append(r.Tools, geminiTool{CodeExecution: &codeExecutionTool{}})
		}
	}

	// P0: Tool Choice
	if req.ToolChoice != nil {
		r.ToolConfig = convertGeminiToolChoice(req.ToolChoice)
	}

	// P1: Safety settings — Gemini-specific. Forwarded as-is.
	if req.SafetySettings != nil {
		r.SafetySettings = req.SafetySettings
	}

	// P1: Cached content reference. Clients create caches via Gemini's separate
	// caches.* REST endpoints; here we only forward the resource name.
	if req.CachedContent != "" {
		r.CachedContent = req.CachedContent
	}

	// P1: Request labels — Google's tagging hook for billing / monitoring.
	if len(req.Labels) > 0 {
		r.Labels = req.Labels
	}

	return r, nil
}

// buildGeminiParts converts a message's content into Gemini parts, handling
// multimodal content. Returns an error when an image cannot be inlined so the
// caller can surface a 422 to the client; silently dropping the image leaves
// the model answering questions about an image it never saw.
func buildGeminiParts(msg provider.Message) ([]part, error) {
	parts := provider.ContentToParts(msg.Content)
	if len(parts) == 0 {
		return []part{{Text: provider.ContentToString(msg.Content)}}, nil
	}

	var geminiParts []part
	for _, p := range parts {
		switch p.Type {
		case "text":
			geminiParts = append(geminiParts, part{Text: p.Text})
		case "image_url":
			if p.ImageURL == nil {
				continue
			}
			block, err := convertImageURLToInlineData(p.ImageURL.URL)
			if err != nil {
				return nil, err
			}
			geminiParts = append(geminiParts, *block)
		case "input_audio":
			if p.InputAudio == nil || p.InputAudio.Data == "" {
				continue
			}
			// OpenAI's audio formats (wav/mp3/flac/opus) map cleanly to
			// Gemini's audio/<format> mime types — Gemini accepts the same
			// codecs via inlineData.
			format := p.InputAudio.Format
			if format == "" {
				format = "mp3" // OpenAI default
			}
			geminiParts = append(geminiParts, part{
				InlineData: &inlineData{
					MimeType: "audio/" + format,
					Data:     p.InputAudio.Data,
				},
			})
		case "file":
			if p.File == nil {
				continue
			}
			// Inline file data path. file_id (Files API reference) is
			// dropped — Gemini's REST surface uses fileUri pointing at
			// Cloud Storage rather than OpenAI's file_id namespace, and
			// translating between the two requires upload coordination
			// outside this turn's scope.
			if p.File.FileData != "" {
				mime := p.File.MimeType
				if mime == "" {
					mime = "application/octet-stream"
				}
				geminiParts = append(geminiParts, part{
					InlineData: &inlineData{
						MimeType: mime,
						Data:     p.File.FileData,
					},
				})
			}
		}
	}

	if len(geminiParts) == 0 {
		return []part{{Text: provider.ContentToString(msg.Content)}}, nil
	}
	return geminiParts, nil
}

// convertImageURLToInlineData turns either a data: URI or an HTTPS image URL
// into Gemini's inline_data part. Gemini only accepts base64; HTTPS URLs must
// be downloaded server-side. Failures map to 422 so the caller can fail fast.
func convertImageURLToInlineData(rawURL string) (*part, error) {
	if mediaType, data, ok := provider.ParseDataURI(rawURL); ok {
		return &part{
			InlineData: &inlineData{
				MimeType: mediaType,
				Data:     data,
			},
		}, nil
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return nil, &provider.ProviderError{
			StatusCode: http.StatusUnprocessableEntity,
			Message:    "gemini image_url must be either a data: URI or an http(s) URL",
		}
	}
	mediaType, data, err := fetchImageAsBase64(rawURL)
	if err != nil {
		return nil, err
	}
	return &part{
		InlineData: &inlineData{
			MimeType: mediaType,
			Data:     data,
		},
	}, nil
}

// imageFetcher 复用一个 SafeClient——设计 §14.3 SSRF 防护：
//   - 仅 https:// scheme
//   - Dial 阶段拒绝 private / loopback / link-local / multicast / cloud metadata IP
//   - 限制 20MB + 30s + 3 跳重定向
//   - MIME 双重校验（拒绝 svg 等可执行格式）
var imageFetcher = safehttp.NewSafeClient(safehttp.DefaultImageConfig())

// fetchImageAsBase64 downloads an HTTPS image and base64-encodes it for use as
// Gemini inline_data. 走 safehttp.SafeClient 防 SSRF（report-driven fix:
// 旧实现用 http.DefaultClient 无 IP 校验，可被 image_url 探测内网）。
func fetchImageAsBase64(url string) (mediaType, data string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	buf, mime, err := imageFetcher.FetchImage(ctx, url)
	if err != nil {
		// SSRF / 黑名单 IP / scheme / MIME 校验失败一律 422；网络故障保持 502
		if errors.Is(err, safehttp.ErrBlockedIP) ||
			errors.Is(err, safehttp.ErrSchemeNotAllowed) ||
			errors.Is(err, safehttp.ErrUnsupportedMime) ||
			errors.Is(err, safehttp.ErrMimeDeclaredMismatch) ||
			errors.Is(err, safehttp.ErrSizeExceeded) ||
			errors.Is(err, safehttp.ErrTooManyRedirects) {
			return "", "", &provider.ProviderError{
				StatusCode: http.StatusUnprocessableEntity,
				Message:    "gemini: " + err.Error(),
			}
		}
		return "", "", &provider.ProviderError{
			StatusCode: http.StatusBadGateway,
			Message:    "gemini: failed to fetch image_url: " + err.Error(),
		}
	}
	return mime, base64.StdEncoding.EncodeToString(buf), nil
}

// attachThoughtSignature stamps Gemini's encrypted reasoning state onto the
// first part of an assistant turn. Gemini 3 returns 400 during multi-turn
// function calling if the signature isn't echoed back; we land it on the
// leading part because Gemini doesn't document a strict per-part placement
// rule and the leading part is the safest universal slot.
func attachThoughtSignature(parts []part, msg provider.Message) {
	if msg.ThoughtSignature == nil || *msg.ThoughtSignature == "" {
		return
	}
	if len(parts) == 0 {
		return
	}
	parts[0].ThoughtSignature = *msg.ThoughtSignature
}

// buildModelPartsWithToolCalls converts assistant tool calls to Gemini functionCall parts.
func buildModelPartsWithToolCalls(msg provider.Message) []part {
	var parts []part

	// Add text part if present
	text := provider.ContentToString(msg.Content)
	if text != "" {
		parts = append(parts, part{Text: text})
	}

	for _, tc := range msg.ToolCalls {
		var args any
		if tc.Function.Arguments != "" {
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		}
		parts = append(parts, part{
			FunctionCall: &functionCall{
				Name: tc.Function.Name,
				Args: args,
			},
		})
	}

	return parts
}

// convertGeminiToolChoice converts OpenAI tool_choice to Gemini toolConfig.
func convertGeminiToolChoice(choice any) *toolConfig {
	switch v := choice.(type) {
	case string:
		switch v {
		case "auto":
			return &toolConfig{FunctionCallingConfig: &functionCallingConfig{Mode: "AUTO"}}
		case "required":
			return &toolConfig{FunctionCallingConfig: &functionCallingConfig{Mode: "ANY"}}
		case "none":
			return &toolConfig{FunctionCallingConfig: &functionCallingConfig{Mode: "NONE"}}
		}
	case map[string]any:
		// Specific function → use ANY mode with allowedFunctionNames
		cfg := &functionCallingConfig{Mode: "ANY"}
		if fn, ok := v["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" {
				cfg.AllowedFunctionNames = []string{name}
			}
		}
		return &toolConfig{FunctionCallingConfig: cfg}
	}
	return nil
}

func convertResponse(resp *response, model string) *provider.ChatCompletionResponse {
	result := &provider.ChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-gemini-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		// Always initialize choices as an empty slice so JSON serializes as
		// [] (not null) when Gemini blocks the response and returns no
		// candidates. The OpenAI spec mandates choices is always an array;
		// many clients crash on null.
		Choices: []provider.Choice{},
	}

	if len(resp.Candidates) == 0 && resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != "" {
		// Prompt was blocked at intake (no candidates produced). Synthesize a
		// content_filter choice so OpenAI-compat clients see a properly shaped
		// response with the right finish_reason instead of an empty array.
		reason := mapFinishReason(resp.PromptFeedback.BlockReason)
		if reason == "stop" {
			// Unmapped block reason — surface as content_filter for safety signal.
			reason = "content_filter"
		}
		result.Choices = []provider.Choice{{
			Index:        0,
			Message:      &provider.Message{Role: "assistant", Content: ""},
			FinishReason: &reason,
		}}
	}

	// Iterate ALL candidates so n>1 (CandidateCount > 1) actually surfaces as
	// multiple OpenAI-shaped choices. Default n=1 still works — single
	// candidate maps to a single Choice with Index=0.
	for ci, candidate := range resp.Candidates {
		text := ""
		reasoningText := ""
		thoughtSignature := ""
		var toolCalls []provider.ToolCall
		toolCallIndex := 0

		if candidate.Content != nil {
			for _, p := range candidate.Content.Parts {
				// Capture the first non-empty thought signature on this
				// candidate. Gemini 3 strictly requires this echoed back on
				// the next turn (400 otherwise) during multi-turn function
				// calling; the OpenAI message envelope only carries one
				// signature slot, so the leading non-empty one wins.
				if thoughtSignature == "" && p.ThoughtSignature != "" {
					thoughtSignature = p.ThoughtSignature
				}
				if p.Thought != nil && *p.Thought {
					reasoningText += p.Text
					continue
				}
				if p.FunctionCall != nil {
					args := p.FunctionCall.Args
					if args == nil {
						args = map[string]any{}
					}
					argsBytes, _ := json.Marshal(args)
					idx := toolCallIndex
					toolCalls = append(toolCalls, provider.ToolCall{
						ID:   fmt.Sprintf("call_%d_%d_%d", time.Now().UnixNano(), ci, toolCallIndex),
						Type: "function",
						Function: provider.ToolCallFunction{
							Name:      p.FunctionCall.Name,
							Arguments: string(argsBytes),
						},
						Index: &idx,
					})
					toolCallIndex++
				} else if p.Text != "" {
					text += p.Text
				}
			}
		}

		finishReason := mapFinishReason(candidate.FinishReason)
		if len(toolCalls) > 0 && finishReason == "stop" {
			finishReason = "tool_calls"
		}

		msg := &provider.Message{
			Role:    "assistant",
			Content: text,
		}
		if reasoningText != "" {
			msg.ReasoningContent = &reasoningText
		}
		if thoughtSignature != "" {
			sig := thoughtSignature
			msg.ThoughtSignature = &sig
		}
		if len(toolCalls) > 0 {
			msg.ToolCalls = toolCalls
		}
		if ann := buildGroundingAnnotations(candidate.GroundingMetadata); ann != nil {
			msg.Annotations = ann
		}

		// Prefer Gemini's per-candidate index when set; otherwise fall back to
		// loop position (most current models always send index=0 even at n=1).
		choiceIndex := candidate.Index
		if choiceIndex == 0 && ci > 0 {
			choiceIndex = ci
		}
		result.Choices = append(result.Choices, provider.Choice{
			Index:        choiceIndex,
			Message:      msg,
			FinishReason: &finishReason,
		})
	}

	if resp.UsageMetadata != nil {
		result.Usage = buildUsage(resp.UsageMetadata)
	}

	return result
}

// buildUsage normalizes Gemini's usageMetadata to the unified Usage shape.
// Gemini's totalTokenCount already includes thoughtsTokenCount, so we don't add
// it again to total.
func buildUsage(u *usageMetadata) *provider.Usage {
	usage := &provider.Usage{
		PromptTokens:     u.PromptTokenCount,
		CompletionTokens: u.CandidatesTokenCount,
		TotalTokens:      u.TotalTokenCount,
		CachedTokens:     u.CachedContentTokenCount,
		ReasoningTokens:  u.ThoughtsTokenCount,
	}
	if u.CachedContentTokenCount > 0 {
		usage.PromptTokensDetails = &provider.PromptTokensDetails{
			CachedTokens: u.CachedContentTokenCount,
		}
	}
	if u.ThoughtsTokenCount > 0 {
		usage.CompletionTokensDetails = &provider.CompletionTokensDetails{
			ReasoningTokens: u.ThoughtsTokenCount,
		}
	}
	return usage
}

// buildGroundingAnnotations flattens Gemini's groundingMetadata into the
// OpenAI-compatible annotations array shape, where each citation is a
// {type:"url_citation", url_citation:{...}} entry. Returns nil when there's
// nothing to surface so the encoder omits the field via omitempty.
//
// Mapping:
//   - groundingSupports[i].segment.{startIndex,endIndex} → start_index/end_index
//   - groundingSupports[i].groundingChunkIndices[*] → resolves to a chunk
//     whose web.uri/title becomes the url/title of the annotation
//
// One support with multiple chunk indices fans out into multiple annotations,
// matching OpenAI's behavior (one per cited URL).
func buildGroundingAnnotations(gm *groundingMetadata) []map[string]any {
	if gm == nil || len(gm.GroundingSupports) == 0 || len(gm.GroundingChunks) == 0 {
		return nil
	}
	var out []map[string]any
	for _, sup := range gm.GroundingSupports {
		if sup.Segment == nil {
			continue
		}
		for _, idx := range sup.GroundingChunkIndices {
			if idx < 0 || idx >= len(gm.GroundingChunks) {
				continue
			}
			chunk := gm.GroundingChunks[idx]
			if chunk.Web == nil || chunk.Web.URI == "" {
				continue
			}
			out = append(out, map[string]any{
				"type": "url_citation",
				"url_citation": map[string]any{
					"url":         chunk.Web.URI,
					"title":       chunk.Web.Title,
					"start_index": sup.Segment.StartIndex,
					"end_index":   sup.Segment.EndIndex,
				},
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mapFinishReason maps Gemini's finishReason enum to OpenAI's finish_reason.
// Source enum: STOP, MAX_TOKENS, SAFETY, RECITATION, OTHER, BLOCKLIST,
// PROHIBITED_CONTENT, SPII, MALFORMED_FUNCTION_CALL, IMAGE_SAFETY, LANGUAGE,
// UNEXPECTED_TOOL_CALL, TOO_MANY_TOOL_CALLS.
//
// Tool-call detection happens at the call site (when functionCall parts are
// present we override "stop" → "tool_calls"), so this mapper deals only with
// the non-tool finish states.
func mapFinishReason(reason string) string {
	switch reason {
	case "STOP", "OTHER", "":
		return "stop"
	case "MAX_TOKENS", "TOO_MANY_TOOL_CALLS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "LANGUAGE":
		return "content_filter"
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL":
		// The model attempted a tool call that didn't conform to the schema or
		// wasn't allowed — surface as a normal stop so clients can inspect the
		// (possibly partial) message. Distinguishing these from regular stops
		// would require an OpenAI-spec extension we don't ship.
		return "stop"
	default:
		return "stop"
	}
}
