package llmkit

import "encoding/base64"

// base64Encode is a tiny indirection so DataURI reads cleanly and the encoding
// choice (standard, with padding — what data: URIs require) lives in one place.
func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
