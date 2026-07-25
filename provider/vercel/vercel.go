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
// response. Vercel returns much more (pricing, tags, modalities) — we leave
// pricing to the seed pipeline and only extract what RemoteModel can carry.
type vercelModelEntry struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	ContextWindow int            `json:"context_window"`
	Pricing       *vercelPricing `json:"pricing"`
}

// vercelPricing extracts just enough pricing shape for ListModels to decide
// dispatch suitability. Full pricing data lives in cmd/fetch-vercel-pricing.
type vercelPricing struct {
	Input  string `json:"input"`  // per-token USD (string-encoded decimal)
	Output string `json:"output"` // per-token USD
	Image  string `json:"image"`  // per-image USD (set on per-image-billed image models)
}

// ListModels overrides compat's default to pull Vercel's richer per-model
// metadata. Filters to type=language so the chat /v1/models response doesn't
// surface embedding / image / video models the chat route can't dispatch to.
// Vercel's endpoint is public, but we still send the Bearer header — Vercel
// tolerates redundant auth and it keeps the request shape uniform with other
// providers' ListModels paths.
func (p *Provider) ListModels(ctx context.Context, apiKey string) ([]provider.RemoteModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

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

	models := make([]provider.RemoteModel, 0, len(result.Data))
	for _, m := range result.Data {
		// Surface models the gateway can actually dispatch:
		//   - language → /v1/chat/completions
		//   - embedding → /v1/embeddings
		//   - image (token-billed only, e.g. openai/gpt-image-2) → /v1/chat/completions
		//     with modalities:["image"]. compat already passes this through.
		//
		// Skipped:
		//   - image (per-image-billed, e.g. bfl/flux-*): Vercel routes them via
		//     chat/completions too but bills per-image; binding would need
		//     billing_unit=per_image which requires the seed pipeline to set
		//     endpoint_kind=images, and we have no /v1/images dispatch on
		//     vercel yet (Vercel has no separate /v1/images endpoint —
		//     everything funnels through chat/completions). Importing them
		//     here would create chat bindings with zero token prices → free.
		//   - video / reranking: no dispatch path at all.
		//
		// After import, the seed pipeline writes endpoint_kind/task_types so
		// embedding bindings accept /v1/embeddings calls; token-billed image
		// bindings stay as chat bindings (the default).
		if !vercelModelImportable(m) {
			continue
		}
		display := m.Name
		if display == "" {
			display = m.ID
		}
		models = append(models, provider.RemoteModel{
			ModelID:       m.ID,
			DisplayName:   display,
			ContextWindow: m.ContextWindow,
		})
	}
	return models, nil
}

// vercelModelImportable returns true when this model has a working dispatch
// path in the gateway. See ListModels comment for the full classification.
func vercelModelImportable(m vercelModelEntry) bool {
	switch m.Type {
	case "", "language", "embedding":
		return true
	case "image":
		// Token-billed image models (openai/gpt-image-*) run via chat/completions
		// with modalities. Per-image-billed (pricing.image set) have no dispatch.
		if m.Pricing == nil {
			return false
		}
		return m.Pricing.Image == "" && (m.Pricing.Input != "" || m.Pricing.Output != "")
	default:
		return false
	}
}
