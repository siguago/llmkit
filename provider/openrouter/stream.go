package openrouter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/siguago/llmkit/provider"
)

type StreamReader struct {
	reader         io.ReadCloser
	scanner        *bufio.Scanner
	usage          *provider.Usage
	emitUsageChunk bool
	// imageCount 累加流式中每个 chunk 解析到的图像数量。
	// OpenRouter 的 usage chunk 不报 image_count，所以由网关自行从 message.images[] / delta.images[]
	// 等多个候选路径中容错探测，stream 结束时 merge 到 usage.ImageCount。
	imageCount int
	// mediaCount 累加结构化资产数量，merge 到 usage.MediaCount。
	mediaCount int
}

func NewStreamReader(reader io.ReadCloser, emitUsageChunk bool) *StreamReader {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &StreamReader{
		reader:         reader,
		scanner:        scanner,
		emitUsageChunk: emitUsageChunk,
	}
}

func (s *StreamReader) Recv() (*provider.ChatCompletionChunk, error) {
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" {
			continue
		}
		// OpenRouter occasionally injects keep-alive comment lines; data: " " or
		// data: : ping. The "[DONE]" sentinel and any non-JSON payload after the
		// prefix should not panic the decoder. Accept both "data: foo" and
		// "data:foo" to tolerate proxies that strip the optional space.
		var data string
		switch {
		case strings.HasPrefix(line, "data: "):
			data = line[6:]
		case strings.HasPrefix(line, "data:"):
			data = line[5:]
		default:
			continue
		}
		if data == "[DONE]" {
			return nil, io.EOF
		}
		if !strings.HasPrefix(data, "{") {
			continue
		}

		// Mid-stream error envelope from OpenRouter (e.g. routing failure
		// after 200 OK). Without detection these would slip through as empty
		// chunks and the client would see a silently truncated stream.
		if errPayload, ok := detectOpenRouterStreamError([]byte(data)); ok {
			return nil, errPayload
		}

		var chunk provider.ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			slog.Warn("openrouter stream: chunk parse error", "error", err, "data_preview", truncate(data, 200))
			continue
		}

		// OpenRouter emits the incremental reasoning trace as delta.reasoning
		// (string) for OpenAI-shaped clients. The unified ChatCompletionChunk
		// doesn't bind that field, so we re-parse the raw payload and stitch it
		// onto the choice's reasoning_content.
		normalizeReasoningOnChunk([]byte(data), &chunk)

		// 图像类模型（如 openai/gpt-image-2 经 OpenRouter 路由）会在 message.images[]
		// 或 delta.images[] 里挂载图像数据。累加每 chunk 的增量；最终 GetUsage 时 merge。
		s.imageCount += countImagesInChunk([]byte(data))

		// 结构化资产：把每个增量 chunk 中的 image entry 解析成 MediaAsset，
		// 直接挂到 chunk 的第一个 delta 上，客户端能在流式响应里拿到完整 URL/base64。
		if assets := extractMediaAssets([]byte(data)); len(assets) > 0 {
			attachAssetsToChunk(&chunk, assets)
			s.mediaCount += len(assets)
		}

		if chunk.Usage != nil {
			provider.NormalizeUsage(chunk.Usage)
			// 把流式中累加的 image_count 合并进上游 usage——OpenRouter 不报这个维度，
			// 必须由网关计数才能让 per_image 策略拿到完整数。
			if s.imageCount > 0 && chunk.Usage.ImageCount == 0 {
				chunk.Usage.ImageCount = s.imageCount
			}
			if s.mediaCount > 0 && chunk.Usage.MediaCount == 0 {
				chunk.Usage.MediaCount = s.mediaCount
			}
			s.usage = chunk.Usage
			if len(chunk.Choices) == 0 && !s.emitUsageChunk {
				continue
			}
		}

		return &chunk, nil
	}

	if err := s.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (s *StreamReader) Close() error {
	return s.reader.Close()
}

