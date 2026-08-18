package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/siguago/llmkit/protocol/openaifiles"
	"github.com/siguago/llmkit/provider"
)

var (
	_ provider.FileUploader          = (*Provider)(nil)
	_ provider.FileLister            = (*Provider)(nil)
	_ provider.FileRetriever         = (*Provider)(nil)
	_ provider.FileDeleter           = (*Provider)(nil)
	_ provider.FileContentDownloader = (*Provider)(nil)
)

// UploadFile calls POST /v1/files with a streaming multipart body. The
// request content is read exactly once and never buffered whole, so the
// operation cannot be replayed — retrying is the caller's decision, with a
// reopened source.
func (p *Provider) UploadFile(ctx context.Context, apiKey string, req *openaifiles.UploadRequest) (*openaifiles.File, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := writeUploadForm(mw, req)
		if closeErr := mw.Close(); err == nil {
			err = closeErr
		}
		pw.CloseWithError(err)
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.filesURL, pr)
	if err != nil {
		pr.CloseWithError(err)
		return nil, err
	}
	provider.SetBearer(httpReq.Header, apiKey)
	httpReq.Header.Set("content-type", mw.FormDataContentType())

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := filesErrorFromResponse(resp); err != nil {
		return nil, err
	}
	var file openaifiles.File
	if err := decodeFilesJSON(resp.Body, &file, "upload response"); err != nil {
		return nil, err
	}
	file.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &file, nil
}

func writeUploadForm(mw *multipart.Writer, req *openaifiles.UploadRequest) error {
	if req.Purpose != "" {
		if err := mw.WriteField("purpose", req.Purpose); err != nil {
			return err
		}
	}
	if req.ExpiresAfter != nil {
		if err := mw.WriteField("expires_after[anchor]", req.ExpiresAfter.Anchor); err != nil {
			return err
		}
		if err := mw.WriteField("expires_after[seconds]", strconv.FormatInt(req.ExpiresAfter.Seconds, 10)); err != nil {
			return err
		}
	}
	part, err := provider.CreateFormFileWithContentType(mw, "file", req.Filename, req.ContentType)
	if err != nil {
		return err
	}
	_, err = io.Copy(part, req.Content)
	return err
}

// ListFiles calls GET /v1/files. A nil request lists with the upstream's own
// defaults.
func (p *Provider) ListFiles(ctx context.Context, apiKey string, req *openaifiles.ListRequest) (*openaifiles.FileList, error) {
	endpoint := p.filesURL
	if req != nil {
		query := make(url.Values)
		if req.After != "" {
			query.Set("after", req.After)
		}
		if req.Limit > 0 {
			query.Set("limit", strconv.Itoa(req.Limit))
		}
		if req.Order != "" {
			query.Set("order", req.Order)
		}
		if req.Purpose != "" {
			query.Set("purpose", req.Purpose)
		}
		if len(query) > 0 {
			endpoint += "?" + query.Encode()
		}
	}
	resp, err := p.doFilesRequest(ctx, apiKey, http.MethodGet, endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list openaifiles.FileList
	if err := decodeFilesJSON(resp.Body, &list, "list response"); err != nil {
		return nil, err
	}
	list.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &list, nil
}

// RetrieveFile calls GET /v1/files/{id}.
func (p *Provider) RetrieveFile(ctx context.Context, apiKey, fileID string) (*openaifiles.File, error) {
	endpoint, err := p.fileResourceURL(fileID)
	if err != nil {
		return nil, err
	}
	resp, err := p.doFilesRequest(ctx, apiKey, http.MethodGet, endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var file openaifiles.File
	if err := decodeFilesJSON(resp.Body, &file, "retrieve response"); err != nil {
		return nil, err
	}
	file.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &file, nil
}

// DeleteFile calls DELETE /v1/files/{id}.
func (p *Provider) DeleteFile(ctx context.Context, apiKey, fileID string) (*openaifiles.DeletedFile, error) {
	endpoint, err := p.fileResourceURL(fileID)
	if err != nil {
		return nil, err
	}
	resp, err := p.doFilesRequest(ctx, apiKey, http.MethodDelete, endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var deleted openaifiles.DeletedFile
	if err := decodeFilesJSON(resp.Body, &deleted, "delete response"); err != nil {
		return nil, err
	}
	deleted.RequestID = provider.RequestIDFromHeader(resp.Header)
	return &deleted, nil
}

// DownloadFileContent calls GET /v1/files/{id}/content and returns the raw
// body for the caller to stream and Close. The download uses the stream
// client: a 512 MB body can legitimately outlive any fixed client timeout, so
// the request context is the only lifetime bound.
func (p *Provider) DownloadFileContent(ctx context.Context, apiKey, fileID string) (io.ReadCloser, error) {
	endpoint, err := p.fileResourceURL(fileID)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/content", nil)
	if err != nil {
		return nil, err
	}
	provider.SetBearer(httpReq.Header, apiKey)
	resp, err := p.streamClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if err := filesErrorFromResponse(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

func (p *Provider) fileResourceURL(fileID string) (string, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", fmt.Errorf("openai files: file ID is required")
	}
	return p.filesURL + "/" + url.PathEscape(fileID), nil
}

func (p *Provider) doFilesRequest(ctx context.Context, apiKey, method, endpoint string) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	provider.SetBearer(httpReq.Header, apiKey)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if err := filesErrorFromResponse(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

// filesErrorFromResponse maps a non-2xx response to a classified provider
// error, preserving the status, Retry-After, request ID, and the (bounded)
// body. It reuses the Responses error envelope: the Files API answers with
// the same {"error": {...}} shape.
func filesErrorFromResponse(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	defer resp.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponsesErrorBodyBytes+1))
	if len(payload) > maxResponsesErrorBodyBytes {
		payload = payload[:maxResponsesErrorBodyBytes]
	}
	apiErr := decodeFilesAPIError(resp, payload)
	if readErr != nil {
		return errors.Join(apiErr, fmt.Errorf("openai files: read error response: %w", readErr))
	}
	return apiErr
}

func decodeFilesAPIError(resp *http.Response, payload []byte) error {
	providerErr := provider.NewProviderErrorFromResponse(resp, "openai files", payload)
	var envelope struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return provider.WithErrorMetadata(providerErr, "", responsesErrorCategory(resp.StatusCode, ""))
	}
	code := envelope.Error.Code
	if code == "" {
		code = envelope.Error.Type
	}
	return provider.WithErrorMetadata(providerErr, code, responsesErrorCategory(resp.StatusCode, envelope.Error.Type))
}

func decodeFilesJSON(reader io.Reader, target any, what string) error {
	payload, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("openai files: read %s: %w", what, err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("openai files: decode %s: %w", what, err)
	}
	return nil
}
