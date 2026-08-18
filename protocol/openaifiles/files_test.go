package openaifiles

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFileDecode_KeepsModeledFieldsAndRawBytes(t *testing.T) {
	wire := `{
  "id": "file-abc",
  "object": "file",
  "bytes": 120000,
  "created_at": 1677610602,
  "expires_at": 1680202602,
  "filename": "input.jsonl",
  "purpose": "batch",
  "status": "processed",
  "status_details": "legacy detail",
  "future_field": {"nested": 900719925474099312345}
}`
	var file File
	if err := json.Unmarshal([]byte(wire), &file); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if file.ID != "file-abc" || file.Bytes != 120000 || file.CreatedAt != 1677610602 ||
		file.ExpiresAt != 1680202602 || file.Filename != "input.jsonl" || file.Purpose != PurposeBatch {
		t.Fatalf("modeled fields lost: %+v", file)
	}
	if file.Status != "processed" || file.StatusDetails != "legacy detail" {
		t.Fatalf("deprecated upstream fields must still decode: %+v", file)
	}
	raw := string(file.RawJSON())
	if !strings.Contains(raw, `"future_field"`) || !strings.Contains(raw, "900719925474099312345") {
		t.Fatalf("RawJSON must preserve unmodeled fields verbatim, got %s", raw)
	}
}

func TestFileDecode_UnknownPurposeSurvives(t *testing.T) {
	var file File
	if err := json.Unmarshal([]byte(`{"id":"f","purpose":"future_purpose"}`), &file); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if file.Purpose != "future_purpose" {
		t.Fatalf("unknown purpose must pass through, got %q", file.Purpose)
	}
}

func TestObjectDecode_RejectsTopLevelNull(t *testing.T) {
	cases := map[string]func([]byte) error{
		"file":    func(b []byte) error { var v File; return json.Unmarshal(b, &v) },
		"list":    func(b []byte) error { var v FileList; return json.Unmarshal(b, &v) },
		"deleted": func(b []byte) error { var v DeletedFile; return json.Unmarshal(b, &v) },
	}
	for name, decode := range cases {
		if err := decode([]byte(" null ")); err == nil {
			t.Errorf("%s: top-level null must not decode into a zero-value success", name)
		}
	}
}

func TestFileListDecode(t *testing.T) {
	wire := `{
  "object": "list",
  "data": [{"id":"file-1","purpose":"user_data"},{"id":"file-2","purpose":"batch"}],
  "first_id": "file-1",
  "last_id": "file-2",
  "has_more": true
}`
	var list FileList
	if err := json.Unmarshal([]byte(wire), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Data) != 2 || list.Data[0].ID != "file-1" || list.Data[1].Purpose != PurposeBatch {
		t.Fatalf("data lost: %+v", list)
	}
	if list.FirstID != "file-1" || list.LastID != "file-2" || !list.HasMore {
		t.Fatalf("pagination fields lost: %+v", list)
	}
	if list.Data[0].RawJSON() == nil {
		t.Fatal("nested files must retain their raw bytes too")
	}
}

func TestUploadRequestValidate(t *testing.T) {
	var nilReq *UploadRequest
	if err := nilReq.Validate(); err == nil {
		t.Error("nil request must fail validation")
	}
	if err := (&UploadRequest{Purpose: PurposeBatch, Content: strings.NewReader("x")}).Validate(); err == nil {
		t.Error("missing filename must fail: the multipart file part cannot be built without one")
	}
	if err := (&UploadRequest{Filename: "a.jsonl", Purpose: PurposeBatch}).Validate(); err == nil {
		t.Error("missing content must fail")
	}
	if err := (&UploadRequest{Filename: "a.jsonl", Content: strings.NewReader("x")}).Validate(); err != nil {
		t.Errorf("purpose is upstream-validated, not local: %v", err)
	}
}

func TestFileMarshal_KnownFieldsOnly(t *testing.T) {
	// Encoding is for logging/debugging, not for the wire; it must at least
	// not invent values for absent optional fields.
	var file File
	if err := json.Unmarshal([]byte(`{"id":"f","object":"file"}`), &file); err != nil {
		t.Fatalf("decode: %v", err)
	}
	encoded, err := json.Marshal(&file)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(encoded), "expires_at") || strings.Contains(string(encoded), "status") {
		t.Fatalf("absent optional fields must stay omitted: %s", encoded)
	}
}
