package anthropic

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Message Batches (/v1/messages/batches) native DTOs.
//
// Unlike the Messages create/stream types in this package, batch resources
// are produced by the upstream and only read by the SDK — they are never
// re-encoded and sent back. Their fidelity contract is therefore the weaker
// resource contract shared with protocol/openaifiles and protocol/openaibatch
// (see ADR 0002): decoding never drops a modeled field, RawJSON exposes the
// exact upstream bytes, unknown enum values and unknown result types pass
// through, and a top-level JSON null is rejected. Succeeded results embed a
// full MessageResponse, which keeps its own strict official-schema
// validation.

// Message Batch processing statuses.
const (
	BatchProcessingStatusInProgress = "in_progress"
	BatchProcessingStatusCanceling  = "canceling"
	BatchProcessingStatusEnded      = "ended"
)

// Message Batch individual result types.
const (
	BatchResultTypeSucceeded = "succeeded"
	BatchResultTypeErrored   = "errored"
	BatchResultTypeCanceled  = "canceled"
	BatchResultTypeExpired   = "expired"
)

// DefaultBatchResultsMaxLineBytes is the zero-config ceiling on one results
// JSONL line. A succeeded line embeds a complete Message, which long-output
// models can grow to megabytes; 32 MiB matches the Responses SSE default.
const DefaultBatchResultsMaxLineBytes = 32 << 20

// MessageBatchRequestItem is one request inside a batch create call.
type MessageBatchRequestItem struct {
	// CustomID must be unique within the batch; results arrive in arbitrary
	// order and CustomID is the only join key. Uniqueness is enforced by the
	// upstream, not locally.
	CustomID string `json:"custom_id"`
	// Params are ordinary Messages API creation parameters. Inside a batch
	// the upstream additionally requires max_tokens >= 1 and rejects
	// stream:true; both are its errors to report.
	Params *MessageRequest `json:"params"`
}

// MessageBatchCreateRequest is the POST /v1/messages/batches body. One batch:
// at most 100,000 requests or 256 MB, whichever comes first.
type MessageBatchCreateRequest struct {
	Requests []MessageBatchRequestItem `json:"requests"`
}

// Validate reports structural problems that would make the request
// unsendable; everything semantic is the upstream's job.
func (r *MessageBatchCreateRequest) Validate() error {
	if r == nil {
		return fmt.Errorf("anthropic: nil message batch create request")
	}
	if len(r.Requests) == 0 {
		return fmt.Errorf("anthropic: message batch needs at least one request")
	}
	for i := range r.Requests {
		if r.Requests[i].Params == nil {
			return fmt.Errorf("anthropic: message batch request %d has nil params", i)
		}
	}
	return nil
}

// MessageBatchListRequest tunes GET /v1/messages/batches. Zero value =
// upstream defaults (limit 20, newest first).
type MessageBatchListRequest struct {
	// AfterID / BeforeID are pagination cursors.
	AfterID  string
	BeforeID string
	// Limit is the page size, 1–1000. Zero omits the parameter.
	Limit int
}

// MessageBatchRequestCounts tallies the requests in a batch by state. The
// non-processing buckets stay zero until the whole batch ends; the sum always
// equals the number of requests in the batch.
type MessageBatchRequestCounts struct {
	Processing int64 `json:"processing"`
	Succeeded  int64 `json:"succeeded"`
	Errored    int64 `json:"errored"`
	Canceled   int64 `json:"canceled"`
	Expired    int64 `json:"expired"`
}

// MessageBatch is the upstream batch object. Timestamps are RFC 3339 strings
// exactly as the upstream sends them; a nullable timestamp that the upstream
// sent as null or omitted decodes to "".
type MessageBatch struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// ProcessingStatus is one of the BatchProcessingStatus* constants;
	// unknown future statuses pass through.
	ProcessingStatus string                    `json:"processing_status"`
	RequestCounts    MessageBatchRequestCounts `json:"request_counts"`
	CreatedAt        string                    `json:"created_at"`
	// ExpiresAt is 24 hours after creation; processing that has not finished
	// by then expires (expired requests are not billed).
	ExpiresAt         string `json:"expires_at"`
	EndedAt           string `json:"ended_at,omitempty"`
	ArchivedAt        string `json:"archived_at,omitempty"`
	CancelInitiatedAt string `json:"cancel_initiated_at,omitempty"`
	// ResultsURL is non-empty once results are available (they stay
	// downloadable for 29 days). The SDK treats it as an availability signal
	// only and always requests the documented per-batch results path against
	// its configured base URL — a URL from the response body must not decide
	// where an outbound request goes.
	ResultsURL string `json:"results_url,omitempty"`

	// RequestID is the upstream request identifier from the response headers,
	// filled by the transport rather than the wire body.
	RequestID string `json:"-"`

	raw json.RawMessage
}

