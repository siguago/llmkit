package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/siguago/llmkit/protocol/openaifiles"
	"github.com/siguago/llmkit/provider"
)

func newFilesTestProvider(t *testing.T, h http.HandlerFunc) *Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewWithBaseURL(srv.URL)
}

func TestUploadFile_MultipartShape(t *testing.T) {
	var gotPath, gotAuth string
	var gotPurpose, gotAnchor, gotSeconds string
	var gotFilename, gotFileContentType, gotFileBody string
	var gotContentLength int64

	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentLength = r.ContentLength
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotPurpose = r.FormValue("purpose")
		gotAnchor = r.FormValue("expires_after[anchor]")
		gotSeconds = r.FormValue("expires_after[seconds]")
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("file part: %v", err)
		} else {
			defer file.Close()
			body, _ := io.ReadAll(file)
			gotFileBody = string(body)
			gotFilename = header.Filename
			gotFileContentType = header.Header.Get("Content-Type")
		}
		w.Header().Set("x-request-id", "req_files_1")
		_, _ = io.WriteString(w, `{"id":"file-abc","object":"file","bytes":21,"created_at":1,"filename":"input.jsonl","purpose":"batch"}`)
	})

	file, err := p.UploadFile(context.Background(), "sk-live", &openaifiles.UploadRequest{
		Filename:    "input.jsonl",
		Purpose:     openaifiles.PurposeBatch,
		Content:     strings.NewReader(`{"custom_id":"r1"}` + "\n"),
		ContentType: "application/jsonl",
		ExpiresAfter: &openaifiles.ExpiresAfter{
			Anchor:  openaifiles.ExpiresAfterAnchorCreatedAt,
			Seconds: 2592000,
		},
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if gotPath != "POST /files" {
		t.Errorf("path = %q, want POST /files", gotPath)
	}
	if gotAuth != "Bearer sk-live" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotContentLength > 0 {
		t.Errorf("upload must stream (chunked), got Content-Length %d", gotContentLength)
	}
	if gotPurpose != "batch" || gotAnchor != "created_at" || gotSeconds != "2592000" {
		t.Errorf("form fields = purpose %q anchor %q seconds %q", gotPurpose, gotAnchor, gotSeconds)
	}
	if gotFilename != "input.jsonl" || gotFileContentType != "application/jsonl" {
		t.Errorf("file part = %q %q", gotFilename, gotFileContentType)
	}
	if gotFileBody != `{"custom_id":"r1"}`+"\n" {
		t.Errorf("file body = %q", gotFileBody)
	}
	if file.ID != "file-abc" || file.RequestID != "req_files_1" {
		t.Errorf("decoded file = %+v", file)
	}
}

func TestUploadFile_OmitsExpiryAndPurposeWhenUnset(t *testing.T) {
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		if _, ok := r.MultipartForm.Value["purpose"]; ok {
			t.Error("unset purpose must not be sent; the upstream owns the missing-purpose error")
		}
		if _, ok := r.MultipartForm.Value["expires_after[anchor]"]; ok {
			t.Error("unset expiry must not be sent")
		}
		_, _ = io.WriteString(w, `{"id":"file-abc","object":"file"}`)
	})
	_, err := p.UploadFile(context.Background(), "sk", &openaifiles.UploadRequest{
		Filename: "a.bin", Content: strings.NewReader("x"),
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
}

func TestUploadFile_ValidatesBeforeNetwork(t *testing.T) {
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid request must not reach the network")
	})
	if _, err := p.UploadFile(context.Background(), "sk", nil); err == nil {
		t.Error("nil request must error")
	}
	if _, err := p.UploadFile(context.Background(), "sk", &openaifiles.UploadRequest{
		Purpose: "batch", Content: strings.NewReader("x"),
	}); err == nil {
		t.Error("missing filename must error")
	}
}

func TestUploadFile_UpstreamErrorClassified(t *testing.T) {
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		// Draining the body first mirrors real servers; the pipe writer must
		// not deadlock the request when the server answers early either way.
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key","type":"invalid_api_key"}}`)
	})
	_, err := p.UploadFile(context.Background(), "sk-bad", &openaifiles.UploadRequest{
		Filename: "a.bin", Purpose: "user_data", Content: strings.NewReader("x"),
	})
	if err == nil {
		t.Fatal("want error")
	}
	var perr *provider.ProviderError
	if !errors.As(err, &perr) || perr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("error = %v, want ProviderError with 401", err)
	}
	if provider.ProviderCode(err) != "invalid_api_key" {
		t.Errorf("provider code = %q", provider.ProviderCode(err))
	}
}

