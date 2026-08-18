package anthropic

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

func TestMessageBatchDecode(t *testing.T) {
	wire := `{
  "id": "msgbatch_013Zva2CMHLNnXjNJJKqJ2EF",
  "type": "message_batch",
  "processing_status": "in_progress",
  "request_counts": {"processing": 100, "succeeded": 50, "errored": 30, "canceled": 10, "expired": 10},
  "created_at": "2024-08-20T18:37:24.100435Z",
  "expires_at": "2024-08-21T18:37:24.100435Z",
  "ended_at": null,
  "archived_at": null,
  "cancel_initiated_at": null,
  "results_url": null,
  "future_field": {"n": 900719925474099312345}
}`
	var batch MessageBatch
	if err := json.Unmarshal([]byte(wire), &batch); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if batch.ID != "msgbatch_013Zva2CMHLNnXjNJJKqJ2EF" || batch.Type != "message_batch" {
		t.Fatalf("identity lost: %+v", batch)
	}
	if batch.ProcessingStatus != BatchProcessingStatusInProgress || batch.HasEnded() {
		t.Fatalf("status lost: %+v", batch)
	}
	counts := batch.RequestCounts
	if counts.Processing != 100 || counts.Succeeded != 50 || counts.Errored != 30 ||
		counts.Canceled != 10 || counts.Expired != 10 {
		t.Fatalf("request counts lost: %+v", counts)
	}
	if batch.CreatedAt != "2024-08-20T18:37:24.100435Z" {
		t.Fatalf("timestamps must stay verbatim RFC 3339 strings, got %q", batch.CreatedAt)
	}
	if batch.EndedAt != "" || batch.ResultsURL != "" {
		t.Fatalf("null nullable fields must decode to empty strings: %+v", batch)
	}
	if !strings.Contains(string(batch.RawJSON()), "900719925474099312345") {
		t.Fatal("RawJSON must preserve unmodeled fields verbatim")
	}
}

func TestMessageBatchHasEnded(t *testing.T) {
	if (&MessageBatch{ProcessingStatus: BatchProcessingStatusCanceling}).HasEnded() {
		t.Error("canceling has not ended")
	}
	if !(&MessageBatch{ProcessingStatus: BatchProcessingStatusEnded}).HasEnded() {
		t.Error("ended must report ended")
	}
}

func TestMessageBatchCreateRequestValidate(t *testing.T) {
	var nilReq *MessageBatchCreateRequest
	if err := nilReq.Validate(); err == nil {
		t.Error("nil request must fail")
	}
	if err := (&MessageBatchCreateRequest{}).Validate(); err == nil {
		t.Error("empty requests must fail")
	}
	if err := (&MessageBatchCreateRequest{
		Requests: []MessageBatchRequestItem{{CustomID: "a", Params: nil}},
	}).Validate(); err == nil {
		t.Error("nil params must fail: the line would serialize as params:null")
	}
	valid := &MessageBatchCreateRequest{Requests: []MessageBatchRequestItem{{
		CustomID: "a",
		Params: &MessageRequest{
			Model: "claude-sonnet-4-5", MaxTokens: 16,
			Messages: []MessageParam{{Role: RoleUser, Content: StringContent("hi")}},
		},
	}}}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid request rejected: %v", err)
	}
}