// HasEnded reports whether processing has finished (successfully or not).
func (b *MessageBatch) HasEnded() bool {
	return b.ProcessingStatus == BatchProcessingStatusEnded
}

// UnmarshalJSON decodes the batch and keeps the exact upstream bytes; a
// top-level null is rejected.
func (b *MessageBatch) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		return fmt.Errorf("anthropic: message batch cannot be null")
	}
	type plain MessageBatch
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*b = MessageBatch(p)
	b.raw = append(json.RawMessage(nil), data...)
	return nil
}

// RawJSON returns the exact bytes this object was decoded from.
func (b *MessageBatch) RawJSON() json.RawMessage { return b.raw }

// MessageBatchList is one page of GET /v1/messages/batches.
type MessageBatchList struct {
	Data    []MessageBatch `json:"data"`
	FirstID string         `json:"first_id,omitempty"`
	LastID  string         `json:"last_id,omitempty"`
	HasMore bool           `json:"has_more"`

	// RequestID is filled by the transport from the response headers.
	RequestID string `json:"-"`

	raw json.RawMessage
}

// UnmarshalJSON decodes the page and keeps the raw bytes; top-level null is
// rejected.
func (l *MessageBatchList) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		return fmt.Errorf("anthropic: message batch list cannot be null")
	}
	type plain MessageBatchList
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*l = MessageBatchList(p)
	l.raw = append(json.RawMessage(nil), data...)
	return nil
}

// RawJSON returns the exact bytes this page was decoded from.
func (l *MessageBatchList) RawJSON() json.RawMessage { return l.raw }

// DeletedMessageBatch is the DELETE acknowledgement. Batches can only be
// deleted after they end; cancel an in-progress batch first.
type DeletedMessageBatch struct {
	ID   string `json:"id"`
	Type string `json:"type"`

	// RequestID is filled by the transport from the response headers.
	RequestID string `json:"-"`

	raw json.RawMessage
}

// UnmarshalJSON decodes the acknowledgement and keeps the raw bytes;
// top-level null is rejected.
func (d *DeletedMessageBatch) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		return fmt.Errorf("anthropic: deleted message batch cannot be null")
	}
	type plain DeletedMessageBatch
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*d = DeletedMessageBatch(p)
	d.raw = append(json.RawMessage(nil), data...)
	return nil
}

// RawJSON returns the exact bytes this object was decoded from.
func (d *DeletedMessageBatch) RawJSON() json.RawMessage { return d.raw }

// MessageBatchErrorResponse is the error envelope inside an errored result:
// {"type":"error","error":{type,message},"request_id":...}. It is data about
// one batched request, not a call error — reaching it means the batch API
// call itself succeeded.
type MessageBatchErrorResponse struct {
	Type      string   `json:"type"`
	Error     APIError `json:"error"`
	RequestID string   `json:"request_id,omitempty"`

	raw json.RawMessage
}

// UnmarshalJSON decodes the envelope and keeps the raw bytes; top-level null
// is rejected.
func (e *MessageBatchErrorResponse) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		return fmt.Errorf("anthropic: batch error response cannot be null")
	}
	type plain MessageBatchErrorResponse
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*e = MessageBatchErrorResponse(p)
	e.raw = append(json.RawMessage(nil), data...)
	return nil
}

// RawJSON returns the exact bytes this envelope was decoded from.
func (e *MessageBatchErrorResponse) RawJSON() json.RawMessage { return e.raw }

// MessageBatchResult is the tagged union inside one results line. Known types
// populate Message or Error; an unknown future type keeps Type and Raw and is
// not an error.
type MessageBatchResult struct {
	Type string
	// Message is set for "succeeded". It passes MessageResponse's strict
	// official-schema validation like any other native Message.
	Message *MessageResponse
	// Error is set for "errored".
	Error *MessageBatchErrorResponse
	// Raw always holds the full result object bytes.
	Raw json.RawMessage
}

