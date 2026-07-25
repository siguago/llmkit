package provider

import (
	"context"
	"io"
)

// ImageGenerationRequest 是网关侧统一的图像生成请求形态。
// 不同 provider 的字段差异由 adapter 内部映射，不在公共结构里堆砌。
type ImageGenerationRequest struct {
	Model             string `json:"model" binding:"required"`
	Prompt            string `json:"prompt" binding:"required"`
	N                 *int   `json:"n,omitempty"`
	Size              string `json:"size,omitempty"`
	AspectRatio       string `json:"aspect_ratio,omitempty"`
	Quality           string `json:"quality,omitempty"`
	Style             string `json:"style,omitempty"`
	Background        string `json:"background,omitempty"`
	OutputFormat      string `json:"output_format,omitempty"`
	OutputCompression *int   `json:"output_compression,omitempty"`
	Moderation        string `json:"moderation,omitempty"`
	// Delivery controls gateway-side asset delivery. "proxy" (default) stores
	// assets and returns /v1/media/:id/content. "inline" also includes b64_json
	// when the upstream returned inline data and the response fits config limits.
	Delivery        string         `json:"delivery,omitempty"`        // proxy | inline
	ResponseFormat  string         `json:"response_format,omitempty"` // deprecated; provider-specific passthrough only
	Stream          *bool          `json:"stream,omitempty"`
	User            string         `json:"user,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	ProviderOptions map[string]any `json:"provider_options,omitempty"`
}

// ImageEditRequest 与生成请求共享多数字段；额外携带二进制上传内容（multipart）。
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
	Delivery          string
	ResponseFormat    string // deprecated
	User              string
	Metadata          map[string]any
	ProviderOptions   map[string]any
}

// UploadPart 是 multipart 上传的最小抽象。Reader 只能一次性消费；
// adapter 内部需要重发或缓存时必须自行 ReadAll。
type UploadPart struct {
	Filename    string
	ContentType string
	SizeBytes   int64
	Reader      io.Reader
}

// ImageGenerationResponse 是网关侧统一的图像响应。`Data` 是生成的资产列表，
// `Usage` 用现有 Usage 表达（PromptTokens / CompletionTokens / ImageCount / Cost 等）。
type ImageGenerationResponse struct {
	Object   string       `json:"object,omitempty"`
	Created  int64        `json:"created"`
	Model    string       `json:"model"`
	Data     []MediaAsset `json:"data"`
	Usage    *Usage       `json:"usage,omitempty"`
	Provider string       `json:"provider_name,omitempty"`
}

// VideoCreateRequest 是网关侧统一的视频创建请求。Provider adapter 必须按 §10 映射。
type VideoCreateRequest struct {
	Model               string           `json:"model" binding:"required"`
	Prompt              string           `json:"prompt" binding:"required"`
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

// VideoJob 是网关侧的视频任务模型，对外 HTTP 响应固定 object=video.job。
// 状态枚举：queued / in_progress / completed / failed / cancel_requested / cancelled / expired。
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

// ErrorObject 标识终态失败的错误。Message 来自上游；Code 是网关分类。
type ErrorObject struct {
	Message  string `json:"message"`
	Code     string `json:"code,omitempty"`
	Type     string `json:"type,omitempty"`
	Provider string `json:"provider,omitempty"`
	Raw      string `json:"raw,omitempty"`
}

// 视频状态枚举常量（与设计文档 §6.6 对齐）
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

// ImageProvider 是支持图像生成/编辑的 provider 子接口。
// Registry 用类型断言判断 provider 是否实现该接口；不强制所有 provider 实现。
type ImageProvider interface {
	Name() string
	GenerateImage(ctx context.Context, apiKey, model string, req *ImageGenerationRequest) (*ImageGenerationResponse, error)
	EditImage(ctx context.Context, apiKey, model string, req *ImageEditRequest) (*ImageGenerationResponse, error)
}

// VideoProvider 是支持视频任务的 provider 子接口。CancelVideoJob 可返回 unsupported。
type VideoProvider interface {
	Name() string
	CreateVideoJob(ctx context.Context, apiKey, model string, req *VideoCreateRequest) (*VideoJob, error)
	GetVideoJob(ctx context.Context, apiKey string, job *VideoJob) (*VideoJob, error)
	CancelVideoJob(ctx context.Context, apiKey string, job *VideoJob) (*VideoJob, error)
}

// ErrUnsupported 表示 provider adapter 主动声明能力不支持。例如 OpenAI Videos 没有 cancel 端点。
type ErrUnsupported struct {
	Op       string
	Provider string
}

func (e *ErrUnsupported) Error() string {
	return e.Provider + " does not support: " + e.Op
}
