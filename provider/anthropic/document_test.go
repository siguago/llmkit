package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/siguago/llmkit/provider"
)

func TestBuildUserMessage_FileBecomesDocumentBlock(t *testing.T) {
	// OpenAI's file content type with inline data → Anthropic document block.
	// Anthropic natively supports PDF input; without this translation the
	// file part gets silently dropped at the gateway boundary.
	out := buildUserMessage(provider.Message{
		Role: "user",
		Content: []provider.ContentPart{
			{Type: "text", Text: "Summarize this:"},
			{Type: "file", File: &provider.FileContent{
				FileData: "BASE64_PDF_BYTES",
				MimeType: "application/pdf",
				Filename: "report.pdf",
			}},
		},
	})
	blocks, ok := out.Content.([]contentBlock)
	if !ok {
		t.Fatalf("expected []contentBlock when file present, got %T", out.Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (text + document), got %d: %+v", len(blocks), blocks)
	}
	if blocks[1].Type != "document" {
		t.Errorf("file part not converted to document block: %+v", blocks[1])
	}
	if blocks[1].Source == nil {
		t.Fatal("document source missing")
	}
	if blocks[1].Source.MediaType != "application/pdf" || blocks[1].Source.Data != "BASE64_PDF_BYTES" {
		t.Errorf("source fields wrong: %+v", blocks[1].Source)
	}
	if blocks[1].Source.Type != "base64" {
		t.Errorf("source type wrong: %q, want base64", blocks[1].Source.Type)
	}
}

func TestBuildUserMessage_FileWithDataURIIsParsed(t *testing.T) {
	// data: URI form ("data:application/pdf;base64,...") is also valid OpenAI
	// input — both raw + URI forms must converge on the same Anthropic shape.
	out := buildUserMessage(provider.Message{
		Role: "user",
		Content: []provider.ContentPart{
			{Type: "file", File: &provider.FileContent{
				FileData: "data:application/pdf;base64,RAWPDFDATA",
			}},
		},
	})
	blocks := out.Content.([]contentBlock)
	if blocks[0].Source == nil || blocks[0].Source.MediaType != "application/pdf" {
		t.Fatalf("data URI not parsed: %+v", blocks[0].Source)
	}
	if blocks[0].Source.Data != "RAWPDFDATA" {
		t.Errorf("data URI base64 not extracted: %q", blocks[0].Source.Data)
	}
}

func TestBuildUserMessage_FileWithoutDataDropped(t *testing.T) {
	// file_id (Files API reference) is OpenAI-specific; Anthropic can't
	// resolve it. Drop silently to keep other parts intact.
	out := buildUserMessage(provider.Message{
		Role: "user",
		Content: []provider.ContentPart{
			{Type: "text", Text: "describe"},
			{Type: "file", File: &provider.FileContent{FileID: "file-abc"}},
		},
	})
	blocks := out.Content.([]contentBlock)
	if len(blocks) != 1 {
		t.Fatalf("file_id-only must be dropped; got %d blocks: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != "text" {
		t.Errorf("text part lost: %+v", blocks[0])
	}
}

func TestBuildUserMessage_FileWithoutMimeDefaultsToPDF(t *testing.T) {
	// Bare base64 without mime_type defaults to PDF — OpenAI's implicit
	// convention when serving file_data without explicit mime.
	out := buildUserMessage(provider.Message{
		Role: "user",
		Content: []provider.ContentPart{
			{Type: "file", File: &provider.FileContent{FileData: "RAWBYTES"}},
		},
	})
	blocks := out.Content.([]contentBlock)
	if blocks[0].Source == nil || blocks[0].Source.MediaType != "application/pdf" {
		t.Errorf("default mime should be application/pdf, got %+v", blocks[0].Source)
	}
}

func TestBuildUserMessage_FileCacheControlPreserved(t *testing.T) {
	// Long PDF is a great cache-breakpoint candidate; cache_control on the
	// file part must reach the document block so Anthropic creates the
	// breakpoint.
	cc := map[string]any{"type": "ephemeral", "ttl": "1h"}
	out := buildUserMessage(provider.Message{
		Role: "user",
		Content: []provider.ContentPart{
			{
				Type: "file",
				File: &provider.FileContent{
					FileData: "BIGPDF",
					MimeType: "application/pdf",
				},
				CacheControl: cc,
			},
		},
	})
	blocks := out.Content.([]contentBlock)
	if blocks[0].CacheControl == nil {
		t.Errorf("cache_control on file part lost: %+v", blocks[0])
	}
}

func TestBuildUserMessage_FileEndToEndWireShape(t *testing.T) {
	// Wire-level integration: encode the message to JSON and verify the
	// document block has the right top-level shape Anthropic expects.
	out := buildUserMessage(provider.Message{
		Role: "user",
		Content: []provider.ContentPart{
			{Type: "file", File: &provider.FileContent{
				FileData: "BASE64",
				MimeType: "application/pdf",
			}},
		},
	})
	bytes, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(bytes)
	if !strings.Contains(got, `"type":"document"`) {
		t.Errorf("document block type missing: %s", got)
	}
	if !strings.Contains(got, `"media_type":"application/pdf"`) {
		t.Errorf("media_type wire field wrong: %s", got)
	}
	if !strings.Contains(got, `"data":"BASE64"`) {
		t.Errorf("base64 data missing on wire: %s", got)
	}
}