// The batch create body must serialize params with the exact MessageRequest
// wire shape — a second schema drifting from the Messages one would be a
// silent contract fork.
func TestMessageBatchCreateRequest_ParamsUseMessageRequestWire(t *testing.T) {
	request := &MessageRequest{
		Model: "claude-sonnet-4-5", MaxTokens: 64,
		Messages: []MessageParam{{Role: RoleUser, Content: StringContent("hello")}},
	}
	standalone, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("encode standalone: %v", err)
	}
	batchBody, err := json.Marshal(&MessageBatchCreateRequest{
		Requests: []MessageBatchRequestItem{{CustomID: "r1", Params: request}},
	})
	if err != nil {
		t.Fatalf("encode batch: %v", err)
	}
	var decoded struct {
		Requests []struct {
			CustomID string          `json:"custom_id"`
			Params   json.RawMessage `json:"params"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(batchBody, &decoded); err != nil {
		t.Fatalf("decode batch body: %v", err)
	}
	if len(decoded.Requests) != 1 || decoded.Requests[0].CustomID != "r1" {
		t.Fatalf("envelope = %s", batchBody)
	}
	if string(decoded.Requests[0].Params) != string(standalone) {
		t.Fatalf("params wire diverged:\nbatch:      %s\nstandalone: %s", decoded.Requests[0].Params, standalone)
	}
}

func TestMessageBatchResultUnion(t *testing.T) {
	succeeded := `{"custom_id":"r1","result":{"type":"succeeded","message":{
    "id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5",
    "content":[{"type":"text","text":"hi"}],
    "usage":{"input_tokens":3,"output_tokens":5}
  }}}`
	var line MessageBatchIndividualResult
	if err := json.Unmarshal([]byte(succeeded), &line); err != nil {
		t.Fatalf("succeeded: %v", err)
	}
	if line.CustomID != "r1" || line.Result.Type != BatchResultTypeSucceeded {
		t.Fatalf("line = %+v", line)
	}
	if line.Result.Message == nil || line.Result.Message.Text() != "hi" {
		t.Fatalf("message lost: %+v", line.Result.Message)
	}

	errored := `{"custom_id":"r2","result":{"type":"errored","error":{
    "type":"error","error":{"type":"rate_limit_error","message":"slow down"},"request_id":"req_x"}}}`
	if err := json.Unmarshal([]byte(errored), &line); err != nil {
		t.Fatalf("errored: %v", err)
	}
	envelope := line.Result.Error
	if envelope == nil || envelope.Error.Type != "rate_limit_error" || envelope.RequestID != "req_x" {
		t.Fatalf("error envelope lost: %+v", envelope)
	}

	for _, kind := range []string{BatchResultTypeCanceled, BatchResultTypeExpired} {
		if err := json.Unmarshal([]byte(`{"custom_id":"r3","result":{"type":"`+kind+`"}}`), &line); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if line.Result.Type != kind || line.Result.Message != nil || line.Result.Error != nil {
			t.Fatalf("%s line = %+v", kind, line.Result)
		}
	}
}

// A future result type must be preserved, not skipped and not an error —
// otherwise one new upstream enum makes whole result files unreadable.
func TestMessageBatchResult_UnknownTypeRawPreserved(t *testing.T) {
	wire := `{"custom_id":"r9","result":{"type":"future_outcome","detail":{"n":900719925474099312345}}}`
	var line MessageBatchIndividualResult
	if err := json.Unmarshal([]byte(wire), &line); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if line.Result.Type != "future_outcome" {
		t.Fatalf("type lost: %+v", line.Result)
	}
	if !strings.Contains(string(line.Result.Raw), "900719925474099312345") {
		t.Fatal("unknown result must keep its raw bytes")
	}
}

func TestMessageBatchResult_MissingTypeFails(t *testing.T) {
	var result MessageBatchResult
	if err := json.Unmarshal([]byte(`{"message":{}}`), &result); err == nil {
		t.Fatal("a result without its discriminator must fail")
	}
}

// Both members are required by the official schema. A line missing either
// used to decode into a zero-value "success" carrying no outcome and no join
// key — found by FuzzMessageBatchResultsReader on the input "{}".
func TestMessageBatchIndividualResult_RequiresBothMembers(t *testing.T) {
	cases := map[string]string{
		"empty object":     `{}`,
		"missing result":   `{"custom_id":"r1"}`,
		"null result":      `{"custom_id":"r1","result":null}`,
		"missing custom":   `{"result":{"type":"canceled"}}`,
		"empty custom_id":  `{"custom_id":"","result":{"type":"canceled"}}`,
		"result not typed": `{"custom_id":"r1","result":{}}`,
	}
	for name, wire := range cases {
		var line MessageBatchIndividualResult
		err := json.Unmarshal([]byte(wire), &line)
		if err == nil {
			t.Errorf("%s: must fail, decoded %+v", name, line)
			continue
		}
		// The wire-shape failures are the ones a relay can cause, so they must
		// be distinguishable from ordinary decode errors.
		if name != "result not typed" && !errors.Is(err, ErrInvalidWire) {
			t.Errorf("%s: err = %v, want ErrInvalidWire", name, err)
		}
	}
}

// Succeeded results embed full Messages, which keep the strict official-
// schema validation: a relay that strips required fields must surface
// ErrInvalidWire, not a zero-value message.
func TestMessageBatchResult_SucceededKeepsStrictMessageValidation(t *testing.T) {
	stripped := `{"custom_id":"r1","result":{"type":"succeeded","message":{"id":"msg_1"}}}`
	var line MessageBatchIndividualResult
	err := json.Unmarshal([]byte(stripped), &line)
	if err == nil || !errors.Is(err, ErrInvalidWire) {
		t.Fatalf("err = %v, want ErrInvalidWire", err)
	}
}

func TestMessageBatchResultsReader(t *testing.T) {
	file := `{"custom_id":"r2","result":{"type":"canceled"}}
{"custom_id":"r1","result":{"type":"succeeded","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":2}}}}
`
	reader := NewMessageBatchResultsReader(io.NopCloser(strings.NewReader(file)), 0)
	defer reader.Close()
	first, err := reader.Next()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.CustomID != "r2" || first.Result.Type != BatchResultTypeCanceled {
		t.Fatalf("first = %+v", first)
	}
	second, err := reader.Next()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.Result.Message == nil || second.Result.Message.Text() != "ok" {
		t.Fatalf("second = %+v", second.Result)
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Fatalf("end = %v, want io.EOF", err)
	}
}

func TestMessageBatchResultsReader_LineCapAndStickyError(t *testing.T) {
	long := `{"custom_id":"r1","result":{"type":"canceled","pad":"` + strings.Repeat("x", 4096) + `"}}`
	reader := NewMessageBatchResultsReader(io.NopCloser(strings.NewReader(long+"\n")), 512)
	defer reader.Close()
	_, err := reader.Next()
	if err == nil || !strings.Contains(err.Error(), "line limit") {
		t.Fatalf("err = %v, want line-limit failure", err)
	}
	if _, again := reader.Next(); again == nil || again == io.EOF {
		t.Fatalf("subsequent Next = %v, want the sticky error", again)
	}
}

func TestMessageBatchListDecode(t *testing.T) {
	wire := `{"data":[{"id":"msgbatch_1","type":"message_batch","processing_status":"ended",
    "request_counts":{"processing":0,"succeeded":2,"errored":0,"canceled":0,"expired":0},
    "created_at":"2024-08-20T18:37:24Z","expires_at":"2024-08-21T18:37:24Z"}],
  "first_id":"msgbatch_1","last_id":"msgbatch_1","has_more":false}`
	var list MessageBatchList
	if err := json.Unmarshal([]byte(wire), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Data) != 1 || !list.Data[0].HasEnded() || list.HasMore {
		t.Fatalf("list = %+v", list)
	}
}

func TestDeletedMessageBatchDecode(t *testing.T) {
	var deleted DeletedMessageBatch
	if err := json.Unmarshal([]byte(`{"id":"msgbatch_1","type":"message_batch_deleted"}`), &deleted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if deleted.ID != "msgbatch_1" || deleted.Type != "message_batch_deleted" {
		t.Fatalf("deleted = %+v", deleted)
	}
}

func TestBatchObjectsRejectTopLevelNull(t *testing.T) {
	cases := map[string]func([]byte) error{
		"batch":   func(b []byte) error { var v MessageBatch; return json.Unmarshal(b, &v) },
		"list":    func(b []byte) error { var v MessageBatchList; return json.Unmarshal(b, &v) },
		"deleted": func(b []byte) error { var v DeletedMessageBatch; return json.Unmarshal(b, &v) },
		"line":    func(b []byte) error { var v MessageBatchIndividualResult; return json.Unmarshal(b, &v) },
		"result":  func(b []byte) error { var v MessageBatchResult; return json.Unmarshal(b, &v) },
	}
	for name, decode := range cases {
		if err := decode([]byte("null")); err == nil {
			t.Errorf("%s: top-level null must not decode into a zero-value success", name)
		}
	}
}

func TestBatchRawJSONAccessors(t *testing.T) {
	var batch MessageBatch
	if err := json.Unmarshal([]byte(`{"id":"b","type":"message_batch","vendor_extra":1}`), &batch); err != nil {
		t.Fatalf("batch: %v", err)
	}
	var list MessageBatchList
	if err := json.Unmarshal([]byte(`{"data":[],"vendor_extra":2}`), &list); err != nil {
		t.Fatalf("list: %v", err)
	}
	var deleted DeletedMessageBatch
	if err := json.Unmarshal([]byte(`{"id":"b","type":"message_batch_deleted","vendor_extra":3}`), &deleted); err != nil {
		t.Fatalf("deleted: %v", err)
	}
	var line MessageBatchIndividualResult
	if err := json.Unmarshal([]byte(`{"custom_id":"r1","result":{"type":"canceled"},"vendor_extra":4}`), &line); err != nil {
		t.Fatalf("line: %v", err)
	}
	var envelope MessageBatchErrorResponse
	if err := json.Unmarshal([]byte(`{"type":"error","error":{"type":"api_error","message":"m"},"vendor_extra":5}`), &envelope); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	raws := map[string]string{
		"MessageBatch":                 string(batch.RawJSON()),
		"MessageBatchList":             string(list.RawJSON()),
		"DeletedMessageBatch":          string(deleted.RawJSON()),
		"MessageBatchIndividualResult": string(line.RawJSON()),
		"MessageBatchErrorResponse":    string(envelope.RawJSON()),
	}
	for name, raw := range raws {
		if !strings.Contains(raw, "vendor_extra") {
			t.Errorf("%s.RawJSON lost the unmodeled field: %s", name, raw)
		}
	}
}

// Same clamp as protocol/openaibatch: an int64 ceiling above the platform int
// must not truncate into a negative bufio token size.
func TestMessageBatchResultsReader_OversizedCapIsClampedNotTruncated(t *testing.T) {
	line := `{"custom_id":"r1","result":{"type":"canceled","pad":"` +
		strings.Repeat("y", 256<<10) + `"}}`
	r := NewMessageBatchResultsReader(io.NopCloser(strings.NewReader(line+"\n")), math.MaxInt64)
	defer r.Close()
	if _, err := r.Next(); err != nil {
		t.Fatalf("a 256 KiB line under a MaxInt64 ceiling must decode: %v", err)
	}
}
