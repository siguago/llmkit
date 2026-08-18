// Package openaibatch contains the native DTOs for the OpenAI Batch API
// (/v1/batches) and the JSONL input/output line formats that batch input
// files and result files use.
//
// Like protocol/openaifiles, these are resources the upstream produces and
// the SDK only reads: decoding never drops a modeled field, RawJSON exposes
// the exact upstream bytes, unknown enum values pass through, and a top-level
// JSON null is rejected. There is no decode→encode round-trip promise.
//
// The per-line request Body stays a json.RawMessage on purpose: a batch line
// can target any of the supported endpoints (/v1/responses,
// /v1/chat/completions, …), and this package does not re-model those request
// schemas — compose the body with the DTO of the endpoint you are batching.
//
// The package depends only on the standard library.
package openaibatch

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Batch statuses, in rough lifecycle order.
const (
	StatusValidating = "validating"
	StatusFailed     = "failed"
	StatusInProgress = "in_progress"
	StatusFinalizing = "finalizing"
	StatusCompleted  = "completed"
	StatusExpired    = "expired"
	StatusCancelling = "cancelling"
	StatusCancelled  = "cancelled"
)

// Endpoints a batch can target, current as of 2026-08-18. The set is open —
// the upstream adds endpoints over time — so none are validated locally.
const (
	EndpointResponses        = "/v1/responses"
	EndpointChatCompletions  = "/v1/chat/completions"
	EndpointEmbeddings       = "/v1/embeddings"
	EndpointCompletions      = "/v1/completions"
	EndpointModerations      = "/v1/moderations"
	EndpointImageGenerations = "/v1/images/generations"
	EndpointImageEdits       = "/v1/images/edits"
	EndpointVideos           = "/v1/videos"
)

// CompletionWindow24h is the only completion window the upstream currently
// accepts. It is still an explicit request field: the SDK does not write
// upstream defaults on the caller's behalf.
const CompletionWindow24h = "24h"

// OutputExpiresAfter is the expiration policy for the generated output and
// error files.
type OutputExpiresAfter struct {
	// Anchor names the timestamp the expiry counts from; "created_at" is the
	// documented value.
	Anchor  string `json:"anchor"`
	Seconds int64  `json:"seconds"`
}

// CreateRequest is the POST /v1/batches body.
type CreateRequest struct {
	// InputFileID references a JSONL file uploaded with purpose "batch".
	// One input file: at most 50,000 requests, at most 200 MB, one model.
	InputFileID string `json:"input_file_id"`
	// Endpoint is the API path every line in the batch targets (see the
	// Endpoint* constants). Forwarded without local validation.
	Endpoint string `json:"endpoint"`
	// CompletionWindow is required by the upstream; currently only "24h".
	CompletionWindow string `json:"completion_window"`
	// Metadata is up to 16 key-value pairs attached to the batch.
	Metadata map[string]string `json:"metadata,omitempty"`
	// OutputExpiresAfter optionally expires the output/error files.
	OutputExpiresAfter *OutputExpiresAfter `json:"output_expires_after,omitempty"`
}

// Validate reports structural problems that would make the request
// unsendable. Semantic validation (endpoint set, window values, file
// suitability) is the upstream's job.
func (r *CreateRequest) Validate() error {
	if r == nil {
		return fmt.Errorf("openaibatch: nil create request")
	}
	if r.InputFileID == "" {
		return fmt.Errorf("openaibatch: InputFileID is required")
	}
	if r.Endpoint == "" {
		return fmt.Errorf("openaibatch: Endpoint is required")
	}
	return nil
}

// ListRequest tunes GET /v1/batches. Zero value = upstream defaults
// (limit 20, newest first).
type ListRequest struct {
	// After is a cursor: the batch ID to continue the listing after.
	After string
	// Limit is the page size, 1–100. Zero omits the parameter.
	Limit int
}

// RequestCounts tallies the requests inside a batch.
type RequestCounts struct {
	Total     int64 `json:"total"`
	Completed int64 `json:"completed"`
	Failed    int64 `json:"failed"`
}

// BatchError is one entry of Batch.Errors.
type BatchError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// Line is the input-file line the error refers to; nil when the error is
	// not tied to a line (the upstream sends null).
	Line  *int64 `json:"line,omitempty"`
	Param string `json:"param,omitempty"`
}

// BatchErrors is the container object of Batch.Errors.
type BatchErrors struct {
	Object string       `json:"object,omitempty"`
	Data   []BatchError `json:"data,omitempty"`
}

