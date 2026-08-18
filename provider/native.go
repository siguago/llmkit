package provider

import (
	"context"
	"io"

	anthropicapi "github.com/siguago/llmkit/protocol/anthropic"
	"github.com/siguago/llmkit/protocol/openaibatch"
	"github.com/siguago/llmkit/protocol/openaifiles"
	"github.com/siguago/llmkit/protocol/responses"
)

// ResponsesCreator is implemented by providers that expose the OpenAI
// Responses create endpoint. It is intentionally separate from Provider: a
// third-party Chat Completions adapter must not grow a new required method just
// because llmkit learned another protocol.
type ResponsesCreator interface {
	CreateResponse(ctx context.Context, apiKey string, req *responses.CreateRequest) (*responses.Response, error)
}

// ResponsesStreamer is the streaming half of Responses creation. It is a
// separate capability because some relays implement only the JSON endpoint.
type ResponsesStreamer interface {
	CreateResponseStream(ctx context.Context, apiKey string, req *responses.CreateRequest) (responses.Stream, error)
}

// ResponsesRetriever retrieves a stored or background Response.
type ResponsesRetriever interface {
	RetrieveResponse(ctx context.Context, apiKey, responseID string, opts *responses.RetrieveOptions) (*responses.Response, error)
}

// ResponsesCanceller cancels a background Response.
type ResponsesCanceller interface {
	CancelResponse(ctx context.Context, apiKey, responseID string) (*responses.Response, error)
}

// ResponsesDeleter deletes a stored Response.
type ResponsesDeleter interface {
	DeleteResponse(ctx context.Context, apiKey, responseID string) (*responses.DeletedResponse, error)
}

// ResponsesInputItemLister lists the items that formed a Response's input.
type ResponsesInputItemLister interface {
	ListResponseInputItems(ctx context.Context, apiKey, responseID string, opts *responses.ListInputItemsOptions) (*responses.InputItemList, error)
}

// ResponsesTokenCounter calls the Responses input-token counting endpoint.
type ResponsesTokenCounter interface {
	CountResponseInputTokens(ctx context.Context, apiKey string, req *responses.TokenCountRequest) (*responses.TokenCountResponse, error)
}

// FileUploader is implemented by providers that expose the OpenAI Files
// upload endpoint (POST /v1/files, multipart). Like every native-surface
// capability it is opt-in per endpoint, so a relay that only proxies part of
// the resource can declare exactly what it has.
type FileUploader interface {
	UploadFile(ctx context.Context, apiKey string, req *openaifiles.UploadRequest) (*openaifiles.File, error)
}

// FileLister lists uploaded files (GET /v1/files).
type FileLister interface {
	ListFiles(ctx context.Context, apiKey string, req *openaifiles.ListRequest) (*openaifiles.FileList, error)
}

// FileRetriever retrieves one file's metadata (GET /v1/files/{id}).
type FileRetriever interface {
	RetrieveFile(ctx context.Context, apiKey, fileID string) (*openaifiles.File, error)
}

// FileDeleter deletes an uploaded file (DELETE /v1/files/{id}).
type FileDeleter interface {
	DeleteFile(ctx context.Context, apiKey, fileID string) (*openaifiles.DeletedFile, error)
}

// FileContentDownloader downloads a file's content (GET /v1/files/{id}/content).
// The returned body is a live network stream: the caller must Close it, and
// its lifetime is bounded by the request context, not by any client timeout.
type FileContentDownloader interface {
	DownloadFileContent(ctx context.Context, apiKey, fileID string) (io.ReadCloser, error)
}

// BatchCreator creates an OpenAI batch (POST /v1/batches) over a previously
// uploaded JSONL input file.
type BatchCreator interface {
	CreateBatch(ctx context.Context, apiKey string, req *openaibatch.CreateRequest) (*openaibatch.Batch, error)
}

// BatchRetriever retrieves one batch by ID; the polling primitive.
type BatchRetriever interface {
	RetrieveBatch(ctx context.Context, apiKey, batchID string) (*openaibatch.Batch, error)
}

// BatchLister lists batches (GET /v1/batches).
type BatchLister interface {
	ListBatches(ctx context.Context, apiKey string, req *openaibatch.ListRequest) (*openaibatch.BatchList, error)
}

// BatchCanceller cancels an in-progress batch. Cancellation is asynchronous:
// the batch stays "cancelling" for up to ten minutes and partial results
// still land in the output file.
type BatchCanceller interface {
	CancelBatch(ctx context.Context, apiKey, batchID string) (*openaibatch.Batch, error)
}

// AnthropicMessageBatchCreator creates an Anthropic Message Batch
// (POST /v1/messages/batches) with inline requests.
type AnthropicMessageBatchCreator interface {
	CreateAnthropicMessageBatch(ctx context.Context, apiKey string, req *anthropicapi.MessageBatchCreateRequest, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageBatch, error)
}

// AnthropicMessageBatchRetriever retrieves one Message Batch by ID; the
// idempotent polling primitive.
type AnthropicMessageBatchRetriever interface {
	RetrieveAnthropicMessageBatch(ctx context.Context, apiKey, batchID string, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageBatch, error)
}

// AnthropicMessageBatchLister lists Message Batches in the workspace.
type AnthropicMessageBatchLister interface {
	ListAnthropicMessageBatches(ctx context.Context, apiKey string, req *anthropicapi.MessageBatchListRequest, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageBatchList, error)
}

// AnthropicMessageBatchCanceller cancels a Message Batch. Non-interruptible
// in-flight requests may still complete.
type AnthropicMessageBatchCanceller interface {
	CancelAnthropicMessageBatch(ctx context.Context, apiKey, batchID string, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageBatch, error)
}

// AnthropicMessageBatchDeleter deletes a Message Batch that has ended.
type AnthropicMessageBatchDeleter interface {
	DeleteAnthropicMessageBatch(ctx context.Context, apiKey, batchID string, opts ...anthropicapi.RequestOption) (*anthropicapi.DeletedMessageBatch, error)
}

// AnthropicMessageBatchResultsReader streams the results JSONL of an ended
// Message Batch. The returned reader wraps a live network stream: the caller
// must Close it, and its lifetime is bounded by the request context.
type AnthropicMessageBatchResultsReader interface {
	ReadAnthropicMessageBatchResults(ctx context.Context, apiKey, batchID string, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageBatchResultsReader, error)
}

// AnthropicMessagesCreator exposes Anthropic's native Messages JSON response
// without converting it to Chat Completions first.
type AnthropicMessagesCreator interface {
	CreateAnthropicMessage(ctx context.Context, apiKey string, req *anthropicapi.MessageRequest, opts ...anthropicapi.RequestOption) (*anthropicapi.MessageResponse, error)
}

// AnthropicMessagesStreamer exposes Anthropic's native Messages event stream.
type AnthropicMessagesStreamer interface {
	CreateAnthropicMessageStream(ctx context.Context, apiKey string, req *anthropicapi.MessageRequest, opts ...anthropicapi.RequestOption) (anthropicapi.Stream, error)
}

// AnthropicTokenCounter calls Anthropic's server-side token counting endpoint.
type AnthropicTokenCounter interface {
	CountAnthropicMessageTokens(ctx context.Context, apiKey string, req *anthropicapi.TokenCountRequest, opts ...anthropicapi.RequestOption) (*anthropicapi.TokenCountResponse, error)
}
