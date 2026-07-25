package openrouter

import (
	"context"
	"maps"
	"strings"

	"github.com/siguago/llmkit/provider"
)

// GenerateImage 通过 OpenRouter chat/completions + modalities=["image","text"] 路径生成图像。
// OpenRouter 没有独立的 /v1/images/generations 接口（截止设计文档审核日），
// 因此 adapter 构造一个 chat 请求并把响应中的 message.images[] 提取成 MediaAsset 返回。
func (p *Provider) GenerateImage(ctx context.Context, apiKey, model string, req *provider.ImageGenerationRequest) (*provider.ImageGenerationResponse, error) {
	chatReq := &provider.ChatCompletionRequest{
		Model: model,
		Messages: []provider.Message{
			{Role: "user", Content: req.Prompt},
		},
		Modalities: []string{"image", "text"},
	}
	// OpenRouter image_config 与 OpenAI Images 字段名不同。把网关统一字段映射进 image_config：
	//   size -> image_size, aspect_ratio -> aspect_ratio, ...
	imageConfig := map[string]any{}
	if req.Size != "" {
		imageConfig["image_size"] = req.Size
	}
	if req.AspectRatio != "" {
		imageConfig["aspect_ratio"] = req.AspectRatio
	}
	// provider_options.openrouter.image_config 允许传入官方 image_config 字段（style/rgb/super_res…）
	if extras := openrouterImageConfigExtras(req.ProviderOptions); len(extras) > 0 {
		maps.Copy(imageConfig, extras)
	}
	if len(imageConfig) > 0 {
		chatReq.ImageConfig = imageConfig
	}
	if req.N != nil {
		chatReq.N = req.N
	}
	if req.User != "" {
		chatReq.User = req.User
	}
	// provider_options.openrouter.provider 用于强制后端路由
	if routing := openrouterProviderRouting(req.ProviderOptions); routing != nil {
		chatReq.ProviderRouting = routing
	}

	resp, err := p.ChatCompletion(ctx, apiKey, model, chatReq)
	if err != nil {
		return nil, err
	}

	out := &provider.ImageGenerationResponse{
		Created:  resp.Created,
		Model:    model,
		Provider: "openrouter",
		Usage:    resp.Usage,
	}
	// 收集所有 choice.message.images[]——extractMediaAssets 已经在 ChatCompletion 路径里挂好。
	for _, c := range resp.Choices {
		if c.Message != nil {
			out.Data = append(out.Data, c.Message.Images...)
		} else if c.Delta != nil {
			out.Data = append(out.Data, c.Delta.Images...)
		}
	}
	if out.Usage != nil && out.Usage.MediaCount == 0 {
		out.Usage.MediaCount = len(out.Data)
	}
	if out.Usage != nil && out.Usage.ImageCount == 0 {
		out.Usage.ImageCount = len(out.Data)
	}
	return out, nil
}

// EditImage 一期不支持——OpenRouter chat/completions 没有标准的图像编辑入口。
// 客户端需要图像编辑时应直连 OpenAI Images。
func (p *Provider) EditImage(ctx context.Context, apiKey, model string, req *provider.ImageEditRequest) (*provider.ImageGenerationResponse, error) {
	return nil, &provider.ErrUnsupported{Provider: "openrouter", Op: "image_edit"}
}

func openrouterImageConfigExtras(opts map[string]any) map[string]any {
	if opts == nil {
		return nil
	}
	or, ok := opts["openrouter"].(map[string]any)
	if !ok {
		return nil
	}
	cfg, ok := or["image_config"].(map[string]any)
	if !ok {
		return nil
	}
	// 去掉空字符串/nil，避免把 "" 发上去导致 upstream 校验失败。
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		if v == nil {
			continue
		}
		out[k] = v
	}
	return out
}

func openrouterProviderRouting(opts map[string]any) any {
	if opts == nil {
		return nil
	}
	or, ok := opts["openrouter"].(map[string]any)
	if !ok {
		return nil
	}
	return or["provider"]
}