func (s *StreamReader) GetUsage() *provider.Usage {
	// 兜底：stream 结束时如果上游没发 usage chunk 但我们累计到了图像数（理论上罕见），
	// 仍然合成一个最小 usage，让 per_image / per_request 策略能正常计费。
	if s.usage == nil && (s.imageCount > 0 || s.mediaCount > 0) {
		s.usage = &provider.Usage{ImageCount: s.imageCount, MediaCount: s.mediaCount, RequestCount: 1}
	}
	if s.usage != nil {
		if s.usage.ImageCount == 0 && s.imageCount > 0 {
			s.usage.ImageCount = s.imageCount
		}
		if s.usage.MediaCount == 0 && s.mediaCount > 0 {
			s.usage.MediaCount = s.mediaCount
		}
	}
	return s.usage
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// detectOpenRouterStreamError mirrors compat.detectStreamError. OpenRouter
// emits {"error":{...}} chunks after 200 OK when routing fails or the chosen
// upstream rejects partway. The wire format we emit must match
// NewProviderError's so handler.errorBody can unwrap the OpenAI-shape body
// directly into the client's SSE error chunk.
//
// Also extracts retry_after from the body when present so the handler can
// echo it as Retry-After even though we're past the HTTP-status header phase.
func detectOpenRouterStreamError(data []byte) (*provider.ProviderError, bool) {
	var aux struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(data, &aux); err != nil || len(aux.Error) == 0 {
		return nil, false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, false
	}
	for k := range probe {
		if k != "error" {
			return nil, false
		}
	}
	status := http.StatusBadGateway
	if codeFloat, ok := aux.Error["code"].(float64); ok {
		if c := int(codeFloat); c >= 400 && c < 600 {
			status = c
		}
	}
	return &provider.ProviderError{
		StatusCode: status,
		Message:    fmt.Sprintf("openrouter api error (status %d): %s", status, string(data)),
		RetryAfter: extractRetryAfterField(aux.Error),
	}, true
}

// extractRetryAfterField pulls a backoff hint out of an upstream error body.
// Same logic as compat.extractRetryAfter but inlined to avoid a package
// dependency. Common shapes: {"retry_after": 30 | "30"}, {"retryAfter": 30}.
func extractRetryAfterField(errObj map[string]any) string {
	for _, key := range []string{"retry_after", "retryAfter"} {
		if v, ok := errObj[key]; ok {
			switch n := v.(type) {
			case string:
				if n != "" {
					return n
				}
			case float64:
				if n > 0 {
					return fmt.Sprintf("%d", int(n))
				}
			}
		}
	}
	return ""
}

// countImagesInChunk 容错探测 OpenRouter 图像路由模型在 chat-completions 响应里
// 挂载图像的多种 JSON 路径。OpenRouter 没有统一规范，不同上游图像模型路径可能不一致；
// 这里尝试常见三种，任一非空即累加；找不到不报错，让 strategy 用 RequestCount fallback。
//
//	choices[].delta.images[]    - 流式增量（hypothetical/常见 SSE 形态）
//	choices[].message.images[]  - 非流式或最终态
//	choices[].image_count       - 上游聚合后的数量字段
func countImagesInChunk(raw []byte) int {
	var aux struct {
		Choices []struct {
			Delta *struct {
				Images []json.RawMessage `json:"images"`
			} `json:"delta"`
			Message *struct {
				Images []json.RawMessage `json:"images"`
			} `json:"message"`
			ImageCount int `json:"image_count"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return 0
	}
	total := 0
	for _, c := range aux.Choices {
		if c.Delta != nil {
			total += len(c.Delta.Images)
		}
		if c.Message != nil {
			total += len(c.Message.Images)
		}
		if c.ImageCount > 0 {
			total += c.ImageCount
		}
	}
	return total
}

// extractMediaAssets parses OpenRouter chat-shape image output into the unified
// MediaAsset list. Walks the same three JSON paths as countImagesInChunk
// (delta.images, message.images, top-level images) and normalizes each entry
// into a structured asset.
//
// OpenRouter's documented shape is:
//
//	{"type": "image_url", "image_url": {"url": "data:image/png;base64,...."}}
//
// where the `url` value may be either a data URL or an external HTTPS URL.
// Data URLs are split into MimeType + B64JSON + DataURL; non-data URLs are
// stored on URL alone (no SSRF download here — that's a downstream decision).
func extractMediaAssets(raw []byte) []provider.MediaAsset {
	var aux struct {
		Choices []struct {
			Delta *struct {
				Images []json.RawMessage `json:"images"`
			} `json:"delta"`
			Message *struct {
				Images []json.RawMessage `json:"images"`
			} `json:"message"`
		} `json:"choices"`
		Images []json.RawMessage `json:"images"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return nil
	}
	var assets []provider.MediaAsset
	for _, c := range aux.Choices {
		if c.Delta != nil {
			for _, img := range c.Delta.Images {
				if a, ok := normalizeChatImageEntry(img); ok {
					assets = append(assets, a)
				}
			}
		}
		if c.Message != nil {
			for _, img := range c.Message.Images {
				if a, ok := normalizeChatImageEntry(img); ok {
					assets = append(assets, a)
				}
			}
		}
	}
	for _, img := range aux.Images {
		if a, ok := normalizeChatImageEntry(img); ok {
			assets = append(assets, a)
		}
	}
	return assets
}

func normalizeChatImageEntry(raw json.RawMessage) (provider.MediaAsset, bool) {
	var asset provider.MediaAsset
	asset.Type = "image"
	var aux struct {
		Type     string `json:"type"`
		ImageURL *struct {
			URL    string `json:"url"`
			Detail string `json:"detail"`
		} `json:"image_url"`
		URL      string `json:"url"`
		B64JSON  string `json:"b64_json"`
		MimeType string `json:"mime_type"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		FileID   string `json:"file_id"`
		ID       string `json:"id"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		// Possibly a bare string URL.
		var s string
		if err2 := json.Unmarshal(raw, &s); err2 == nil && s != "" {
			fillURLOrDataURL(&asset, s)
			return asset, true
		}
		return asset, false
	}
	if aux.ImageURL != nil && aux.ImageURL.URL != "" {
		fillURLOrDataURL(&asset, aux.ImageURL.URL)
	}
	if aux.URL != "" && asset.URL == "" && asset.DataURL == "" {
		fillURLOrDataURL(&asset, aux.URL)
	}
	if aux.B64JSON != "" {
		asset.B64JSON = aux.B64JSON
	}
	if aux.MimeType != "" {
		asset.MimeType = aux.MimeType
	}
	if aux.Width > 0 {
		asset.Width = aux.Width
	}
	if aux.Height > 0 {
		asset.Height = aux.Height
	}
	if aux.FileID != "" {
		asset.FileID = aux.FileID
	}
	if aux.ID != "" {
		asset.ID = aux.ID
	}
	if asset.URL == "" && asset.B64JSON == "" && asset.DataURL == "" && asset.FileID == "" {
		return asset, false
	}
	return asset, true
}

// attachAssetsToChunk 把流式 chunk 中提取到的 MediaAsset 列表挂到第一个 choice 的
// delta.Images（流式增量）上。该字段在 SSE 序列化时会出 JSON，让客户端在流式响应里直接看到结构化资产。
func attachAssetsToChunk(chunk *provider.ChatCompletionChunk, assets []provider.MediaAsset) {
	if chunk == nil || len(assets) == 0 || len(chunk.Choices) == 0 {
		return
	}
	target := chunk.Choices[0].Delta
	if target == nil {
		target = chunk.Choices[0].Message
	}
	if target == nil {
		return
	}
	target.Images = append(target.Images, assets...)
}

func fillURLOrDataURL(asset *provider.MediaAsset, raw string) {
	if strings.HasPrefix(raw, "data:") {
		if mt, data, ok := provider.ParseDataURI(raw); ok {
			asset.DataURL = raw
			asset.MimeType = mt
			asset.B64JSON = data
			return
		}
		asset.DataURL = raw
		return
	}
	asset.URL = raw
}

// normalizeReasoningOnChunk lifts choices[].delta.reasoning from the raw chunk
// into the unified delta.reasoning_content. OpenRouter reasoning models stream
// thoughts here for OpenAI-compat clients.
func normalizeReasoningOnChunk(raw []byte, chunk *provider.ChatCompletionChunk) {
	var aux struct {
		Choices []struct {
			Delta *struct {
				Reasoning *string `json:"reasoning"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return
	}
	for i, c := range aux.Choices {
		if i >= len(chunk.Choices) || c.Delta == nil || c.Delta.Reasoning == nil {
			continue
		}
		if chunk.Choices[i].Delta != nil && chunk.Choices[i].Delta.ReasoningContent == nil {
			chunk.Choices[i].Delta.ReasoningContent = c.Delta.Reasoning
		}
	}
}
