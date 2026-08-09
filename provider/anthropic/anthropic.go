package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/siguago/llmkit/internal/httpx"
	"github.com/siguago/llmkit/internal/logging"
	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	"github.com/siguago/llmkit/provider"
)

const defaultBaseURL = "https://api.anthropic.com/v1"

type Provider struct {
	baseURL       string
	messagesURL   string
	tokenCountURL string
	modelsURL     string
	client        *http.Client // non-streaming requests (with timeout)
	streamClient  *http.Client // streaming requests (no global timeout)
}

// New constructs an Anthropic provider pointed at the official API.
func New() *Provider { return NewWithBaseURL("") }

// isOfficialEndpoint reports whether this provider talks to Anthropic's own API
// rather than a relay. Two independent behaviours key off it — usage-reporting
// guarantees and TTL inference — so it is named for the fact itself. Deriving
// either one from the other would couple them: relaxing the guarantee for a
// trusted relay would silently start fabricating that relay's TTL breakdown.
func (p *Provider) isOfficialEndpoint() bool {
	return p != nil && p.baseURL == defaultBaseURL
}

// ReportsCacheWriteUsage implements provider.CacheWriteUsageReporter. The
// official endpoint reports cache_creation_input_tokens on both buffered and
// streaming responses. A custom relay may omit that field, so it cannot make
// the same zero-means-no-write guarantee.
func (p *Provider) ReportsCacheWriteUsage() bool {
	return p.isOfficialEndpoint()
}

// NewWithBaseURL constructs an Anthropic provider against a custom API root
// (relay/proxy deployments). Pass an empty string for the official endpoint.
// The root must be the API base without a trailing slash, e.g.
// "https://api.anthropic.com/v1".
func NewWithBaseURL(baseURL string) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	outboundTransport := httpx.NewOutbound()
	return &Provider{
		baseURL:       baseURL,
		messagesURL:   baseURL + "/messages",
		tokenCountURL: baseURL + "/messages/count_tokens",
		modelsURL:     baseURL + "/models",
		client: &http.Client{
			Timeout:       300 * time.Second,
			Transport:     outboundTransport,
			CheckRedirect: checkAnthropicRedirect,
		},
		streamClient: &http.Client{
			Transport:     outboundTransport,
			CheckRedirect: checkAnthropicRedirect,
		},
	}
}

// checkAnthropicRedirect permits the ordinary same-origin redirect behavior,
// but never forwards Anthropic's x-api-key to another origin. Defining a
// callback replaces net/http's default ten-hop limit, so preserve that limit
// explicitly as well.
func checkAnthropicRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if len(via) > 0 && !sameHTTPOrigin(via[0].URL, req.URL) {
		return http.ErrUseLastResponse
	}
	return nil
}

func sameHTTPOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectiveHTTPPort(left) == effectiveHTTPPort(right)
}

func effectiveHTTPPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	switch strings.ToLower(value.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func (p *Provider) Name() string {
	return "anthropic"
}

func (p *Provider) ChatCompletion(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (*provider.ChatCompletionResponse, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	anthropicReq, jsonSchemaToolName := buildRequest(model, req)
	anthropicReq.Stream = false

	jsonBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, err
	}

	var requestOptions []anthropicapi.RequestOption
	if betas := requiredBetas(model, req); betas != "" {
		requestOptions = append(requestOptions, anthropicapi.WithBetas(betas))
	}
	resp, err := p.doLegacyChatRequest(ctx, apiKey, p.messagesURL, jsonBody, false, requestOptions...)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var anthropicResp response
	if err := json.Unmarshal(respBody, &anthropicResp); err != nil {
		return nil, err
	}
	// Capture the raw content blocks alongside the typed decode so we can
	// preserve provider-native block types (server_tool_use,
	// web_search_tool_result, code_execution_tool_result) verbatim for
	// multi-turn round-trip. The `response` struct only models a subset of
	// the block fields; the typed view drives content/tool_use logic, the
	// raw view drives ProviderTurnBlocks.
	var rawWithContent struct {
		Content []map[string]any `json:"content"`
	}
	_ = json.Unmarshal(respBody, &rawWithContent)

	return convertResponse(ctx, &anthropicResp, rawWithContent.Content, jsonSchemaToolName), nil
}

func (p *Provider) ChatCompletionStream(ctx context.Context, apiKey, model string, req *provider.ChatCompletionRequest) (provider.StreamReader, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	anthropicReq, jsonSchemaToolName := buildRequest(model, req)
	anthropicReq.Stream = true

	jsonBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, err
	}

	var requestOptions []anthropicapi.RequestOption
	if betas := requiredBetas(model, req); betas != "" {
		requestOptions = append(requestOptions, anthropicapi.WithBetas(betas))
	}
	resp, err := p.doLegacyChatRequest(ctx, apiKey, p.messagesURL, jsonBody, true, requestOptions...)
	if err != nil {
		return nil, err
	}

	// Not ReportsCacheWriteUsage(): that answers whether the aggregate is
	// trustworthy, while this answers whether an unlabelled cache write may be
	// attributed to the 5m tier. Both happen to key off the official endpoint.
	return newStreamReader(ctx, resp.Body, jsonSchemaToolName, p.isOfficialEndpoint()), nil
}

// ListModels fetches available models from the Anthropic API.
func (p *Provider) ListModels(ctx context.Context, apiKey string) ([]provider.RemoteModel, error) {
	models, _, err := p.listModelsWithTaskTypes(ctx, apiKey)
	return models, err
}

// ListModelsWithTaskTypes fetches the same catalog as ListModels and classifies
// the claude- family as chat. Anything else remains listed but deliberately has
// no task-map entry.
func (p *Provider) ListModelsWithTaskTypes(ctx context.Context, apiKey string) ([]provider.RemoteModel, map[string][]string, error) {
	return p.listModelsWithTaskTypes(ctx, apiKey)
}

