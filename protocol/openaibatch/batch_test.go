package openaibatch

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestBatchDecode_FullObject(t *testing.T) {
	wire := `{
  "id": "batch_abc123",
  "object": "batch",
  "endpoint": "/v1/chat/completions",
  "errors": {"object":"list","data":[{"code":"invalid_json_line","line":17,"message":"bad line","param":null}]},
  "input_file_id": "file-abc123",
  "completion_window": "24h",
  "status": "completed",
  "output_file_id": "file-out",
  "error_file_id": "file-err",
  "created_at": 1711471533,
  "in_progress_at": 1711471538,
  "expires_at": 1711557933,
  "finalizing_at": 1711493133,
  "completed_at": 1711493163,
  "failed_at": null,
  "expired_at": null,
  "cancelling_at": null,
  "cancelled_at": null,
  "model": "gpt-5-2025-08-07",
  "request_counts": {"total": 100, "completed": 95, "failed": 5},
  "metadata": {"customer_id": "user_123"},
  "usage": {
    "input_tokens": 50,
    "input_tokens_details": {"cached_tokens": 12},
    "output_tokens": 20,
    "output_tokens_details": {"reasoning_tokens": 7},
    "total_tokens": 70
  },
  "future_field": 900719925474099312345
}`
	var batch Batch
	if err := json.Unmarshal([]byte(wire), &batch); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if batch.ID != "batch_abc123" || batch.Status != StatusCompleted ||
		batch.Endpoint != EndpointChatCompletions || batch.CompletionWindow != CompletionWindow24h {
		t.Fatalf("core fields lost: %+v", batch)
	}
	if batch.OutputFileID != "file-out" || batch.ErrorFileID != "file-err" || batch.Model != "gpt-5-2025-08-07" {
		t.Fatalf("result fields lost: %+v", batch)
	}
	if batch.RequestCounts == nil || batch.RequestCounts.Total != 100 || batch.RequestCounts.Failed != 5 {
		t.Fatalf("request counts lost: %+v", batch.RequestCounts)
	}
	if batch.Errors == nil || len(batch.Errors.Data) != 1 {
		t.Fatalf("errors lost: %+v", batch.Errors)
	}
	entry := batch.Errors.Data[0]
	if entry.Code != "invalid_json_line" || entry.Line == nil || *entry.Line != 17 {
		t.Fatalf("error entry lost: %+v", entry)
	}
	if batch.Usage == nil || batch.Usage.TotalTokens != 70 ||
		batch.Usage.InputTokensDetails == nil || batch.Usage.InputTokensDetails.CachedTokens != 12 ||
		batch.Usage.OutputTokensDetails == nil || batch.Usage.OutputTokensDetails.ReasoningTokens != 7 {
		t.Fatalf("usage lost: %+v", batch.Usage)
	}
	if !strings.Contains(string(batch.RawJSON()), "900719925474099312345") {
		t.Fatal("RawJSON must preserve unmodeled fields verbatim")
	}
}

// The upstream only fills usage on batches created after 2025-09-07. Absent
// usage must stay nil — a synthesized zero would read as "this batch cost
// nothing".
func TestBatchDecode_AbsentUsageStaysNil(t *testing.T) {
	var batch Batch
	if err := json.Unmarshal([]byte(`{"id":"b","object":"batch","status":"validating","created_at":1}`), &batch); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if batch.Usage != nil {
		t.Fatalf("absent usage must decode to nil, got %+v", batch.Usage)
	}
	if batch.RequestCounts != nil {
		t.Fatalf("absent request_counts must decode to nil, got %+v", batch.RequestCounts)
	}
}

func TestBatchDecode_ErrorLineNullSurvives(t *testing.T) {
	var batch Batch
	wire := `{"id":"b","errors":{"data":[{"code":"c","message":"m","line":null}]}}`
	if err := json.Unmarshal([]byte(wire), &batch); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if batch.Errors.Data[0].Line != nil {
		t.Fatal("null line must decode to nil, not zero")
	}
}

