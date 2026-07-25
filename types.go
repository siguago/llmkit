package llmkit

import "github.com/siguago/llmkit/provider"

// The unified request/response surface lives in the provider package so that
// every vendor adapter can share it. These aliases re-export it at the root so
// ordinary callers only ever import one package.
//
// They are Go type ALIASES, not definitions — llmkit.ChatRequest and
// provider.ChatCompletionRequest are the same type, so values pass freely
// between the root façade and any adapter you drive directly.
type (
	// ChatRequest is the unified, OpenAI-shaped chat completion request.
	// Vendor-specific extensions live on it as optional fields; adapters that
	// don't understand a field ignore it.
	ChatRequest = provider.ChatCompletionRequest
	// ChatResponse is the unified non-streaming chat completion response.
	ChatResponse = provider.ChatCompletionResponse
	// Chunk is one streaming delta.
	Chunk = provider.ChatCompletionChunk
	// Stream reads Chunks until io.EOF. Always Close it.
	Stream = provider.StreamReader

	// Message is one conversation turn.
	Message = provider.Message
	// ContentPart is one element of a multimodal message body.
	ContentPart = provider.ContentPart
	// ImageURL addresses an image, either by https URL or data: URI.
	ImageURL = provider.ImageURL
	// InputAudio carries base64 audio input.
	InputAudio = provider.InputAudio
	// FileContent carries an uploaded-file reference or inline file bytes.
	FileContent = provider.FileContent
	// Choice is one completion alternative.
	Choice = provider.Choice
	// Usage reports token and capacity consumption.
	Usage = provider.Usage
	// PromptTokensDetails breaks down prompt token usage.
	PromptTokensDetails = provider.PromptTokensDetails
	// CompletionTokensDetails breaks down completion token usage.
	CompletionTokensDetails = provider.CompletionTokensDetails

	// Tool is a function tool definition.
	Tool = provider.Tool
	// ToolFunction describes a tool's name, description and JSON Schema.
	ToolFunction = provider.ToolFunction
	// ToolCall is a model-issued invocation of a tool.
	ToolCall = provider.ToolCall
	// ToolCallFunction is the callee and its JSON-encoded arguments.
	ToolCallFunction = provider.ToolCallFunction

	// ResponseFormat requests text, JSON object, or JSON schema output.
	ResponseFormat = provider.ResponseFormat
	// JSONSchemaSpec is a named JSON Schema for structured output.
	JSONSchemaSpec = provider.JSONSchemaSpec
	// ThinkingConfig controls extended thinking / reasoning.
	ThinkingConfig = provider.ThinkingConfig
	// StreamOptions controls streaming behavior (usage reporting).
	StreamOptions = provider.StreamOptions

	// EmbeddingRequest is the unified embeddings request.
	EmbeddingRequest = provider.EmbeddingRequest
	// EmbeddingResponse is the unified embeddings response.
	EmbeddingResponse = provider.EmbeddingResponse
	// EmbeddingItem is one embedding vector.
	EmbeddingItem = provider.EmbeddingItem

	// MediaAsset is a generated image / video / audio artifact.
	MediaAsset = provider.MediaAsset
	// ImageRequest asks for image generation.
	ImageRequest = provider.ImageGenerationRequest
	// ImageEditRequest asks for image editing with uploaded source images.
	ImageEditRequest = provider.ImageEditRequest
	// ImageResponse carries generated images.
	ImageResponse = provider.ImageGenerationResponse
	// UploadPart is one multipart file upload.
	UploadPart = provider.UploadPart

	// VideoRequest asks for video generation. Video is always asynchronous.
	VideoRequest = provider.VideoCreateRequest
	// VideoJob tracks an asynchronous video generation task.
	VideoJob = provider.VideoJob
	// ImageReference points at a guide image by file ID, URL, or base64.
	ImageReference = provider.ImageReference
	// FrameImage is a first/last frame guide for video generation.
	FrameImage = provider.FrameImage
	// InputReference is a style/character/object guide for video generation.
	InputReference = provider.InputReference
	// ErrorObject describes a terminal job failure.
	ErrorObject = provider.ErrorObject

	// RemoteModel is one entry from a provider's model catalog.
	RemoteModel = provider.RemoteModel
	// APIError is an error returned by an upstream vendor API.
	APIError = provider.ProviderError
)

// Video job states. Use IsTerminalVideoStatus to test for completion.
const (
	VideoStatusQueued          = provider.VideoStatusQueued
	VideoStatusInProgress      = provider.VideoStatusInProgress
	VideoStatusCompleted       = provider.VideoStatusCompleted
	VideoStatusFailed          = provider.VideoStatusFailed
	VideoStatusCancelRequested = provider.VideoStatusCancelRequested
	VideoStatusCancelled       = provider.VideoStatusCancelled
	VideoStatusExpired         = provider.VideoStatusExpired
)

// IsTerminalVideoStatus reports whether a video job has reached a final state
// and no longer needs polling.
func IsTerminalVideoStatus(s string) bool { return provider.IsTerminalVideoStatus(s) }

// Message roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)
