package provider

import (
	"context"
	"io"
)

// ImageGenerationRequest 是统一的图像生成请求。各家的字段差异由 adapter 内部
// 映射，不在这个公共结构里堆砌；不认识某个字段的厂商会忽略它。
//
// Model 和 Prompt 必填，其余可选。
type ImageGenerationRequest struct {
	Model             string `json:"model"`
	Prompt            string `json:"prompt"`
	N                 *int   `json:"n,omitempty"`
	Size              string `json:"size,omitempty"`
	AspectRatio       string `json:"aspect_ratio,omitempty"`
	Quality           string `json:"quality,omitempty"`
	Style             string `json:"style,omitempty"`
	Background        string `json:"background,omitempty"`
	OutputFormat      string `json:"output_format,omitempty"`
	OutputCompression *int   `json:"output_compression,omitempty"`
	Moderation        string `json:"moderation,omitempty"`
	// ResponseFormat is a request-side delivery preference ("url" or
	// "b64_json") for OpenAI-shaped image endpoints that still accept it.
	// Adapters that support it forward it; the rest ignore it. It is a
	// preference, not a guarantee — what you actually received is whichever
	// of URL / B64JSON the returned MediaAsset carries.
	ResponseFormat string         `json:"response_format,omitempty"`
	Stream         *bool          `json:"stream,omitempty"`
	User           string         `json:"user,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	// ProviderOptions is an opaque per-vendor block forwarded as-is, for knobs
	// this struct does not model. Providers that don't recognize it ignore it.
	ProviderOptions map[string]any `json:"provider_options,omitempty"`
}

// ImageEditRequest 与生成请求共享多数字段，额外携带 multipart 上传内容。
//
// Model、Prompt 和至少一张 Images 必填。
type ImageEditRequest struct {
	Model             string
	Prompt            string
	Images            []UploadPart
	Mask              *UploadPart
	N                 *int
	Size              string
	Quality           string
	Background        string
	OutputFormat      string
	OutputCompression *int
	// ResponseFormat 语义同 ImageGenerationRequest.ResponseFormat：请求侧交付
	// 偏好，支持的 adapter 转发，实际拿到什么以返回的 MediaAsset 为准。
	ResponseFormat  string
	User            string
	Metadata        map[string]any
	ProviderOptions map[string]any
}

// UploadPart 是 multipart 上传的最小抽象。
//
// Reader 只能消费一次，这也是 Client.EditImage 从不重试的原因：第二次尝试拿到
// 的是一个已经读空的 Reader。adapter 内部若需重发必须自行 ReadAll。
type UploadPart struct {
	Filename    string
	ContentType string
	SizeBytes   int64
	Reader      io.Reader
}

// ImageGenerationResponse 是统一的图像响应。Data 是生成的资产列表，
// Usage 用现有 Usage 表达（PromptTokens / CompletionTokens / ImageCount / Cost 等）。
type ImageGenerationResponse struct {
	Object   string       `json:"object,omitempty"`
	Created  int64        `json:"created"`
	Model    string       `json:"model"`
	Data     []MediaAsset `json:"data"`
	Usage    *Usage       `json:"usage,omitempty"`
	Provider string       `json:"provider_name,omitempty"`
}

// VideoCreateRequest 是统一的视频创建请求。各家的字段差异由 adapter 内部映射；
// 不认识某个字段的厂商会忽略它。
//
// Model 和 Prompt 必填，其余可选。
type VideoCreateRequest struct {
	Model               string           `json:"model"`
	Prompt              string           `json:"prompt"`
	Size                string           `json:"size,omitempty"`
	AspectRatio         string           `json:"aspect_ratio,omitempty"`
	Resolution          string           `json:"resolution,omitempty"`
	DurationSeconds     *float64         `json:"duration_seconds,omitempty"`
	Seed                *int             `json:"seed,omitempty"`
	GenerateAudio       *bool            `json:"generate_audio,omitempty"`
	NegativePrompt      string           `json:"negative_prompt,omitempty"`
	InputReferenceImage *ImageReference  `json:"input_reference_image,omitempty"`
	FrameImages         []FrameImage     `json:"frame_images,omitempty"`
	InputReferences     []InputReference `json:"input_references,omitempty"`
	WebhookURL          string           `json:"webhook_url,omitempty"`
	Metadata            map[string]any   `json:"metadata,omitempty"`
	User                string           `json:"user,omitempty"`
	ProviderOptions     map[string]any   `json:"provider_options,omitempty"`
}

// ImageReference 支持 file_id / image_url / b64_json 三种形式之一。
type ImageReference struct {
	FileID   string `json:"file_id,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	B64JSON  string `json:"b64_json,omitempty"`
}

// FrameImage 是视频生成的首帧/末帧引导图。
type FrameImage struct {
	Role     string `json:"role,omitempty"` // first | last
	FileID   string `json:"file_id,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	B64JSON  string `json:"b64_json,omitempty"`
}

// InputReference 是 OpenRouter 的 style / character / object 风格引导。
type InputReference struct {
	Type     string `json:"type,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	B64JSON  string `json:"b64_json,omitempty"`
}