// UsageTokensDetails breaks down input tokens.
type UsageTokensDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

// UsageOutputTokensDetails breaks down output tokens.
type UsageOutputTokensDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

// Usage is aggregate token usage for the whole batch. The upstream only
// populates it on batches created after 2025-09-07; a nil Usage on the Batch
// means "not provided", never "zero tokens".
type Usage struct {
	InputTokens         int64                     `json:"input_tokens"`
	InputTokensDetails  *UsageTokensDetails       `json:"input_tokens_details,omitempty"`
	OutputTokens        int64                     `json:"output_tokens"`
	OutputTokensDetails *UsageOutputTokensDetails `json:"output_tokens_details,omitempty"`
	TotalTokens         int64                     `json:"total_tokens"`
}

// Batch is the upstream batch object.
type Batch struct {
	ID               string `json:"id"`
	Object           string `json:"object"`
	Endpoint         string `json:"endpoint"`
	InputFileID      string `json:"input_file_id"`
	CompletionWindow string `json:"completion_window"`
	// Status is one of the Status* constants; unknown future statuses pass
	// through as-is.
	Status string `json:"status"`
	// OutputFileID / ErrorFileID are set once results exist; download them
	// with the Files content endpoint and decode with OutputReader. The
	// upstream deletes output files 30 days after completion.
	OutputFileID string       `json:"output_file_id,omitempty"`
	ErrorFileID  string       `json:"error_file_id,omitempty"`
	Errors       *BatchErrors `json:"errors,omitempty"`
	// Model is the model the batch ran against (optional upstream field).
	Model string `json:"model,omitempty"`

	CreatedAt    int64 `json:"created_at"`
	InProgressAt int64 `json:"in_progress_at,omitempty"`
	ExpiresAt    int64 `json:"expires_at,omitempty"`
	FinalizingAt int64 `json:"finalizing_at,omitempty"`
	CompletedAt  int64 `json:"completed_at,omitempty"`
	FailedAt     int64 `json:"failed_at,omitempty"`
	ExpiredAt    int64 `json:"expired_at,omitempty"`
	CancellingAt int64 `json:"cancelling_at,omitempty"`
	CancelledAt  int64 `json:"cancelled_at,omitempty"`

	RequestCounts *RequestCounts    `json:"request_counts,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	// Usage is nil when the upstream did not provide it (batches created
	// before 2025-09-07); do not read nil as zero.
	Usage *Usage `json:"usage,omitempty"`

	// RequestID is the upstream request identifier from the response headers,
	// filled by the transport rather than the wire body.
	RequestID string `json:"-"`

	raw json.RawMessage
}

// IsTerminal reports whether the batch has reached a state it cannot leave.
func (b *Batch) IsTerminal() bool {
	switch b.Status {
	case StatusCompleted, StatusFailed, StatusExpired, StatusCancelled:
		return true
	default:
		return false
	}
}

// UnmarshalJSON decodes the batch and keeps the exact upstream bytes
// retrievable via RawJSON; a top-level null is rejected.
func (b *Batch) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		return fmt.Errorf("openaibatch: batch object cannot be null")
	}
	type plain Batch
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*b = Batch(p)
	b.raw = append(json.RawMessage(nil), data...)
	return nil
}

// RawJSON returns the exact bytes this object was decoded from.
func (b *Batch) RawJSON() json.RawMessage { return b.raw }

// BatchList is one page of GET /v1/batches.
type BatchList struct {
	Object  string  `json:"object"`
	Data    []Batch `json:"data"`
	FirstID string  `json:"first_id,omitempty"`
	LastID  string  `json:"last_id,omitempty"`
	HasMore bool    `json:"has_more"`

	// RequestID is filled by the transport from the response headers.
	RequestID string `json:"-"`

	raw json.RawMessage
}

// UnmarshalJSON decodes the page and keeps the raw bytes; top-level null is
// rejected.
func (l *BatchList) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		return fmt.Errorf("openaibatch: batch list cannot be null")
	}
	type plain BatchList
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*l = BatchList(p)
	l.raw = append(json.RawMessage(nil), data...)
	return nil
}

// RawJSON returns the exact bytes this page was decoded from.
func (l *BatchList) RawJSON() json.RawMessage { return l.raw }

// isJSONNull distinguishes an explicit top-level null from real content.
func isJSONNull(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}