// UnmarshalJSON decodes the union by its "type" discriminator.
func (r *MessageBatchResult) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		return fmt.Errorf("anthropic: batch result cannot be null")
	}
	var head struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	if head.Type == "" {
		return fmt.Errorf("anthropic: batch result is missing its type discriminator")
	}
	out := MessageBatchResult{Type: head.Type, Raw: append(json.RawMessage(nil), data...)}
	switch head.Type {
	case BatchResultTypeSucceeded:
		if len(head.Message) == 0 || isJSONNull(head.Message) {
			return fmt.Errorf("anthropic: succeeded batch result is missing its message")
		}
		var message MessageResponse
		if err := json.Unmarshal(head.Message, &message); err != nil {
			return err
		}
		out.Message = &message
	case BatchResultTypeErrored:
		if len(head.Error) == 0 || isJSONNull(head.Error) {
			return fmt.Errorf("anthropic: errored batch result is missing its error")
		}
		var envelope MessageBatchErrorResponse
		if err := json.Unmarshal(head.Error, &envelope); err != nil {
			return err
		}
		out.Error = &envelope
	case BatchResultTypeCanceled, BatchResultTypeExpired:
		// Type alone carries the outcome.
	default:
		// Unknown future result type: preserved in Type and Raw, never
		// skipped, never an error.
	}
	*r = out
	return nil
}

// MessageBatchIndividualResult is one line of the results file. Lines arrive
// in arbitrary order; match them to requests by CustomID.
type MessageBatchIndividualResult struct {
	CustomID string             `json:"custom_id"`
	Result   MessageBatchResult `json:"result"`

	raw json.RawMessage
}

// UnmarshalJSON decodes the line and keeps its raw bytes. Both members are
// required by the official schema and both are enforced: custom_id is the
// only key that joins a result back to its request, and result carries the
// outcome. A line missing either is corrupt data, not a zero-value success —
// the same stance MessageResponse takes on its own required fields.
func (r *MessageBatchIndividualResult) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		return fmt.Errorf("anthropic: batch results line cannot be null")
	}
	type plain MessageBatchIndividualResult
	var probe struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if len(probe.Result) == 0 || isJSONNull(probe.Result) {
		return fmt.Errorf("%w: batch results line field %q is required", ErrInvalidWire, "result")
	}
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if p.CustomID == "" {
		return fmt.Errorf("%w: batch results line field %q must not be empty", ErrInvalidWire, "custom_id")
	}
	*r = MessageBatchIndividualResult(p)
	r.raw = append(json.RawMessage(nil), data...)
	return nil
}

// RawJSON returns the exact bytes this line was decoded from.
func (r *MessageBatchIndividualResult) RawJSON() json.RawMessage { return r.raw }

// MessageBatchResultsReader decodes the results JSONL stream line by line
// without buffering the whole file. It is always strict: the results file is
// a complete artifact, so a malformed line is data corruption and fails the
// read instead of being skipped.
type MessageBatchResultsReader struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
	maxLine int
	line    int
	err     error
}

// NewMessageBatchResultsReader wraps the results content stream.
// maxLineBytes caps one line; non-positive means
// DefaultBatchResultsMaxLineBytes. Close the reader when done — it closes the
// underlying body.
func NewMessageBatchResultsReader(body io.ReadCloser, maxLineBytes int64) *MessageBatchResultsReader {
	if maxLineBytes <= 0 {
		maxLineBytes = DefaultBatchResultsMaxLineBytes
	}
	scanner := bufio.NewScanner(body)
	// The effective scanner cap is max(maxLineBytes, cap(initial)), so the
	// initial buffer must never exceed the requested line limit or the limit
	// silently stops binding.
	initial := 64 << 10
	if int64(initial) > maxLineBytes {
		initial = int(maxLineBytes)
	}
	scanner.Buffer(make([]byte, initial), int(maxLineBytes))
	return &MessageBatchResultsReader{body: body, scanner: scanner, maxLine: int(maxLineBytes)}
}

// Next returns the next result line, io.EOF at the end. Blank lines are
// skipped; any other undecodable line fails with its line number.
func (r *MessageBatchResultsReader) Next() (*MessageBatchIndividualResult, error) {
	if r.err != nil {
		return nil, r.err
	}
	for r.scanner.Scan() {
		r.line++
		lineBytes := bytes.TrimSpace(r.scanner.Bytes())
		if len(lineBytes) == 0 {
			continue
		}
		var result MessageBatchIndividualResult
		if err := json.Unmarshal(lineBytes, &result); err != nil {
			r.err = fmt.Errorf("anthropic: batch results line %d: %w", r.line, err)
			return nil, r.err
		}
		return &result, nil
	}
	if err := r.scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			r.err = fmt.Errorf("anthropic: batch results line %d exceeds the %d-byte line limit: %w", r.line+1, r.maxLine, err)
		} else {
			r.err = fmt.Errorf("anthropic: read batch results line %d: %w", r.line+1, err)
		}
		return nil, r.err
	}
	r.err = io.EOF
	return nil, io.EOF
}

// Close closes the underlying content stream.
func (r *MessageBatchResultsReader) Close() error { return r.body.Close() }