// VideoJob 是一个异步视频任务的状态。Client.CreateVideo 返回它，Client.GetVideo
// 刷新它，Client.WaitVideo 轮询到终态。
//
// Status 取 VideoStatus* 常量之一；用 IsTerminalVideoStatus 判断是否还需轮询。
// 任务失败时 Status 是 VideoStatusFailed 且 Error 非空 —— 注意那不是调用错误，
// GetVideo 本身返回的 err 仍是 nil。
type VideoJob struct {
	ID            string         `json:"id"`
	Model         string         `json:"model"`
	ProviderName  string         `json:"provider_name"`
	ProviderJobID string         `json:"provider_job_id,omitempty"`
	Status        string         `json:"status"`
	Progress      int            `json:"progress,omitempty"`
	Assets        []MediaAsset   `json:"assets,omitempty"`
	Error         *ErrorObject   `json:"error,omitempty"`
	Usage         *Usage         `json:"usage,omitempty"`
	CreatedAt     int64          `json:"created_at"`
	UpdatedAt     int64          `json:"updated_at"`
	CompletedAt   int64          `json:"completed_at,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// ErrorObject 描述一个终态失败的视频任务。Message 和 Raw 来自上游原文，
// Code / Type 是归一化后的分类。
type ErrorObject struct {
	Message  string `json:"message"`
	Code     string `json:"code,omitempty"`
	Type     string `json:"type,omitempty"`
	Provider string `json:"provider,omitempty"`
	Raw      string `json:"raw,omitempty"`
}

// 视频任务状态。VideoStatusCancelRequested 只会出现在支持取消的厂商上
// （见 Client.SupportsVideoCancellation）。
const (
	VideoStatusQueued          = "queued"
	VideoStatusInProgress      = "in_progress"
	VideoStatusCompleted       = "completed"
	VideoStatusFailed          = "failed"
	VideoStatusCancelRequested = "cancel_requested"
	VideoStatusCancelled       = "cancelled"
	VideoStatusExpired         = "expired"
)

// IsTerminalVideoStatus 判断状态是否进入终态（不需再轮询）。
func IsTerminalVideoStatus(s string) bool {
	switch s {
	case VideoStatusCompleted, VideoStatusFailed, VideoStatusCancelled, VideoStatusExpired:
		return true
	default:
		return false
	}
}

// 媒体能力按「单个端点」而不是「整块功能」切分，因为厂商的支持就是按端点参差
// 的：Vercel 和 OpenRouter 能生成图像但没有编辑端点，Gemini 和 OpenRouter 能
// 建视频任务但没有取消端点。
//
// 只实现得了一半的 adapter 就只实现对应的那个接口，不要用返回 ErrUnsupported
// 的空方法把接口凑齐 —— 那样类型断言会返回 true，调用方拿到的能力探测就是错的。

// ImageGenerator 支持从文本提示生成图像。
type ImageGenerator interface {
	Name() string
	GenerateImage(ctx context.Context, apiKey, model string, req *ImageGenerationRequest) (*ImageGenerationResponse, error)
}

// ImageEditor 支持按提示编辑上传的图像。比 ImageGenerator 少见得多：
// 聚合类服务通常只转发生成端点。
type ImageEditor interface {
	Name() string
	EditImage(ctx context.Context, apiKey, model string, req *ImageEditRequest) (*ImageGenerationResponse, error)
}

// ImageProvider 是两种图像能力都具备的 provider。断言它等于同时要求生成和编辑；
// 只需要其中一种时请断言更小的那个接口。
type ImageProvider interface {
	ImageGenerator
	ImageEditor
}

// VideoCreator 支持提交并轮询异步视频任务。创建和查询不拆开：能建任务却查不了
// 状态的 adapter 没有意义。
type VideoCreator interface {
	Name() string
	CreateVideoJob(ctx context.Context, apiKey, model string, req *VideoCreateRequest) (*VideoJob, error)
	GetVideoJob(ctx context.Context, apiKey string, job *VideoJob) (*VideoJob, error)
}

// VideoCanceller 支持请求上游取消运行中的任务。内置 adapter 中目前只有
// Volcengine 实现；Gemini、DashScope、EasyRouter 和 OpenRouter 都没有取消
// 端点。调用前可通过 Client.SupportsVideoCancellation 探测。
type VideoCanceller interface {
	Name() string
	CancelVideoJob(ctx context.Context, apiKey string, job *VideoJob) (*VideoJob, error)
}

// VideoProvider 是视频能力齐全（含取消）的 provider。
type VideoProvider interface {
	VideoCreator
	VideoCanceller
}

// ErrUnsupported 表示 provider adapter 主动声明能力不支持。例如 OpenAI Videos 没有 cancel 端点。
type ErrUnsupported struct {
	Op       string
	Provider string
}

func (e *ErrUnsupported) Error() string {
	return e.Provider + " does not support: " + e.Op
}
