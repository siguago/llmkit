package llmkit

import (
	"context"
	"fmt"
	"io"

	"github.com/siguago/llmkit/protocol/openaifiles"
	"github.com/siguago/llmkit/provider"
)

// UploadFile uploads a file via the OpenAI Files API (POST /v1/files). The
// request Content is streamed exactly once, so this call is never
// automatically retried — a half-sent reader cannot be replayed. To retry,
// reopen the source and call again.
func (c *Client) UploadFile(ctx context.Context, req *openaifiles.UploadRequest) (*openaifiles.File, error) {
	if req == nil {
		return nil, fmt.Errorf("llmkit: nil file upload request")
	}
	uploader, ok := c.provider.(provider.FileUploader)
	if !ok {
		return nil, unsupportedf(c.name, "file upload")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	file, err := uploader.UploadFile(ctx, c.cfg.apiKey, req)
	return file, translateUnsupported(err)
}

// ListFiles lists uploaded files. A nil request uses the upstream's own
// defaults (limit 10000, newest first).
func (c *Client) ListFiles(ctx context.Context, req *openaifiles.ListRequest) (*openaifiles.FileList, error) {
	lister, ok := c.provider.(provider.FileLister)
	if !ok {
		return nil, unsupportedf(c.name, "file listing")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	list, err := doValue(ctx, c.cfg.retry, func() (*openaifiles.FileList, error) {
		return lister.ListFiles(ctx, c.cfg.apiKey, req)
	})
	return list, translateUnsupported(err)
}

// RetrieveFile retrieves one file's metadata by ID.
func (c *Client) RetrieveFile(ctx context.Context, fileID string) (*openaifiles.File, error) {
	retriever, ok := c.provider.(provider.FileRetriever)
	if !ok {
		return nil, unsupportedf(c.name, "file retrieval")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	file, err := doValue(ctx, c.cfg.retry, func() (*openaifiles.File, error) {
		return retriever.RetrieveFile(ctx, c.cfg.apiKey, fileID)
	})
	return file, translateUnsupported(err)
}

// DeleteFile deletes an uploaded file. It is not automatically retried
// because deletion changes resource state.
func (c *Client) DeleteFile(ctx context.Context, fileID string) (*openaifiles.DeletedFile, error) {
	deleter, ok := c.provider.(provider.FileDeleter)
	if !ok {
		return nil, unsupportedf(c.name, "file deletion")
	}
	ctx, cancel := c.prepare(ctx)
	defer cancel()
	deleted, err := deleter.DeleteFile(ctx, c.cfg.apiKey, fileID)
	return deleted, translateUnsupported(err)
}

// DownloadFileContent downloads a file's content as a stream. Always Close
// the returned reader. Retries cover only the request handshake; once a body
// is returned, a read error surfaces as-is — there is no automatic resume.
// WithTimeout does not bound the download because the body outlives the
// method call; put the desired lifetime on ctx.
func (c *Client) DownloadFileContent(ctx context.Context, fileID string) (io.ReadCloser, error) {
	downloader, ok := c.provider.(provider.FileContentDownloader)
	if !ok {
		return nil, unsupportedf(c.name, "file content download")
	}
	ctx = c.decorate(ctx)
	body, err := doValue(ctx, c.cfg.retry, func() (io.ReadCloser, error) {
		return downloader.DownloadFileContent(ctx, c.cfg.apiKey, fileID)
	})
	return body, translateUnsupported(err)
}

// SupportsFileUpload reports whether the OpenAI Files upload endpoint is
// available. The other file operations are separately probeable because
// relays often implement only part of the resource.
func (c *Client) SupportsFileUpload() bool {
	_, ok := c.provider.(provider.FileUploader)
	return ok
}

// SupportsFileListing reports whether uploaded files can be listed.
func (c *Client) SupportsFileListing() bool {
	_, ok := c.provider.(provider.FileLister)
	return ok
}

// SupportsFileRetrieval reports whether file metadata can be retrieved.
func (c *Client) SupportsFileRetrieval() bool {
	_, ok := c.provider.(provider.FileRetriever)
	return ok
}

// SupportsFileDeletion reports whether uploaded files can be deleted.
func (c *Client) SupportsFileDeletion() bool {
	_, ok := c.provider.(provider.FileDeleter)
	return ok
}

// SupportsFileContentDownload reports whether file content can be downloaded.
func (c *Client) SupportsFileContentDownload() bool {
	_, ok := c.provider.(provider.FileContentDownloader)
	return ok
}