func TestBatchDecode_UnknownStatusSurvives(t *testing.T) {
	var batch Batch
	if err := json.Unmarshal([]byte(`{"id":"b","status":"future_status"}`), &batch); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if batch.Status != "future_status" {
		t.Fatalf("unknown status must pass through, got %q", batch.Status)
	}
	if batch.IsTerminal() {
		t.Fatal("an unknown status must not be treated as terminal")
	}
}

func TestBatchIsTerminal(t *testing.T) {
	terminal := []string{StatusCompleted, StatusFailed, StatusExpired, StatusCancelled}
	for _, status := range terminal {
		if !(&Batch{Status: status}).IsTerminal() {
			t.Errorf("%s must be terminal", status)
		}
	}
	for _, status := range []string{StatusValidating, StatusInProgress, StatusFinalizing, StatusCancelling, ""} {
		if (&Batch{Status: status}).IsTerminal() {
			t.Errorf("%s must not be terminal", status)
		}
	}
}

func TestTopLevelNullRejected(t *testing.T) {
	cases := map[string]func([]byte) error{
		"batch":  func(b []byte) error { var v Batch; return json.Unmarshal(b, &v) },
		"list":   func(b []byte) error { var v BatchList; return json.Unmarshal(b, &v) },
		"output": func(b []byte) error { var v OutputItem; return json.Unmarshal(b, &v) },
	}
	for name, decode := range cases {
		if err := decode([]byte("null")); err == nil {
			t.Errorf("%s: top-level null must not decode into a zero-value success", name)
		}
	}
}

func TestCreateRequestValidate(t *testing.T) {
	var nilReq *CreateRequest
	if err := nilReq.Validate(); err == nil {
		t.Error("nil request must fail")
	}
	if err := (&CreateRequest{Endpoint: EndpointResponses}).Validate(); err == nil {
		t.Error("missing input file must fail")
	}
	if err := (&CreateRequest{InputFileID: "file-1"}).Validate(); err == nil {
		t.Error("missing endpoint must fail")
	}
	// completion_window is upstream-validated; an empty one must pass local
	// validation so the upstream's own error stays visible.
	if err := (&CreateRequest{InputFileID: "file-1", Endpoint: EndpointResponses}).Validate(); err != nil {
		t.Errorf("structural-only validation: %v", err)
	}
}

