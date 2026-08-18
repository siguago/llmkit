package llmkit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/siguago/llmkit/protocol/openaifiles"
)

func TestFiles_UnsupportedProviderFailsBeforeNetwork(t *testing.T) {
	c := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("unsupported file operation reached the network")
	})
	if _, err := c.UploadFile(context.Background(), &openaifiles.UploadRequest{
		Filename: "a.jsonl", Purpose: openaifiles.PurposeBatch, Content: strings.NewReader("x"),
	}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("UploadFile err = %v, want ErrUnsupported", err)
	}
	if _, err := c.ListFiles(context.Background(), nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("ListFiles err = %v, want ErrUnsupported", err)
	}
	if _, err := c.RetrieveFile(context.Background(), "file-1"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("RetrieveFile err = %v, want ErrUnsupported", err)
	}
	if _, err := c.DeleteFile(context.Background(), "file-1"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("DeleteFile err = %v, want ErrUnsupported", err)
	}
	if _, err := c.DownloadFileContent(context.Background(), "file-1"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("DownloadFileContent err = %v, want ErrUnsupported", err)
	}
}

// Uploads consume their reader; a replay would send an empty or truncated
// body. The client must therefore never retry an upload on its own, even for
// errors the general policy considers retryable.
func TestUploadFile_NeverRetried(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom","type":"server_error"}}`)
	})
	_, err := c.UploadFile(context.Background(), &openaifiles.UploadRequest{
		Filename: "a.jsonl", Purpose: openaifiles.PurposeBatch, Content: strings.NewReader("x"),
	})
	if err == nil {
		t.Fatal("want error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upload hit the server %d times, want exactly 1", got)
	}
}

func TestListFiles_RetriesLikeARead(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"flake","type":"server_error"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"file-1","purpose":"batch"}],"has_more":false}`)
	})
	list, err := c.ListFiles(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("want one retry, got %d calls", calls.Load())
	}
	if len(list.Data) != 1 || list.Data[0].ID != "file-1" {
		t.Fatalf("list = %+v", list)
	}
}

func TestDeleteFile_NotRetried(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom","type":"server_error"}}`)
	})
	if _, err := c.DeleteFile(context.Background(), "file-1"); err == nil {
		t.Fatal("want error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("delete hit the server %d times, want exactly 1: deletion changes resource state", got)
	}
}

func TestDownloadFileContent_HandshakeRetriedBodyStreams(t *testing.T) {
	var calls atomic.Int32
	c := newTestClientFor(t, OpenAI, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"flake","type":"server_error"}}`)
			return
		}
		_, _ = io.WriteString(w, "content-bytes")
	})
	body, err := c.DownloadFileContent(context.Background(), "file-1")
	if err != nil {
		t.Fatalf("DownloadFileContent: %v", err)
	}
	defer body.Close()
	content, err := io.ReadAll(body)
	if err != nil || string(content) != "content-bytes" {
		t.Fatalf("read = %q, %v", content, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("want handshake retry, got %d calls", calls.Load())
	}
}

func TestUploadFile_NilRequestFailsFast(t *testing.T) {
	c := newTestClientFor(t, OpenAI, func(http.ResponseWriter, *http.Request) {
		t.Fatal("nil request must not reach the network")
	})
	if _, err := c.UploadFile(context.Background(), nil); err == nil {
		t.Error("want error for nil request")
	}
}
