package openaibatch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// DefaultMaxLineBytes is the zero-config ceiling on one JSONL line in a batch
// output or error file. It matches the Responses SSE default: an image
// generation batched through /v1/images/generations returns base64 image data
// on a single line.
const DefaultMaxLineBytes = 32 << 20

// InputItem is one line of a batch input file.
type InputItem struct {
	// CustomID must be unique within the batch; it is the only way to map
	// results (which arrive in arbitrary order) back to requests. Uniqueness
	// is enforced by the upstream, not locally.
	CustomID string `json:"custom_id"`
	// Method is the HTTP method; the upstream currently accepts POST only.
	Method string `json:"method"`
	// URL is the endpoint path, matching the batch's Endpoint (see the
	// Endpoint* constants).
	URL string `json:"url"`
	// Body is the endpoint-specific request body, verbatim. Compose it with
	// the DTO of the endpoint being batched; this package does not re-model
	// those schemas.
	Body json.RawMessage `json:"body"`
}

// NewInputItem builds one input line, marshaling body with encoding/json and
// defaulting Method to POST.
func NewInputItem(customID, url string, body any) (InputItem, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return InputItem{}, fmt.Errorf("openaibatch: encode body for %q: %w", customID, err)
	}
	return InputItem{CustomID: customID, Method: http.MethodPost, URL: url, Body: encoded}, nil
}

// EncodeInput writes items as JSONL: one JSON object per line, trailing
// newline after each. The result is what an input file with purpose "batch"
// must contain. Limits (50,000 lines, 200 MB, one model per file) are the
// upstream's and are not enforced here.
func EncodeInput(w io.Writer, items ...InputItem) error {
	enc := json.NewEncoder(w)
	for i := range items {
		if err := enc.Encode(&items[i]); err != nil {
			return fmt.Errorf("openaibatch: encode input line %d: %w", i+1, err)
		}
	}
	return nil
}

// OutputResponse is the successful half of an output line.
type OutputResponse struct {
	StatusCode int    `json:"status_code"`
	RequestID  string `json:"request_id"`
	// Body is the endpoint-specific response, verbatim. Decode it with the
	// DTO of the endpoint that was batched.
	Body json.RawMessage `json:"body"`
}

// OutputError is the failed half of an output line.
type OutputError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// OutputItem is one line of a batch output or error file. Lines arrive in
// arbitrary order; match them to requests by CustomID.
type OutputItem struct {
	ID       string          `json:"id"`
	CustomID string          `json:"custom_id"`
	Response *OutputResponse `json:"response"`
	Error    *OutputError    `json:"error"`

	raw json.RawMessage
}

// UnmarshalJSON decodes the line and keeps its raw bytes; top-level null is
// rejected.
func (o *OutputItem) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		return fmt.Errorf("openaibatch: output line cannot be null")
	}
	type plain OutputItem
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*o = OutputItem(p)
	o.raw = append(json.RawMessage(nil), data...)
	return nil
}

// RawJSON returns the exact bytes this line was decoded from.
func (o *OutputItem) RawJSON() json.RawMessage { return o.raw }

// OutputReader decodes a batch output or error file line by line without
// buffering the whole file. It is always strict: these files are complete
// artifacts, so a malformed line is data corruption, not transport jitter —
// it fails the read instead of being skipped.
type OutputReader struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
	maxLine int
	line    int
	err     error
}

// NewOutputReader wraps the content stream of an output or error file
// (typically from DownloadFileContent). maxLineBytes caps one line;
// non-positive means DefaultMaxLineBytes. Close the reader when done — it
// closes the underlying body.
func NewOutputReader(body io.ReadCloser, maxLineBytes int64) *OutputReader {
	if maxLineBytes <= 0 {
		maxLineBytes = DefaultMaxLineBytes
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
	return &OutputReader{body: body, scanner: scanner, maxLine: int(maxLineBytes)}
}

// Next returns the next line, io.EOF at the end of the file. Blank lines
// (including a trailing newline) are skipped; any other undecodable line
// fails with its line number.
func (r *OutputReader) Next() (*OutputItem, error) {
	if r.err != nil {
		return nil, r.err
	}
	for r.scanner.Scan() {
		r.line++
		lineBytes := bytes.TrimSpace(r.scanner.Bytes())
		if len(lineBytes) == 0 {
			continue
		}
		var item OutputItem
		if err := json.Unmarshal(lineBytes, &item); err != nil {
			r.err = fmt.Errorf("openaibatch: output line %d: %w", r.line, err)
			return nil, r.err
		}
		return &item, nil
	}
	if err := r.scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			r.err = fmt.Errorf("openaibatch: output line %d exceeds the %d-byte line limit: %w", r.line+1, r.maxLine, err)
		} else {
			r.err = fmt.Errorf("openaibatch: read output line %d: %w", r.line+1, err)
		}
		return nil, r.err
	}
	r.err = io.EOF
	return nil, io.EOF
}

// Close closes the underlying content stream.
func (r *OutputReader) Close() error { return r.body.Close() }
