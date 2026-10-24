package provider

import "errors"

// ErrorOrigin identifies where an adapter observed a failure. It is diagnostic
// metadata only: it does not change HTTP status, classification or replay safety.
type ErrorOrigin string

const (
	// ErrorOriginUnknown means the adapter has not established a source. An HTTP
	// status or a missing request ID alone cannot establish one.
	ErrorOriginUnknown ErrorOrigin = "unknown"
	// ErrorOriginLocal marks a failure detected before sending the request.
	ErrorOriginLocal ErrorOrigin = "local"
	// ErrorOriginUpstream marks an error received in an upstream HTTP response,
	// including an explicit error envelope inside a successful SSE connection.
	ErrorOriginUpstream ErrorOrigin = "upstream"
)

type errorWithOrigin struct {
	err    error
	origin ErrorOrigin
}

func (e *errorWithOrigin) Error() string            { return e.err.Error() }
func (e *errorWithOrigin) Unwrap() error            { return e.err }
func (e *errorWithOrigin) ErrorOrigin() ErrorOrigin { return e.origin }

// WithErrorOrigin records an established source without hiding errors.Is/As or
// provider metadata. Nil stays nil. Unknown or invalid origins leave err as is;
// an existing known origin is preserved, so repeated annotation is idempotent.
// Adapters must mark the source where they observe it, rather than infer it from
// a status code or the presence of response headers.
func WithErrorOrigin(err error, origin ErrorOrigin) error {
	if err == nil || (origin != ErrorOriginLocal && origin != ErrorOriginUpstream) {
		return err
	}
	if ErrorOriginOf(err) != ErrorOriginUnknown {
		return err
	}
	return &errorWithOrigin{err: err, origin: origin}
}

// ErrorOriginOf returns the source explicitly recorded by an adapter, or
// ErrorOriginUnknown for nil and unmarked errors. DeepSeek annotates local
// validation and upstream HTTP failures; the shared compat SSE reader annotates
// explicit error envelopes. Other paths may return unmarked errors. Wrapping an
// error preserves its origin.
func ErrorOriginOf(err error) ErrorOrigin {
	var marked interface {
		error
		ErrorOrigin() ErrorOrigin
	}
	if errors.As(err, &marked) {
		switch origin := marked.ErrorOrigin(); origin {
		case ErrorOriginLocal, ErrorOriginUpstream:
			return origin
		}
	}
	return ErrorOriginUnknown
}
