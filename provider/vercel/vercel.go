// Package vercel wraps Vercel AI Gateway as an OpenAI-compatible aggregator
// provider. Vercel's API at https://ai-gateway.vercel.sh/v1 is 1:1 OpenAI
// Chat Completions / Embeddings / Models, so we ride on top of compat.New for
// the chat path and only override ListModels to pull Vercel's richer metadata
// (context_window + display name straight from the upstream).
package vercel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/siguago/llmkit/internal/httpx"
	"github.com/siguago/llmkit/provider"
	"github.com/siguago/llmkit/provider/compat"
)

const (
	name           = "vercel"
	defaultBaseURL = "https://ai-gateway.vercel.sh/v1"
)

// Provider embeds the OpenAI-compat surface and adds a ListModels override
// that surfaces Vercel's per-model context_window + human-readable name.
type Provider struct {
	*compat.Provider
	baseURL string
	client  *http.Client
}

// New constructs a Vercel AI Gateway provider. Empty baseURL falls back to the
// official endpoint; env VERCEL_BASE_URL or yaml providers.vercel_base_url can
// override (handled by config loader). Mirrors easyrouter's normalize so users
// can configure just the host root and we'll append /v1.
func New(baseURL string) *Provider {
	baseURL = normalizeBaseURL(baseURL)
	outboundTransport := httpx.NewOutbound()
	return &Provider{
		Provider: compat.New(compat.Config{
			ProviderName:   name,
			BaseURL:        baseURL,
			PassModalities: true, // Vercel forwards multimodal chat output
			PassAudio:      false,
		}),
		baseURL: baseURL,
		// 300s 与其它 image/video provider（easyrouter/openrouter/anthropic 等）对齐：
		// 共享给 ListModels（短任务，自带 30s context 内层兜底）和 images.go 的
		// GenerateImage（gpt-image-2 在高画质下经常 30-90s）。30s 上限会把上游正
		// 常的长任务染成 context.DeadlineExceeded → 502，掩盖真实状态。
		// EditImage 在 Vercel 上不存在，固定返回 ErrUnsupported，不走 HTTP。
		client: &http.Client{
			Timeout:   300 * time.Second,
			Transport: outboundTransport,
		},
	}
}

// normalizeBaseURL accepts either a full /v1 URL or a host root and ensures
// the result ends in /v1 with no trailing slash. Mirrors easyrouter's logic so
// `vercel_base_url: "https://ai-gateway.vercel.sh"` works the same as the
// fully-qualified form.
func normalizeBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return defaultBaseURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		if strings.HasSuffix(raw, "/v1") {
			return raw
		}
		return raw + "/v1"
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/v1"
	} else if !strings.HasSuffix(u.Path, "/v1") {
		u.Path += "/v1"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// vercelModelEntry mirrors the fields we care about from Vercel's /v1/models
// response. Vercel returns much more (tags, modalities) — pricing is decoded
// only to preserve the historical ListModels filter and is never exported.
type vercelModelEntry struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	ContextWindow int            `json:"context_window"`
	Pricing       *vercelPricing `json:"pricing"`
}

// vercelPricing is retained for the legacy ListModels filter. Historically it
// exposed token-billed image models that Vercel can serve through chat while
// hiding per-image-billed entries that the old gateway could not bill safely.
type vercelPricing struct {
	Input  string `json:"input"`
	Output string `json:"output"`
	Image  string `json:"image"`
}

// ListModels overrides compat's default to pull Vercel's richer per-model
// metadata. Its optional task-listing extension classifies the mixed language,
// embedding and image catalog without changing RemoteModel.
// Vercel's endpoint is public, but we still send the Bearer header when there is
// one — Vercel tolerates redundant auth and it keeps the request shape uniform
// with other providers' ListModels paths.
func (p *Provider) ListModels(ctx context.Context, apiKey string) ([]provider.RemoteModel, error) {
	entries, err := p.listRemoteModels(ctx, apiKey)
	if err != nil {
		return nil, err
	}
	models := make([]provider.RemoteModel, 0, len(entries))
	for _, entry := range entries {
		if vercelModelImportable(entry) {
			models = append(models, entry.remoteModel())
		}
	}
	return models, nil
}

// ListModelsWithTaskTypes returns Vercel's mixed catalog with classifications
// derived from the upstream type field. Untyped legacy entries remain listed
// but deliberately have no task-map entry.
func (p *Provider) ListModelsWithTaskTypes(ctx context.Context, apiKey string) ([]provider.RemoteModel, map[string][]string, error) {
	entries, err := p.listRemoteModels(ctx, apiKey)
	if err != nil {
		return nil, nil, err
	}
	models := make([]provider.RemoteModel, 0, len(entries))
	taskTypes := make(map[string][]string)
	for _, entry := range entries {
		if !vercelRichModelImportable(entry) {
			continue
		}
		models = append(models, entry.remoteModel())
		if tasks := vercelModelTaskTypes(entry); len(tasks) > 0 {
			taskTypes[entry.ID] = tasks
		}
	}
	return models, taskTypes, nil
}

func (m vercelModelEntry) remoteModel() provider.RemoteModel {
	display := m.Name
	if display == "" {
		display = m.ID
	}
	return provider.RemoteModel{
		ModelID:       m.ID,
		DisplayName:   display,
		ContextWindow: m.ContextWindow,
	}
}

func (p *Provider) listRemoteModels(ctx context.Context, apiKey string) ([]vercelModelEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	provider.SetBearer(httpReq.Header, apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		return nil, provider.NewProviderErrorFromResponse(resp, name, respBody)
	}

	var result struct {
		Data []vercelModelEntry `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Data, nil
}

// vercelModelTaskTypes follows Vercel's explicit catalog type. Empty means
// unknown rather than chat, so old or future entries cannot be misclassified.
func vercelModelTaskTypes(m vercelModelEntry) []string {
	switch strings.ToLower(strings.TrimSpace(m.Type)) {
	case "language":
		return []string{provider.RemoteModelTaskChat}
	case "embedding":
		return []string{provider.RemoteModelTaskEmbedding}
	case "image":
		return []string{provider.RemoteModelTaskImageGenerate}
	default:
		return nil
	}
}

// vercelModelImportable preserves the historical ListModels filter. In
// particular, only token-billed image entries were exposed through the legacy
// chat-oriented catalog; per-image entries remain exclusive to the rich task
// catalog where callers can bind them to image.generate explicitly.
func vercelModelImportable(m vercelModelEntry) bool {
	switch m.Type {
	case "", "language", "embedding":
		return true
	case "image":
		if m.Pricing == nil {
			return false
		}
		return m.Pricing.Image == "" && (m.Pricing.Input != "" || m.Pricing.Output != "")
	default:
		return false
	}
}

func vercelRichModelImportable(m vercelModelEntry) bool {
	switch strings.ToLower(strings.TrimSpace(m.Type)) {
	case "", "language", "embedding", "image":
		return true
	default:
		return false
	}
}

var _ provider.ModelTaskLister = (*Provider)(nil)