func TestListFiles_QueryParams(t *testing.T) {
	var gotQuery string
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"object":"list","data":[],"first_id":"","last_id":"","has_more":false}`)
	})
	list, err := p.ListFiles(context.Background(), "sk", &openaifiles.ListRequest{
		After: "file-cursor", Limit: 25, Order: "asc", Purpose: "batch",
	})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	for _, want := range []string{"after=file-cursor", "limit=25", "order=asc", "purpose=batch"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if list.Data == nil || len(list.Data) != 0 {
		t.Errorf("empty list must decode to empty slice, got %#v", list.Data)
	}
}

func TestListFiles_NilRequestSendsNoQuery(t *testing.T) {
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("nil request must not invent query params, got %q", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[],"has_more":false}`)
	})
	if _, err := p.ListFiles(context.Background(), "sk", nil); err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
}

func TestRetrieveAndDeleteFile_PathsAndEscaping(t *testing.T) {
	var paths []string
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		if r.Method == http.MethodDelete {
			_, _ = io.WriteString(w, `{"id":"file-a/b","object":"file","deleted":true}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"file-a/b","object":"file"}`)
	})
	if _, err := p.RetrieveFile(context.Background(), "sk", "file-a/b"); err != nil {
		t.Fatalf("RetrieveFile: %v", err)
	}
	deleted, err := p.DeleteFile(context.Background(), "sk", "file-a/b")
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if !deleted.Deleted {
		t.Error("deleted flag lost")
	}
	want := []string{"GET /files/file-a%2Fb", "DELETE /files/file-a%2Fb"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

func TestFileResourceOps_RejectEmptyID(t *testing.T) {
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("empty ID must not reach the network")
	})
	if _, err := p.RetrieveFile(context.Background(), "sk", "  "); err == nil {
		t.Error("RetrieveFile with blank ID must error")
	}
	if _, err := p.DeleteFile(context.Background(), "sk", ""); err == nil {
		t.Error("DeleteFile with empty ID must error")
	}
	if _, err := p.DownloadFileContent(context.Background(), "sk", ""); err == nil {
		t.Error("DownloadFileContent with empty ID must error")
	}
}

func TestDownloadFileContent_StreamsBody(t *testing.T) {
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Method + " " + r.URL.Path; got != "GET /files/file-abc/content" {
			t.Errorf("path = %q", got)
		}
		_, _ = io.WriteString(w, "line1\nline2\n")
	})
	body, err := p.DownloadFileContent(context.Background(), "sk", "file-abc")
	if err != nil {
		t.Fatalf("DownloadFileContent: %v", err)
	}
	defer body.Close()
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(content) != "line1\nline2\n" {
		t.Errorf("content = %q", content)
	}
}

func TestDownloadFileContent_ErrorStatusClosesBody(t *testing.T) {
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"no such file","type":"invalid_request_error","code":"not_found"}}`)
	})
	_, err := p.DownloadFileContent(context.Background(), "sk", "file-miss")
	if err == nil {
		t.Fatal("want error")
	}
	var perr *provider.ProviderError
	if !errors.As(err, &perr) || perr.StatusCode != http.StatusNotFound {
		t.Fatalf("error = %v, want 404 ProviderError", err)
	}
}

// The download body must survive past the non-stream client's absolute
// timeout budget, which is why it rides the stream client. Cripple the
// non-stream client: if the download used it, the request would fail.
func TestDownloadFileContent_UsesStreamClient(t *testing.T) {
	p := newFilesTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "payload")
	})
	if p.streamClient.Timeout != 0 {
		t.Fatal("precondition: stream client must have no absolute timeout")
	}
	p.client.Timeout = time.Nanosecond
	body, err := p.DownloadFileContent(context.Background(), "sk", "file-abc")
	if err != nil {
		t.Fatalf("download must ride the stream client, got %v", err)
	}
	defer body.Close()
	content, err := io.ReadAll(body)
	if err != nil || string(content) != "payload" {
		t.Fatalf("read = %q, %v", content, err)
	}
}
