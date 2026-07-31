// Package responses defines the wire-level data model for OpenAI's Responses
// API. It intentionally contains no HTTP transport and depends only on the Go
// standard library.
//
// Known union variants decode into editable typed fields and retain unmodeled
// object members in ExtraFields. Unknown event, item, content, and tool variants
// keep their complete original JSON in Raw so new API variants remain usable
// before this package learns their schema. Use the constructors in this package
// for programmatically-created values.
package responses
