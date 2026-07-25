package provider

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"strings"
)

// CreateFormFileWithContentType is multipart.Writer.CreateFormFile with an
// explicit Content-Type header. The standard helper always uses
// application/octet-stream, which makes image edit adapters lose MIME fidelity.
func CreateFormFileWithContentType(mw *multipart.Writer, fieldName, filename, contentType string) (io.Writer, error) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
		escapeMultipartQuotes(fieldName), escapeMultipartQuotes(filename)))
	header.Set("Content-Type", contentType)
	return mw.CreatePart(header)
}

func escapeMultipartQuotes(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}
