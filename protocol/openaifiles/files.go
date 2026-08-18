// Package openaifiles contains the native DTOs for the OpenAI Files API
// (/v1/files): upload, listing, retrieval, deletion, and content download.
//
// These objects are produced by the upstream and only read by the SDK — they
// are never re-encoded and sent back (uploads travel as multipart form data,
// not JSON). The fidelity contract is therefore weaker than the round-trip
// guarantee of protocol/responses and protocol/anthropic: decoding never drops
// a modeled field, and RawJSON exposes the exact upstream bytes for anything
// this package does not model. Unknown enum values (a future purpose, say)
// pass through untouched.
//
// The package depends only on the standard library.
package openaifiles

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Known File.Purpose values. The set is open — upstream adds purposes over
// time — so none of these are validated locally; an unknown purpose is passed
// through and, if invalid, rejected by the upstream with its own error.
const (
	PurposeAssistants       = "assistants"
	PurposeAssistantsOutput = "assistants_output"
	PurposeBatch            = "batch"
	PurposeBatchOutput      = "batch_output"
	PurposeFineTune         = "fine-tune"
	PurposeFineTuneResults  = "fine-tune-results"
	PurposeVision           = "vision"
	PurposeUserData         = "user_data"
)

// ExpiresAfterAnchorCreatedAt is the only documented anchor for ExpiresAfter.
const ExpiresAfterAnchorCreatedAt = "created_at"

// ExpiresAfter is the optional expiration policy for an uploaded file,
// serialized as the multipart fields expires_after[anchor] and
// expires_after[seconds].
type ExpiresAfter struct {
	// Anchor names the timestamp the expiry counts from. The only documented
	// value is "created_at" (ExpiresAfterAnchorCreatedAt).
	Anchor string
	// Seconds until expiry, counted from the anchor.
	Seconds int64
}

// UploadRequest describes one file upload. Content is read exactly once while
// the request is being sent, which is why uploads are never automatically
// retried — reopen the source and call again to retry.
type UploadRequest struct {
	// Filename is required: it names the file part in the multipart form and
	// becomes File.Filename upstream.
	Filename string
	// Purpose declares the intended use (see the Purpose* constants). The
	// upstream requires it; it is forwarded as-is without local validation.
	Purpose string
	// Content is the file payload. It is streamed into the request body, not
	// buffered whole (files can be up to 512 MB).
	Content io.Reader
	// ContentType of the file part. Empty means application/octet-stream.
	ContentType string
	// ExpiresAfter optionally asks the upstream to expire the file.
	ExpiresAfter *ExpiresAfter
}

// Validate reports structural problems that would make the request impossible
// to send. Semantic validation (purpose values, size limits) is the
// upstream's job.
func (r *UploadRequest) Validate() error {
	if r == nil {
		return fmt.Errorf("openaifiles: nil upload request")
	}
	if r.Filename == "" {
		return fmt.Errorf("openaifiles: Filename is required to build the multipart file part")
	}
	if r.Content == nil {
		return fmt.Errorf("openaifiles: Content is required")
	}
	return nil
}

// ListRequest tunes GET /v1/files. The zero value lists with the upstream's
// own defaults (limit 10000, descending by created_at); the SDK does not
// invent different ones.
type ListRequest struct {
	// After is a cursor: the object ID to continue the listing after.
	After string
	// Limit is the page size, 1–10000. Zero omits the parameter.
	Limit int
	// Order sorts by created_at: "asc" or "desc". Empty omits the parameter.
	Order string
	// Purpose filters to files with the given purpose. Empty omits it.
	Purpose string
}

// File is the upstream file object.
type File struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	// ExpiresAt is a Unix timestamp; zero means the upstream sent none.
	ExpiresAt int64  `json:"expires_at,omitempty"`
	Filename  string `json:"filename"`
	Purpose   string `json:"purpose"`
	// Status and StatusDetails are deprecated upstream but still decoded so
	// older deployments and relays that send them stay readable.
	Status        string `json:"status,omitempty"`
	StatusDetails string `json:"status_details,omitempty"`

	// RequestID is the upstream request identifier from the response headers,
	// filled by the transport rather than the wire body.
	RequestID string `json:"-"`

	raw json.RawMessage
}

// UnmarshalJSON decodes the object and keeps the exact upstream bytes
// retrievable via RawJSON. A top-level JSON null is rejected instead of
// producing a zero-value "success".
func (f *File) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		return fmt.Errorf("openaifiles: file object cannot be null")
	}
	type plain File
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*f = File(p)
	f.raw = append(json.RawMessage(nil), data...)
	return nil
}

// RawJSON returns the exact bytes this object was decoded from, including any
// fields this package does not model. Nil when the object was not produced by
// decoding.
func (f *File) RawJSON() json.RawMessage { return f.raw }

// FileList is one page of GET /v1/files.
type FileList struct {
	Object  string `json:"object"`
	Data    []File `json:"data"`
	FirstID string `json:"first_id"`
	LastID  string `json:"last_id"`
	HasMore bool   `json:"has_more"`

	// RequestID is filled by the transport from the response headers.
	RequestID string `json:"-"`

	raw json.RawMessage
}

// UnmarshalJSON decodes the page and keeps the raw bytes; top-level null is
// rejected.
func (l *FileList) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		return fmt.Errorf("openaifiles: file list cannot be null")
	}
	type plain FileList
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*l = FileList(p)
	l.raw = append(json.RawMessage(nil), data...)
	return nil
}

// RawJSON returns the exact bytes this page was decoded from.
func (l *FileList) RawJSON() json.RawMessage { return l.raw }

// DeletedFile is the DELETE /v1/files/{id} acknowledgement.
type DeletedFile struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`

	// RequestID is filled by the transport from the response headers.
	RequestID string `json:"-"`

	raw json.RawMessage
}

// UnmarshalJSON decodes the acknowledgement and keeps the raw bytes;
// top-level null is rejected.
func (d *DeletedFile) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		return fmt.Errorf("openaifiles: deletion object cannot be null")
	}
	type plain DeletedFile
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*d = DeletedFile(p)
	d.raw = append(json.RawMessage(nil), data...)
	return nil
}

// RawJSON returns the exact bytes this object was decoded from.
func (d *DeletedFile) RawJSON() json.RawMessage { return d.raw }

// isJSONNull distinguishes an explicit top-level null from real content.
func isJSONNull(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}