func TestCreateRequestMarshal_OmitsUnsetOptionals(t *testing.T) {
	encoded, err := json.Marshal(&CreateRequest{
		InputFileID: "file-1", Endpoint: EndpointChatCompletions, CompletionWindow: CompletionWindow24h,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := string(encoded)
	if strings.Contains(got, "metadata") || strings.Contains(got, "output_expires_after") {
		t.Fatalf("unset optionals must be omitted: %s", got)
	}
	want := `{"input_file_id":"file-1","endpoint":"/v1/chat/completions","completion_window":"24h"}`
	if got != want {
		t.Fatalf("wire = %s, want %s", got, want)
	}
}

func TestBatchListDecode(t *testing.T) {
	wire := `{"object":"list","data":[{"id":"batch_1","status":"completed"}],"first_id":"batch_1","last_id":"batch_1","has_more":false}`
	var list BatchList
	if err := json.Unmarshal([]byte(wire), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != "batch_1" || list.HasMore {
		t.Fatalf("list = %+v", list)
	}
	if list.Data[0].RawJSON() == nil {
		t.Fatal("nested batches must retain raw bytes")
	}
}

func TestNewInputItemAndEncodeInput(t *testing.T) {
	item, err := NewInputItem("req-1", EndpointChatCompletions, map[string]any{
		"model": "gpt-5-mini", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatalf("NewInputItem: %v", err)
	}
	if item.Method != "POST" || item.URL != EndpointChatCompletions {
		t.Fatalf("item = %+v", item)
	}
	var out strings.Builder
	second := InputItem{CustomID: "req-2", Method: "POST", URL: EndpointChatCompletions, Body: json.RawMessage(`{"model":"gpt-5-mini"}`)}
	if err := EncodeInput(&out, item, second); err != nil {
		t.Fatalf("EncodeInput: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), out.String())
	}
	var round InputItem
	if err := json.Unmarshal([]byte(lines[0]), &round); err != nil {
		t.Fatalf("line 1 must be standalone JSON: %v", err)
	}
	if round.CustomID != "req-1" || !strings.Contains(string(round.Body), `"gpt-5-mini"`) {
		t.Fatalf("round-tripped line = %+v", round)
	}
}

func TestOutputReader_DecodesLinesAndPreservesBody(t *testing.T) {
	file := `{"id":"batch_req_1","custom_id":"req-2","response":{"status_code":200,"request_id":"r1","body":{"big":900719925474099312345}},"error":null}
{"id":"batch_req_2","custom_id":"req-1","response":null,"error":{"code":"server_error","message":"boom"}}
`
	reader := NewOutputReader(io.NopCloser(strings.NewReader(file)), 0)
	defer reader.Close()

	first, err := reader.Next()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.CustomID != "req-2" || first.Response == nil || first.Response.StatusCode != 200 {
		t.Fatalf("first = %+v", first)
	}
	if string(first.Response.Body) != `{"big":900719925474099312345}` {
		t.Fatalf("body must survive verbatim, got %s", first.Response.Body)
	}
	second, err := reader.Next()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.Error == nil || second.Error.Code != "server_error" || second.Response != nil {
		t.Fatalf("second = %+v", second)
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Fatalf("end = %v, want io.EOF", err)
	}
}

func TestOutputReader_SkipsBlankAndCRLF(t *testing.T) {
	file := "{\"id\":\"a\",\"custom_id\":\"1\"}\r\n\n{\"id\":\"b\",\"custom_id\":\"2\"}"
	reader := NewOutputReader(io.NopCloser(strings.NewReader(file)), 0)
	defer reader.Close()
	var ids []string
	for {
		item, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		ids = append(ids, item.CustomID)
	}
	if len(ids) != 2 || ids[0] != "1" || ids[1] != "2" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestOutputReader_MalformedLineFailsWithLineNumber(t *testing.T) {
	file := "{\"id\":\"a\",\"custom_id\":\"1\"}\n{broken\n"
	reader := NewOutputReader(io.NopCloser(strings.NewReader(file)), 0)
	defer reader.Close()
	if _, err := reader.Next(); err != nil {
		t.Fatalf("first line: %v", err)
	}
	_, err := reader.Next()
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v, want a line-2 failure", err)
	}
	// The failure is sticky: a corrupt artifact must not half-succeed.
	if _, again := reader.Next(); again == nil || again == io.EOF {
		t.Fatalf("subsequent Next = %v, want the sticky error", again)
	}
}

func TestOutputReader_LineCapEnforced(t *testing.T) {
	long := `{"id":"a","custom_id":"1","response":{"status_code":200,"request_id":"r","body":"` +
		strings.Repeat("x", 2048) + `"}}`
	reader := NewOutputReader(io.NopCloser(strings.NewReader(long+"\n")), 1024)
	defer reader.Close()
	_, err := reader.Next()
	if err == nil || !strings.Contains(err.Error(), "line limit") {
		t.Fatalf("err = %v, want a line-limit failure", err)
	}

	roomy := NewOutputReader(io.NopCloser(strings.NewReader(long+"\n")), int64(len(long))+16)
	defer roomy.Close()
	if _, err := roomy.Next(); err != nil {
		t.Fatalf("a line under the cap must decode: %v", err)
	}
}

func TestOutputReader_EmptyFileIsJustEOF(t *testing.T) {
	reader := NewOutputReader(io.NopCloser(strings.NewReader("")), 0)
	defer reader.Close()
	if _, err := reader.Next(); err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}