func (p *Provider) listModelsWithTaskTypes(ctx context.Context, apiKey string) ([]provider.RemoteModel, map[string][]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var allModels []provider.RemoteModel
	taskTypes := make(map[string][]string)
	afterID := ""

	for {
		url := p.modelsURL + "?limit=1000"
		if afterID != "" {
			url += "&after_id=" + afterID
		}

		httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, nil, err
		}
		provider.SetKeyHeader(httpReq.Header, "x-api-key", apiKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")

		resp, err := p.client.Do(httpReq)
		if err != nil {
			return nil, nil, err
		}

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
			resp.Body.Close()
			return nil, nil, provider.NewProviderErrorFromResponse(resp, "anthropic", respBody)
		}

		var result struct {
			Data []struct {
				ID           string `json:"id"`
				DisplayName  string `json:"display_name"`
				DeprecatedAt string `json:"deprecated_at"`
			} `json:"data"`
			HasMore bool `json:"has_more"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, nil, err
		}
		resp.Body.Close()

		now := time.Now().UTC()
		for _, m := range result.Data {
			// Anthropic still lists models past their deprecation date so old
			// integrations don't suddenly disappear. The gateway's import flow
			// is for new wiring, so we filter out anything already deprecated
			// (deprecated_at <= now) to avoid surfacing dead models.
			if m.DeprecatedAt != "" {
				if t, err := time.Parse(time.RFC3339, m.DeprecatedAt); err == nil && !t.After(now) {
					continue
				}
			}
			allModels = append(allModels, provider.RemoteModel{
				ModelID:     m.ID,
				DisplayName: m.DisplayName,
			})
			if tasks := anthropicModelTaskTypes(m.ID); len(tasks) > 0 {
				taskTypes[m.ID] = tasks
			}
		}

		if !result.HasMore || len(result.Data) == 0 {
			break
		}
		afterID = result.Data[len(result.Data)-1].ID
	}

	return allModels, taskTypes, nil
}

// anthropicModelTaskTypes is a prefix test rather than a per-ID allowlist,
// because /v1/models has only ever returned chat models and Anthropic ships new
// Claude IDs faster than an allowlist could track. This deliberately differs
// from the DeepSeek adapter, whose catalog mixes retired aliases and non-chat
// models and therefore cannot be classified by prefix. Should Anthropic ever
// list a non-chat claude- model, this must become an allowlist.
func anthropicModelTaskTypes(modelID string) []string {
	if strings.HasPrefix(modelID, "claude-") {
		return []string{provider.RemoteModelTaskChat}
	}
	return nil
}

var _ provider.ModelTaskLister = (*Provider)(nil)

// validateRequest enforces Anthropic-specific request shape constraints that
// the upstream wouldn't catch before consuming a billable connection. We map
// rejections to 422 so clients see a clear "you can't ask for this" error
// rather than a generic upstream 400.
func validateRequest(req *provider.ChatCompletionRequest) error {
	// Anthropic Messages API returns exactly one assistant message per call;
	// n>1 is not supported. Failing here avoids the silent "asked for 3 got 1"
	// mismatch where we'd otherwise drop the n field.
	if req.N != nil && *req.N != 1 {
		return &provider.ProviderError{
			StatusCode: http.StatusUnprocessableEntity,
			Message:    "anthropic does not support n>1; only a single completion per request is allowed",
		}
	}
	// max_tokens is required by Anthropic. The gateway defaults to 4096 when
	// the client omits both max_tokens and max_completion_tokens, so this
	// branch only triggers if the client explicitly sets a non-positive value.
	if req.MaxTokens != nil && *req.MaxTokens <= 0 {
		return &provider.ProviderError{
			StatusCode: http.StatusUnprocessableEntity,
			Message:    "anthropic requires max_tokens > 0",
		}
	}
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens <= 0 {
		return &provider.ProviderError{
			StatusCode: http.StatusUnprocessableEntity,
			Message:    "anthropic requires max_completion_tokens > 0",
		}
	}
	hasConversationMessage := false
	for i, msg := range req.Messages {
		var representable bool
		switch msg.Role {
		case "system":
			continue
		case "user":
			hasConversationMessage = true
			representable = userMessageHasRepresentableContent(msg)
		case "assistant":
			hasConversationMessage = true
			if !assistantToolCallsAreRepresentable(msg.ToolCalls) {
				return &provider.ProviderError{
					StatusCode: http.StatusUnprocessableEntity,
					Message:    "anthropic assistant message at index " + strconv.Itoa(i) + " has a malformed tool call",
				}
			}
			representable = assistantMessageHasRepresentableContent(msg)
		case "tool":
			hasConversationMessage = true
			if msg.ToolCallID == "" {
				return &provider.ProviderError{
					StatusCode: http.StatusUnprocessableEntity,
					Message:    "anthropic tool message at index " + strconv.Itoa(i) + " requires tool_call_id",
				}
			}
			continue
		default:
			return &provider.ProviderError{
				StatusCode: http.StatusUnprocessableEntity,
				Message:    "anthropic message at index " + strconv.Itoa(i) + " has unsupported role " + strconv.Quote(msg.Role),
			}
		}
		if !representable {
			return &provider.ProviderError{
				StatusCode: http.StatusUnprocessableEntity,
				Message:    "anthropic message at index " + strconv.Itoa(i) + " with role " + msg.Role + " has no representable content",
			}
		}
	}
	if !hasConversationMessage {
		return &provider.ProviderError{
			StatusCode: http.StatusUnprocessableEntity,
			Message:    "anthropic requires at least one user, assistant, or tool message",
		}
	}
	return nil
}

func assistantToolCallsAreRepresentable(calls []provider.ToolCall) bool {
	for _, call := range calls {
		if call.ID == "" || call.Function.Name == "" || call.Function.Arguments == "" {
			return false
		}
		var input map[string]json.RawMessage
		if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil || input == nil {
			return false
		}
	}
	return true
}

// userMessageHasRepresentableContent mirrors buildUserMessage's conversion
// rules. Unsupported media may be mixed with useful content and dropped, but
// a whole turn that would collapse to an empty string/array is rejected before
// opening a billable upstream connection.
func userMessageHasRepresentableContent(msg provider.Message) bool {
	for _, p := range provider.ContentToParts(msg.Content) {
		switch p.Type {
		case "text":
			if p.Text != "" {
				return true
			}
		case "image_url":
			if usableImageURL(p.ImageURL) {
				return true
			}
		case "file":
			if convertFileToAnthropicDocument(p.File) != nil {
				return true
			}
		}
	}
	return false
}

// assistantMessageHasRepresentableContent mirrors buildAssistantMessage. An
// assistant turn may legitimately consist only of thinking/provider/tool-use
// blocks, so text is not the sole validity signal here.
func assistantMessageHasRepresentableContent(msg provider.Message) bool {
	if hasSignedThinking(msg) {
		return true
	}
	for _, data := range msg.RedactedThinking {
		if data != "" {
			return true
		}
	}
	if len(msg.ToolCalls) > 0 {
		return true
	}
	for _, raw := range msg.ProviderTurnBlocks {
		if _, ok := forwardableProviderBlock(raw); ok {
			return true
		}
	}
	for _, p := range provider.ContentToParts(msg.Content) {
		if p.Type == "text" && p.Text != "" {
			return true
		}
	}
	return false
}

func hasSignedThinking(msg provider.Message) bool {
	return msg.ReasoningContent != nil && *msg.ReasoningContent != "" &&
		msg.ReasoningContentSignature != nil && *msg.ReasoningContentSignature != ""
}

func usableImageURL(image *provider.ImageURL) bool {
	if image == nil || image.URL == "" {
		return false
	}
	if !strings.HasPrefix(image.URL, "data:") {
		return true
	}
	mediaType, data, ok := provider.ParseDataURI(image.URL)
	return ok && mediaType != "" && data != ""
}

// forwardableProviderBlock mirrors buildAssistantMessage's raw-block path.
// An empty object (or one without a type) is not a valid Anthropic content
// block and must not make an otherwise empty assistant turn look usable.
func forwardableProviderBlock(raw any) (map[string]any, bool) {
	block, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	blockType, _ := block["type"].(string)
	return block, blockType != ""
}

// requiredBetas inspects the unified request and returns the
// `anthropic-beta` header value needed to unlock features the request asks
// for. Currently:
//
//  1. interleaved-thinking-2025-05-14 — extended thinking + tool use. Without
//     this header the model thinks once at the start; with it, the model can
//     think between tool calls (the typical user expectation when both are
//     requested).
//  2. extended-cache-ttl-2025-04-11 — enables ttl != "5m" on cache_control
//     blocks (e.g. {"type":"ephemeral","ttl":"1h"}).
//  3. context-1m-2025-08-07 — unlocks the 1M-token context window on Claude 4+
//     models. Anthropic still requires this beta even on natively-supporting
//     models (Opus 4.6+, Sonnet 4.6); without it the request caps at 200k.
//     Sending it on a request whose prompt is < 200k is a no-op.
//
// Returns a comma-separated header value, or "" when no betas are needed.
func requiredBetas(model string, req *provider.ChatCompletionRequest) string {
	var betas []string

	thinkingEnabled := req.Thinking != nil && req.Thinking.Type == "enabled" && req.Thinking.BudgetTokens > 0
	if thinkingEnabled && requestUsesTools(req) {
		betas = append(betas, "interleaved-thinking-2025-05-14")
	}

	if hasExtendedCacheTTL(req) {
		betas = append(betas, "extended-cache-ttl-2025-04-11")
	}

	if is1MContextModel(model) {
		betas = append(betas, "context-1m-2025-08-07")
	}

	if len(betas) == 0 {
		return ""
	}
	return strings.Join(betas, ",")
}

func requestUsesTools(req *provider.ChatCompletionRequest) bool {
	if len(req.Tools) > 0 || len(normalizeExtraTools(req.ExtraTools)) > 0 {
		return true
	}
	return req.ResponseFormat != nil && req.ResponseFormat.Type == "json_schema" && req.ResponseFormat.JSONSchema != nil
}

// is1MContextModel reports whether the given Anthropic model ID supports the
// 1M-token context window via the context-1m-2025-08-07 beta header.
//
// As of 2026-05 the supported models are the Claude 4 family
// (Opus 4 / 4.6 / 4.7, Sonnet 4 / 4.5 / 4.6). Anthropic announced retiring
// the beta on Sonnet 4.5 and Sonnet 4 on 2026-04-30; we still pass the header
// for forward compat — the API returns 400 when the prompt actually exceeds
// 200k on a retired model, which is the same UX as not sending the header.
func is1MContextModel(model string) bool {
	m := strings.ToLower(model)
	// Strip any vendor prefix (e.g. "anthropic/claude-opus-4-7" via OpenRouter
	// path hitting the native provider directly is unusual but harmless).
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "claude-opus-4") ||
		strings.HasPrefix(m, "claude-sonnet-4")
}

// hasExtendedCacheTTL returns true if any cache_control object in the request
// requests a ttl other than the default 5 minutes.
func hasExtendedCacheTTL(req *provider.ChatCompletionRequest) bool {
	check := func(cc any) bool {
		if cc == nil {
			return false
		}
		encoded, err := json.Marshal(cc)
		if err != nil {
			return false
		}
		var decoded struct {
			TTL string `json:"ttl"`
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return false
		}
		return decoded.TTL != "" && decoded.TTL != "5m"
	}
	checkRawEnvelope := func(raw any) bool {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return false
		}
		var decoded struct {
			CacheControl json.RawMessage `json:"cache_control"`
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil || len(decoded.CacheControl) == 0 {
			return false
		}
		return check(decoded.CacheControl)
	}
	for _, msg := range req.Messages {
		parts := provider.ContentToParts(msg.Content)
		switch msg.Role {
		case "system":
			for _, p := range parts {
				if p.Type == "text" && p.Text != "" && check(p.CacheControl) {
					return true
				}
			}
		case "tool":
			for _, p := range parts {
				switch p.Type {
				case "text":
					if p.Text != "" && check(p.CacheControl) {
						return true
					}
				case "image_url":
					if usableImageURL(p.ImageURL) && check(p.CacheControl) {
						return true
					}
				}
			}
		case "assistant":
			for _, p := range parts {
				if p.Type == "text" && p.Text != "" && check(p.CacheControl) {
					return true
				}
			}
			for _, raw := range msg.ProviderTurnBlocks {
				// buildAssistantMessage currently forwards map-shaped provider
				// blocks verbatim; inspect the cache_control using JSON so its
				// value may still be a named map, struct, or RawMessage.
				if _, ok := forwardableProviderBlock(raw); ok && checkRawEnvelope(raw) {
					return true
				}
			}
		default:
			for _, p := range parts {
				switch p.Type {
				case "text":
					if p.Text != "" && check(p.CacheControl) {
						return true
					}
				case "image_url":
					if usableImageURL(p.ImageURL) && check(p.CacheControl) {
						return true
					}
				case "file":
					// Defer to the converter rather than re-deriving when a
					// file part survives: it owns the drop rules, and a copy
					// here would silently rot into false positives (a beta
					// header sent for a cache_control that never shipped).
					if convertFileToAnthropicDocument(p.File) != nil && check(p.CacheControl) {
						return true
					}
				}
			}
		}
	}
	for _, t := range req.Tools {
		if check(t.CacheControl) {
			return true
		}
	}
	for _, raw := range normalizeExtraTools(req.ExtraTools) {
		if checkRawEnvelope(raw) {
			return true
		}
	}
	return false
}

// normalizeExtraTools accepts both the ordinary JSON-decoded []any form and
// programmatic typed slices / json.RawMessage arrays. The latter serialize to
// the same upstream tools array and therefore must participate in beta-header
// detection too.
func normalizeExtraTools(raw any) []any {
	if raw == nil {
		return nil
	}
	if tools, ok := raw.([]any); ok {
		return tools
	}
	decodeRawArray := func(encoded []byte) []any {
		var elements []json.RawMessage
		if err := json.Unmarshal(encoded, &elements); err != nil {
			return nil
		}
		tools := make([]any, len(elements))
		for i := range elements {
			tools[i] = elements[i]
		}
		return tools
	}
	if encoded, ok := raw.(json.RawMessage); ok {
		return decodeRawArray(encoded)
	}

	value := reflect.ValueOf(raw)
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return nil
	}
	// Treat named byte slices as raw JSON too. ExtraTools is an array of
	// objects, so a byte slice cannot be a meaningful typed tools collection.
	if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
		return decodeRawArray(value.Bytes())
	}
	if value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
		return nil
	}

	tools := make([]any, value.Len())
	for i := 0; i < value.Len(); i++ {
		tools[i] = value.Index(i).Interface()
	}
	return tools
}

// buildRequest returns the Anthropic request and the json_schema tool name (empty if not used).
func buildRequest(model string, req *provider.ChatCompletionRequest) (*request, string) {
	extraTools := normalizeExtraTools(req.ExtraTools)
	hasUserTools := len(req.Tools) > 0 || len(extraTools) > 0
	r := &request{
		Model:     model,
		MaxTokens: 4096,
	}

	if req.MaxCompletionTokens != nil {
		r.MaxTokens = *req.MaxCompletionTokens
	} else if req.MaxTokens != nil {
		r.MaxTokens = *req.MaxTokens
	}
	if req.Temperature != nil {
		r.Temperature = req.Temperature
	}
	if req.TopP != nil {
		r.TopP = req.TopP
	}
	if req.TopK != nil {
		r.TopK = req.TopK
	}
	if req.ServiceTier != nil {
		r.ServiceTier = *req.ServiceTier
	}
	// Map OpenAI's user / safety_identifier to Anthropic's metadata.user_id
	// for abuse tracking. SafetyIdentifier is OpenAI's newer-SDK successor;
	// prefer it when both are set, fall back to User.
	if id := req.SafetyIdentifier; id != "" {
		r.Metadata = map[string]any{"user_id": id}
	} else if req.User != "" {
		r.Metadata = map[string]any{"user_id": req.User}
	}

	// Carry through the prior turn's code_execution container reference so
	// Anthropic resumes the same Python sandbox (files / installed packages
	// preserved). The client receives this descriptor in the previous
	// response's `container` field and must echo it back here.
	if req.Container != nil {
		r.Container = req.Container
	}

	// P1: Extended Thinking
	if req.Thinking != nil && req.Thinking.Type == "enabled" && req.Thinking.BudgetTokens > 0 {
		r.Thinking = &thinkingConfig{
			Type:         "enabled",
			BudgetTokens: req.Thinking.BudgetTokens,
			Display:      req.Thinking.Display, // "summarized" / "omitted"; empty → upstream default
		}
		// Thinking requires temperature = 1 and is incompatible with top_k
		r.Temperature = nil
		r.TopK = nil
		// max_tokens must be > budget_tokens
		if r.MaxTokens <= req.Thinking.BudgetTokens {
			r.MaxTokens = req.Thinking.BudgetTokens + 4096
		}
	}

	// P0: Tools — function tools follow OpenAI's nested `{type:"function",function:{...}}`
	// envelope; we unwrap them into Anthropic's flat `{name,description,input_schema}` shape.
	if len(req.Tools) > 0 {
		for _, t := range req.Tools {
			r.Tools = append(r.Tools, anthropicTool{
				Name:         t.Function.Name,
				Description:  t.Function.Description,
				InputSchema:  t.Function.Parameters,
				CacheControl: t.CacheControl,
			})
		}
	}

	// Anthropic server-side built-in tools (web_search, code_execution,
	// computer use, bash, text_editor) don't fit OpenAI's function envelope;
	// callers ship them through ExtraTools as raw objects. In addition to the
	// ordinary JSON-decoded []any shape, accept programmatic typed slices and
	// RawMessage arrays; normalizeExtraTools preserves their JSON meaning.
	r.Tools = append(r.Tools, extraTools...)

	// P0: Tool Choice — also fold parallel_tool_calls=false into tool_choice.
	// Anthropic carries this opt-out under tool_choice.disable_parallel_tool_use
	// rather than as a top-level field.
	if req.ToolChoice != nil {
		r.ToolChoice = convertToolChoice(req.ToolChoice)
	} else if hasUserTools && req.ParallelToolCalls != nil && !*req.ParallelToolCalls {
		// Synthesize an auto choice so we have somewhere to attach the flag.
		r.ToolChoice = &toolChoiceConfig{Type: "auto"}
	}
	if r.ToolChoice != nil && r.ToolChoice.Type != "none" &&
		req.ParallelToolCalls != nil && !*req.ParallelToolCalls {
		r.ToolChoice.DisableParallelToolUse = true
	}

	// P0: Structured Output (JSON mode simulation)
	systemTextSuffix := ""
	jsonSchemaToolName := ""
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case "json_object":
			systemTextSuffix = "\n\nYou must respond with valid JSON only. Do not include any text outside of the JSON object."
		case "json_schema":
			// Only use tool-as-structured-output when user has no tools (avoid conflict)
			if req.ResponseFormat.JSONSchema != nil && !hasUserTools {
				jsonSchemaToolName = req.ResponseFormat.JSONSchema.Name
				schemaBytes, _ := json.Marshal(req.ResponseFormat.JSONSchema.Schema)
				r.Tools = append(r.Tools, anthropicTool{
					Name:        jsonSchemaToolName,
					Description: req.ResponseFormat.JSONSchema.Description,
					InputSchema: json.RawMessage(schemaBytes),
				})
				// Preserve the client's parallel_tool_calls=false intent
				// across the json_schema rewrite. Type:"tool" with a fixed
				// Name already implicitly selects a single tool, so this is
				// mostly semantic preservation; we honor it so a future
				// upstream change won't quietly drop the client's signal.
				disable := req.ParallelToolCalls != nil && !*req.ParallelToolCalls
				if r.ToolChoice != nil && r.ToolChoice.DisableParallelToolUse {
					disable = true
				}
				r.ToolChoice = &toolChoiceConfig{
					Type:                   "tool",
					Name:                   jsonSchemaToolName,
					DisableParallelToolUse: disable,
				}
			} else if req.ResponseFormat.JSONSchema != nil {
				// Fallback: when user has tools, use system prompt approach
				schemaBytes, _ := json.Marshal(req.ResponseFormat.JSONSchema.Schema)
				systemTextSuffix = "\n\nYou must respond with valid JSON matching this schema:\n" + string(schemaBytes)
			}
		}
	}

	// Build messages: extract system, convert tool messages, handle vision.
	// systemBlocks accumulates structured system content so that prompt caching
	// breakpoints (cache_control) survive into the Anthropic request. When no
	// part of the system content has a cache hint we collapse to a string at
	// the end (back-compat with existing wire bytes).
	var systemBlocks []contentBlock
	var pendingToolResults []contentBlock

	appendSystemText := func(text string, cc any) {
		// Anthropic rejects empty text blocks outright, and a cache breakpoint
		// on empty text has nothing to mark, so an empty part is dropped even
		// when it carries cache_control.
		if text == "" {
			return
		}
		systemBlocks = append(systemBlocks, contentBlock{
			Type:         "text",
			Text:         text,
			CacheControl: cc,
		})
	}

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			parts := provider.ContentToParts(msg.Content)
			if len(parts) == 0 {
				appendSystemText(provider.ContentToString(msg.Content), nil)
			} else {
				for _, p := range parts {
					if p.Type == "text" {
						appendSystemText(p.Text, p.CacheControl)
					}
				}
			}
			continue
		}

		if msg.Role == "tool" {
			// Accumulate tool results — they need to be in a user message.
			// Anthropic's tool_result content accepts a string OR an array of
			// content blocks (text/image). OpenAI tool messages can carry
			// images too (function returns a screenshot etc.); preserve them.
			pendingToolResults = append(pendingToolResults, buildToolResultBlock(msg))
			continue
		}

		// Flush pending tool results before a non-tool message
		if len(pendingToolResults) > 0 {
			if msg.Role == "user" {
				// BUG 8 fix: merge tool results with user content to avoid consecutive user messages
				userMsg := buildUserMessage(msg)
				var blocks []contentBlock
				blocks = append(blocks, pendingToolResults...)
				switch c := userMsg.Content.(type) {
				case string:
					if c != "" {
						blocks = append(blocks, contentBlock{Type: "text", Text: c})
					}
				case []contentBlock:
					blocks = append(blocks, c...)
				}
				r.Messages = append(r.Messages, message{Role: "user", Content: blocks})
				pendingToolResults = nil
				continue
			}
			r.Messages = append(r.Messages, message{
				Role:    "user",
				Content: pendingToolResults,
			})
			pendingToolResults = nil
		}

		if msg.Role == "assistant" {
			r.Messages = append(r.Messages, buildAssistantMessage(msg))
			continue
		}

		// User message — handle multimodal content
		r.Messages = append(r.Messages, buildUserMessage(msg))
	}

	// Flush any remaining tool results
	if len(pendingToolResults) > 0 {
		r.Messages = append(r.Messages, message{
			Role:    "user",
			Content: pendingToolResults,
		})
	}

	// Build system field. If any block carries a cache hint we must keep the
	// array shape so Anthropic interprets the breakpoint; otherwise collapse to
	// a single string (more compact, matches the legacy wire bytes).
	if systemTextSuffix != "" {
		appendSystemText(systemTextSuffix, nil)
	}
	hasCacheHint := false
	for _, b := range systemBlocks {
		if b.CacheControl != nil {
			hasCacheHint = true
			break
		}
	}
	if len(systemBlocks) > 0 {
		if hasCacheHint {
			r.System = systemBlocks
		} else {
			var sb strings.Builder
			for i, b := range systemBlocks {
				if i > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(b.Text)
			}
			r.System = sb.String()
		}
	}

	// Handle stop sequences. The unified Request.Stop is `any` to mirror
	// OpenAI's polymorphic shape (string OR array of strings). The JSON path
	// hands us either string or []any; programmatic callers may construct
	// []string directly. Cover all three to avoid silently dropping the
	// limit and letting the model run past intended stop points.
	if req.Stop != nil {
		switch v := req.Stop.(type) {
		case string:
			r.StopSequences = []string{v}
		case []string:
			r.StopSequences = append(r.StopSequences, v...)
		case []any:
			for _, s := range v {
				if str, ok := s.(string); ok {
					r.StopSequences = append(r.StopSequences, str)
				}
			}
		}
	}

	return r, jsonSchemaToolName
}

// convertToolChoice converts OpenAI tool_choice format to Anthropic format.
func convertToolChoice(choice any) *toolChoiceConfig {
	switch v := choice.(type) {
	case string:
		switch v {
		case "auto":
			return &toolChoiceConfig{Type: "auto"}
		case "required":
			return &toolChoiceConfig{Type: "any"}
		case "none":
			return &toolChoiceConfig{Type: "none"}
		default:
			return &toolChoiceConfig{Type: "auto"}
		}
	case map[string]any:
		// {type: "function", function: {name: "X"}}
		if fn, ok := v["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				return &toolChoiceConfig{Type: "tool", Name: name}
			}
		}
		return &toolChoiceConfig{Type: "auto"}
	}
	return nil
}

// buildToolResultBlock turns an OpenAI-shaped tool message into Anthropic's
// tool_result content block. When the original tool result is plain text we
// pass a string for compactness; when it carries images we emit nested
// content blocks (Anthropic supports both shapes).
func buildToolResultBlock(msg provider.Message) contentBlock {
	parts := provider.ContentToParts(msg.Content)
	hasImage := false
	hasCacheHint := false
	for _, p := range parts {
		if p.Type == "image_url" && usableImageURL(p.ImageURL) {
			hasImage = true
		}
		if ((p.Type == "text" && p.Text != "") ||
			(p.Type == "image_url" && usableImageURL(p.ImageURL))) && p.CacheControl != nil {
			hasCacheHint = true
		}
	}
	if !hasImage && !hasCacheHint {
		block := contentBlock{
			Type:      "tool_result",
			ToolUseID: msg.ToolCallID,
		}
		if text := provider.ContentToString(msg.Content); text != "" {
			block.Content = text
		}
		return block
	}
	var blocks []contentBlock
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				blocks = append(blocks, contentBlock{
					Type:         "text",
					Text:         p.Text,
					CacheControl: p.CacheControl,
				})
			}
		case "image_url":
			if usableImageURL(p.ImageURL) {
				b := convertImageToAnthropicBlock(p.ImageURL.URL)
				b.CacheControl = p.CacheControl
				blocks = append(blocks, b)
			}
		}
	}
	if len(blocks) == 0 {
		// Content is optional on tool_result. Leave it absent rather than
		// storing a typed nil slice in an interface (which serializes as null)
		// or emitting an empty string that carries no result.
		return contentBlock{
			Type:      "tool_result",
			ToolUseID: msg.ToolCallID,
		}
	}
	return contentBlock{
		Type:      "tool_result",
		ToolUseID: msg.ToolCallID,
		Content:   blocks,
	}
}

// buildAssistantMessage converts an OpenAI assistant message (possibly with
// tool_calls) to Anthropic format.
//
// Extended-thinking + tool_use round-trip: Anthropic requires the assistant
// turn that produced the tool_use to ALSO carry the original thinking block
// with its cryptographic signature. The gateway preserves the signature
// through Message.ReasoningContentSignature; if the client echoed it back we
// reconstruct the thinking block here. Without it, Anthropic returns 400
// ("signature missing") on the next turn.
//
// Provider-native blocks (server_tool_use / web_search_tool_result /
// code_execution_tool_result) shuttled through Message.ProviderTurnBlocks are
// re-emitted between thinking and text — preserving the original block order
// (thinking → server_tool_use → tool_result → text-with-citations) is what
// keeps Anthropic's multi-turn citation continuity working.
//
// cache_control on the assistant content is preserved so users can mark long
// responses as cache breakpoints in multi-turn conversations.
func buildAssistantMessage(msg provider.Message) message {
	hasThinking := hasSignedThinking(msg)
	hasRedacted := false
	for _, data := range msg.RedactedThinking {
		if data != "" {
			hasRedacted = true
			break
		}
	}
	hasProviderBlocks := false
	for _, raw := range msg.ProviderTurnBlocks {
		if _, ok := forwardableProviderBlock(raw); ok {
			hasProviderBlocks = true
			break
		}
	}

	contentParts := provider.ContentToParts(msg.Content)
	// Only a non-empty text part can actually carry a breakpoint into the
	// request — the emit loop below drops every other part type. Letting a
	// dropped part raise this flag would divert an otherwise plain turn onto
	// the block path and emit an empty content array, which Anthropic rejects.
	hasContentCacheHint := false
	for _, p := range contentParts {
		if p.Type == "text" && p.Text != "" && p.CacheControl != nil {
			hasContentCacheHint = true
			break
		}
	}

	// Fast path: plain text-only assistant turn with no thinking, no redacted
	// thinking, no tool calls, no cache hint — emit as a string for the most
	// compact wire form.
	if len(msg.ToolCalls) == 0 && !hasThinking && !hasRedacted && !hasContentCacheHint && !hasProviderBlocks {
		return message{
			Role:    "assistant",
			Content: provider.ContentToString(msg.Content),
		}
	}

	var blocks []contentBlock
	if hasThinking {
		blocks = append(blocks, contentBlock{
			Type:      "thinking",
			Thinking:  *msg.ReasoningContent,
			Signature: *msg.ReasoningContentSignature,
		})
	}
	// Echo back redacted_thinking blocks verbatim. Anthropic uses the opaque
	// `data` blob to retain reasoning state across turns.
	for _, data := range msg.RedactedThinking {
		if data == "" {
			continue
		}
		blocks = append(blocks, contentBlock{
			Type: "redacted_thinking",
			Data: data,
		})
	}
	// Re-emit provider-native blocks (server_tool_use, web_search_tool_result,
	// etc.) verbatim. We can't reconstruct these from OpenAI shape — the
	// client must have carried them through ProviderTurnBlocks. Their
	// position between thinking and text matches the original Anthropic
	// turn ordering, which is what keeps citation continuity intact.
	for _, raw := range msg.ProviderTurnBlocks {
		if m, ok := forwardableProviderBlock(raw); ok {
			blocks = append(blocks, contentBlock{Raw: m})
		}
	}
	if hasContentCacheHint {
		// Preserve per-part cache_control breakpoints so multi-turn caching
		// works for assistant responses too.
		for _, p := range contentParts {
			if p.Type != "text" || p.Text == "" {
				continue
			}
			blocks = append(blocks, contentBlock{
				Type:         "text",
				Text:         p.Text,
				CacheControl: p.CacheControl,
			})
		}
	} else if text := provider.ContentToString(msg.Content); text != "" {
		blocks = append(blocks, contentBlock{Type: "text", Text: text})
	}
	for _, tc := range msg.ToolCalls {
		blocks = append(blocks, contentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: json.RawMessage(tc.Function.Arguments),
		})
	}
	return message{
		Role:    "assistant",
		Content: blocks,
	}
}

// buildUserMessage converts an OpenAI user message to Anthropic format, handling multimodal content.
func buildUserMessage(msg provider.Message) message {
	parts := provider.ContentToParts(msg.Content)
	if len(parts) == 0 {
		return message{
			Role:    msg.Role,
			Content: provider.ContentToString(msg.Content),
		}
	}

	// Fast path only when no caching hint and a single text part. Any
	// cache_control marker requires the block-array form so Anthropic sees the
	// breakpoint.
	if len(parts) == 1 && parts[0].Type == "text" && parts[0].CacheControl == nil {
		return message{
			Role:    msg.Role,
			Content: parts[0].Text,
		}
	}

	// Multimodal or cache-marked: convert to Anthropic content blocks
	var blocks []contentBlock
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text == "" {
				continue
			}
			blocks = append(blocks, contentBlock{
				Type:         "text",
				Text:         p.Text,
				CacheControl: p.CacheControl,
			})
		case "image_url":
			if usableImageURL(p.ImageURL) {
				b := convertImageToAnthropicBlock(p.ImageURL.URL)
				b.CacheControl = p.CacheControl
				blocks = append(blocks, b)
			}
		case "file":
			// OpenAI file content (PDF docs etc.) → Anthropic document block.
			// Only the inline form (file_data + mime_type) translates cleanly;
			// file_id is OpenAI Files API namespace and Anthropic can't resolve
			// it. URL form via file_data starting with `data:` URI also works.
			if b := convertFileToAnthropicDocument(p.File); b != nil {
				b.CacheControl = p.CacheControl
				blocks = append(blocks, *b)
			}
		}
	}
	return message{
		Role:    msg.Role,
		Content: blocks,
	}
}

// convertFileToAnthropicDocument turns an OpenAI `file` content part into an
// Anthropic `document` block. Returns nil when the file lacks inline data
// (file_id-only references can't be resolved by Anthropic — silently dropped
// in line with how other unsupported parts get handled). Both raw base64
// + mime_type and `data:application/pdf;base64,...` URI forms are accepted.
func convertFileToAnthropicDocument(f *provider.FileContent) *contentBlock {
	if f == nil {
		return nil
	}
	if f.FileData == "" {
		return nil
	}
	// Accept either raw base64 (with explicit mime_type) or data URI.
	mediaType := f.MimeType
	data := f.FileData
	if strings.HasPrefix(data, "data:") {
		mt, d, ok := provider.ParseDataURI(data)
		if !ok || mt == "" || d == "" {
			return nil
		}
		mediaType, data = mt, d
	}
	if mediaType == "" {
		// Anthropic requires media_type. Fall back to a reasonable default
		// for the most common case (PDF) — bare base64 without mime_type
		// is the OpenAI "treat-as-pdf" implicit convention.
		mediaType = "application/pdf"
	}
	return &contentBlock{
		Type: "document",
		Source: &imageSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      data,
		},
	}
}

// convertImageToAnthropicBlock converts an image URL to an Anthropic image content block.
func convertImageToAnthropicBlock(url string) contentBlock {
	mediaType, data, ok := provider.ParseDataURI(url)
	if ok {
		return contentBlock{
			Type: "image",
			Source: &imageSource{
				Type:      "base64",
				MediaType: mediaType,
				Data:      data,
			},
		}
	}
	return contentBlock{
		Type: "image",
		Source: &imageSource{
			Type: "url",
			URL:  url,
		},
	}
}

// providerTurnBlockTypes are Anthropic content-block types that the gateway
// preserves verbatim across multi-turn conversations. They carry encrypted
// payloads (encrypted_content / encrypted_index) that Anthropic's upstream
// requires echoed back to maintain feature continuity (web search results,
// code execution outputs, etc.). The OpenAI message shape can't model them,
// so we shuttle them through Message.ProviderTurnBlocks.
var providerTurnBlockTypes = map[string]bool{
	"server_tool_use":                   true,
	"web_search_tool_result":            true,
	"code_execution_tool_result":        true,
	"bash_code_execution_result":        true,
	"text_editor_code_execution_result": true,
	"mcp_tool_use":                      true,
	"mcp_tool_result":                   true,
}

func convertResponse(ctx context.Context, resp *response, rawContent []map[string]any, jsonSchemaToolName string) *provider.ChatCompletionResponse {
	var content string
	var reasoningContent string
	var reasoningSignature string
	var redactedThinking []string
	var toolCalls []provider.ToolCall
	var annotations []map[string]any
	var providerTurnBlocks []any
	toolCallIndex := 0

	// Index raw blocks by position so we can fetch the verbatim JSON object
	// when we encounter a server-side tool block.
	getRawBlock := func(i int) map[string]any {
		if i < 0 || i >= len(rawContent) {
			return nil
		}
		return rawContent[i]
	}

	for i, block := range resp.Content {
		switch block.Type {
		case "thinking":
			reasoningContent += block.Thinking
			// Capture the FIRST non-empty signature. In interleaved-thinking mode
			// there can be multiple thinking blocks, but the OpenAI-compat surface
			// only carries one signature; the leading block is what clients need
			// to echo back to satisfy Anthropic's verification.
			if reasoningSignature == "" && block.Signature != "" {
				reasoningSignature = block.Signature
			}
		case "redacted_thinking":
			// Anthropic auto-redacts thinking blocks containing sensitive data.
			// The replacement block carries an opaque `data` blob that the model
			// uses to retain reasoning context across turns. We collect them so
			// the client can echo them back verbatim on multi-turn.
			if block.Data != "" {
				redactedThinking = append(redactedThinking, block.Data)
			}
		case "text":
			content += block.Text
			// Built-in server tools (web_search) attach citations onto text
			// blocks. Surface them as OpenAI-style annotations so clients can
			// render source attribution; encrypted_index is preserved for
			// callers that need to round-trip the citation back to Anthropic.
			for _, ct := range block.Citations {
				annotations = append(annotations, citationToAnnotation(ct))
			}
		case "tool_use":
			if jsonSchemaToolName != "" && block.Name == jsonSchemaToolName {
				// json_schema structured output — extract input as JSON content
				argsBytes, _ := json.Marshal(block.Input)
				content += string(argsBytes)
				continue
			}
			argsBytes, _ := json.Marshal(block.Input)
			idx := toolCallIndex
			toolCalls = append(toolCalls, provider.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: provider.ToolCallFunction{
					Name:      block.Name,
					Arguments: string(argsBytes),
				},
				Index: &idx,
			})
			toolCallIndex++
		default:
			// Provider-native block types (server_tool_use,
			// web_search_tool_result, code_execution_tool_result, etc.) are
			// not modeled in the OpenAI message shape. Preserve them in
			// ProviderTurnBlocks so a client that retains the field can
			// round-trip the assistant message back through the gateway,
			// keeping Anthropic's encrypted_content / encrypted_index
			// payloads intact.
			if providerTurnBlockTypes[block.Type] {
				if raw := getRawBlock(i); raw != nil {
					providerTurnBlocks = append(providerTurnBlocks, raw)
				}
			} else {
				// Unknown block type — leave a debug trail so an Anthropic API
				// addition (new server tool, new content modality) doesn't get
				// silently dropped from gateway responses. Operators can use
				// the log to decide whether to extend providerTurnBlockTypes.
				logging.From(ctx).Warn("llmkit: unhandled content block type — dropped from response",
					"type", block.Type)
			}
		}
	}

	finishReason := mapStopReason(resp.StopReason)
	// json_schema mode: if all tool_use blocks were json_schema, map "tool_calls" → "stop"
	if jsonSchemaToolName != "" && len(toolCalls) == 0 && finishReason == "tool_calls" {
		finishReason = "stop"
	}

	msg := &provider.Message{
		Role:    "assistant",
		Content: content,
	}
	if reasoningContent != "" {
		msg.ReasoningContent = &reasoningContent
	}
	if reasoningSignature != "" {
		msg.ReasoningContentSignature = &reasoningSignature
	}
	if len(redactedThinking) > 0 {
		msg.RedactedThinking = redactedThinking
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	if len(annotations) > 0 {
		msg.Annotations = annotations
	}
	if len(providerTurnBlocks) > 0 {
		msg.ProviderTurnBlocks = providerTurnBlocks
	}

	return &provider.ChatCompletionResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []provider.Choice{
			{
				Index:        0,
				Message:      msg,
				FinishReason: &finishReason,
			},
		},
		Usage: buildUsage(resp.Usage),
		// Surface code_execution sandbox descriptor so callers can pass it
		// back as request.container next turn (sandbox + files reused).
		Container: resp.Container,
	}
}

// buildUsage normalizes Anthropic usage into OpenAI-compatible accounting.
// PromptTokens must include cache read/creation tokens; otherwise total_tokens
// = prompt_tokens + completion_tokens under-counts every cache hit.
func buildUsage(u responseUsage) *provider.Usage {
	cacheCreationTokens := u.CacheCreationInputTokens
	cacheCreationDetailsKnown := false
	if u.CacheCreation != nil {
		detailTotal := u.CacheCreation.Ephemeral5mInputTokens + u.CacheCreation.Ephemeral1hInputTokens
		if cacheCreationTokens == 0 {
			cacheCreationTokens = detailTotal
		}
		cacheCreationDetailsKnown = detailTotal == cacheCreationTokens
	}
	prompt := u.InputTokens + u.CacheReadInputTokens + cacheCreationTokens
	usage := &provider.Usage{
		PromptTokens:        prompt,
		CompletionTokens:    u.OutputTokens,
		TotalTokens:         prompt + u.OutputTokens,
		CachedTokens:        u.CacheReadInputTokens,
		CacheCreationTokens: cacheCreationTokens,
		PromptTokensDetails: &provider.PromptTokensDetails{
			CachedTokens: u.CacheReadInputTokens,
		},
	}
	if cacheCreationDetailsKnown {
		usage.CacheCreationTokensDetails = &provider.CacheCreationTokensDetails{
			Ephemeral5mTokens: u.CacheCreation.Ephemeral5mInputTokens,
			Ephemeral1hTokens: u.CacheCreation.Ephemeral1hInputTokens,
		}
	}
	return usage
}

// citationToAnnotation flattens an Anthropic citation object into the
// OpenAI-compat annotations entry shape. Web search citations become
// `url_citation`; document/page citations fall back to a generic shape that
// preserves all locator fields so callers can re-emit them verbatim if needed.
//
// encrypted_index is preserved on every annotation — Anthropic requires it
// echoed back on the next turn to maintain citation continuity. Clients that
// only know OpenAI's url_citation can ignore this extra field.
func citationToAnnotation(c citation) map[string]any {
	if c.URL != "" {
		urlCit := map[string]any{
			"url":        c.URL,
			"title":      c.Title,
			"cited_text": c.CitedText,
		}
		if c.EncryptedIndex != "" {
			urlCit["encrypted_index"] = c.EncryptedIndex
		}
		return map[string]any{
			"type":         "url_citation",
			"url_citation": urlCit,
		}
	}
	// Document citation (char/page/block locators). OpenAI's annotations
	// schema doesn't define this exactly, so we emit a structurally similar
	// `file_citation` envelope that preserves all locator fields. Anthropic's
	// specific citation type (e.g. "char_location") goes inside file_citation
	// so the OpenAI annotation envelope stays standard-shaped — strict-mode
	// clients reject unknown top-level fields, lenient clients ignore the
	// inner one.
	fileCit := map[string]any{"cited_text": c.CitedText}
	if c.Type != "" {
		fileCit["anthropic_citation_type"] = c.Type
	}
	if c.EncryptedIndex != "" {
		fileCit["encrypted_index"] = c.EncryptedIndex
	}
	if c.DocumentIndex != nil {
		fileCit["document_index"] = *c.DocumentIndex
	}
	if c.StartCharIndex != nil {
		fileCit["start_char_index"] = *c.StartCharIndex
	}
	if c.EndCharIndex != nil {
		fileCit["end_char_index"] = *c.EndCharIndex
	}
	if c.StartPageNum != nil {
		fileCit["start_page_number"] = *c.StartPageNum
	}
	if c.EndPageNum != nil {
		fileCit["end_page_number"] = *c.EndPageNum
	}
	if c.StartBlockIdx != nil {
		fileCit["start_block_index"] = *c.StartBlockIdx
	}
	if c.EndBlockIdx != nil {
		fileCit["end_block_index"] = *c.EndBlockIdx
	}
	return map[string]any{
		"type":          "file_citation",
		"file_citation": fileCit,
	}
}

func mapStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "pause_turn":
		// pause_turn is emitted when a long-running server tool (e.g. web_search) needs
		// the client to continue the turn. From the client's perspective the model
		// stopped naturally, so map to "stop"; clients that care about the distinction
		// can consult the original Anthropic stop_reason via the upstream response.
		return "stop"
	case "refusal":
		// Refusal is mapped to OpenAI's content_filter so safety-aware clients react.
		return "content_filter"
	default:
		return "stop"
	}
}
