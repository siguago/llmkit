package sse

import (
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDecoder_AllFieldsAndMultilineData(t *testing.T) {
	input := "event: response.output_text.delta\n" +
		"id: evt_42\n" +
		"retry: 1500\n" +
		"data: first\n" +
		"data:second\n\n"

	d := NewDecoder(strings.NewReader(input), 1024)
	event, err := d.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	wantRetry := 1500 * time.Millisecond
	want := Event{
		Name:  "response.output_text.delta",
		ID:    "evt_42",
		Retry: &wantRetry,
		Data:  []byte("first\nsecond"),
	}
	if !reflect.DeepEqual(event, want) {
		t.Fatalf("event = %#v, want %#v", event, want)
	}
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("second Next error = %v, want EOF", err)
	}
}

func TestDecoder_CRLFCommentsUnknownFieldsAndBOM(t *testing.T) {
	input := "\xef\xbb\xbf: initial comment\r\n" +
		"unknown: ignored\r\n" +
		":keep-alive\r\n" +
		"event: message_delta\r\n" +
		"data: {\"type\":\"message_delta\"}\r\n\r\n"

	d := NewDecoder(strings.NewReader(input), 1024)
	event, err := d.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if event.Name != "message_delta" || string(event.Data) != `{"type":"message_delta"}` {
		t.Fatalf("event = %#v", event)
	}
}

func TestDecoder_EmptyAndWhitespaceData(t *testing.T) {
	input := "data\n" + // A field without a colon has an empty value.
		"data:\n" +
		"data: \n" +
		"data:  two-leading-spaces\n\n"

	event := mustNext(t, NewDecoder(strings.NewReader(input), 1024))
	if got, want := string(event.Data), "\n\n\n two-leading-spaces"; got != want {
		t.Fatalf("Data = %q, want %q", got, want)
	}
	if event.Data == nil {
		t.Fatal("a dispatched empty data field must produce a non-nil Data slice")
	}
}

func TestDecoder_BlankLinesDispatchAndResetEventName(t *testing.T) {
	input := "event: first\ndata: one\n\n" +
		"\n: comment-only frame\n\n" +
		"data: two\n\n"
	d := NewDecoder(strings.NewReader(input), 1024)

	first := mustNext(t, d)
	second := mustNext(t, d)
	if first.Name != "first" || string(first.Data) != "one" {
		t.Fatalf("first = %#v", first)
	}
	if second.Name != "" || string(second.Data) != "two" {
		t.Fatalf("second = %#v; event name leaked across blank line", second)
	}
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final error = %v, want EOF", err)
	}
}

func TestDecoder_IDPersistsClearsAndRejectsNUL(t *testing.T) {
	input := "id: first\ndata: one\n\n" +
		"data: two\n\n" +
		"id: bad\x00id\ndata: three\n\n" +
		"id:\ndata: four\n\n"
	d := NewDecoder(strings.NewReader(input), 1024)

	wants := []struct {
		id   string
		data string
	}{
		{"first", "one"},
		{"first", "two"},
		{"first", "three"},
		{"", "four"},
	}
	for i, want := range wants {
		event := mustNext(t, d)
		if event.ID != want.id || string(event.Data) != want.data {
			t.Fatalf("event %d = %#v, want ID %q and Data %q", i, event, want.id, want.data)
		}
	}
}

func TestDecoder_RetryValidation(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantRetry *time.Duration
	}{
		{name: "zero is valid", value: "0", wantRetry: durationPtr(0)},
		{name: "milliseconds", value: "0012", wantRetry: durationPtr(12 * time.Millisecond)},
		{name: "empty", value: "", wantRetry: nil},
		{name: "negative", value: "-1", wantRetry: nil},
		{name: "leading plus", value: "+1", wantRetry: nil},
		{name: "space", value: "1 ", wantRetry: nil},
		{name: "decimal", value: "1.5", wantRetry: nil},
		{name: "duration overflow", value: "9223372036855", wantRetry: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDecoder(strings.NewReader("retry: "+tt.value+"\ndata: x\n\n"), 1024)
			event, err := d.Next()
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if !reflect.DeepEqual(event.Retry, tt.wantRetry) {
				t.Fatalf("Retry = %v, want %v", event.Retry, tt.wantRetry)
			}
		})
	}
}

func TestDecoder_InvalidRetryDoesNotReplaceValidRetry(t *testing.T) {
	d := NewDecoder(strings.NewReader("retry: 25\nretry: nope\ndata: x\n\n"), 1024)
	event := mustNext(t, d)
	if event.Retry == nil || *event.Retry != 25*time.Millisecond {
		t.Fatalf("Retry = %v, want 25ms", event.Retry)
	}
}

func TestDecoder_IsIndependentOfReadChunking(t *testing.T) {
	input := []byte("event: delta\r\nid: 7\r\nretry: 10\r\ndata: a\r\ndata: b\r\n\r\ndata: tail")
	want, wantErr := drainDecoder(NewDecoder(strings.NewReader(string(input)), 1024))
	if wantErr != nil {
		t.Fatalf("baseline: %v", wantErr)
	}

	for chunkSize := 1; chunkSize <= len(input); chunkSize++ {
		got, gotErr := drainDecoder(NewDecoder(&chunkReader{data: input, size: chunkSize}, 1024))
		if gotErr != nil {
			t.Fatalf("chunk size %d: %v", chunkSize, gotErr)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("chunk size %d: events = %#v, want %#v", chunkSize, got, want)
		}
	}
}

func TestDecoder_EOFDispatchesPendingDataExactlyOnce(t *testing.T) {
	d := NewDecoder(strings.NewReader("event: final\ndata: unterminated"), 1024)
	event, err := d.Next()
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if event.Name != "final" || string(event.Data) != "unterminated" {
		t.Fatalf("event = %#v", event)
	}
	for i := 0; i < 2; i++ {
		if _, err := d.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("Next after final event (%d) = %v, want EOF", i, err)
		}
	}
}

func TestDecoder_HandlesReaderReturningDataAndEOFTogether(t *testing.T) {
	d := NewDecoder(&dataAndEOFReader{data: []byte("data: final")}, 1024)
	event := mustNext(t, d)
	if got := string(event.Data); got != "final" {
		t.Fatalf("Data = %q, want final", got)
	}
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("second Next error = %v, want EOF", err)
	}
}

func TestDecoder_EOFWithoutDataDoesNotDispatch(t *testing.T) {
	for _, input := range []string{"", ": comment", "event: named", "id: 42", "retry: 1"} {
		d := NewDecoder(strings.NewReader(input), 1024)
		if event, err := d.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("input %q: event = %#v, error = %v, want EOF", input, event, err)
		}
	}
}

func TestDecoder_ReturnedDataIsNotReused(t *testing.T) {
	d := NewDecoder(strings.NewReader("data: first\n\ndata: second\n\n"), 1024)
	first := mustNext(t, d)
	_ = mustNext(t, d)
	if got := string(first.Data); got != "first" {
		t.Fatalf("first event data was mutated to %q", got)
	}
}

func TestDecoder_EventSizeLimitExactAndExceededAcrossDataLines(t *testing.T) {
	// Data assembles to "ab\ncd" (5 bytes); field prefixes and line endings do
	// not consume the assembled-event budget.
	exact := NewDecoder(strings.NewReader("data: ab\ndata: cd\n\n"), 5)
	if event := mustNext(t, exact); string(event.Data) != "ab\ncd" {
		t.Fatalf("Data = %q", event.Data)
	}

	over := NewDecoder(strings.NewReader("data: ab\ndata: cde\n\n"), 5)
	_, err := over.Next()
	assertTooLarge(t, err, 5, 6)
	// Size failures are terminal: continuing after a partial event would turn
	// a truncation into apparently successful output.
	_, secondErr := over.Next()
	if secondErr != err {
		t.Fatalf("second error = %v, want the same terminal error %v", secondErr, err)
	}
}

func TestDecoder_EventSizeCountsNameIDAndRetry(t *testing.T) {
	// event (1) + id (1) + retry token (1) + data (1) = 4.
	exact := NewDecoder(strings.NewReader("event: e\nid: i\nretry: 1\ndata: d\n\n"), 4)
	_ = mustNext(t, exact)

	over := NewDecoder(strings.NewReader("event: ee\nid: i\nretry: 1\ndata: d\n\n"), 4)
	_, err := over.Next()
	assertTooLarge(t, err, 4, 5)
}

func TestDecoder_UnknownAndCommentPhysicalLinesAreBounded(t *testing.T) {
	// With a logical limit of 4, lineSyntaxAllowance permits 20 physical
	// bytes. The next byte must fail even though the line would be ignored.
	for _, prefix := range []string{":", "unknown:"} {
		input := prefix + strings.Repeat("x", 21-len(prefix)) + "\n"
		d := NewDecoder(strings.NewReader(input), 4)
		_, err := d.Next()
		assertTooLarge(t, err, 4, 21)
	}
}

func TestDecoder_NonPositiveLimitUsesDefault(t *testing.T) {
	for _, limit := range []int{0, -1} {
		d := NewDecoder(strings.NewReader("data: "+strings.Repeat("x", 1024)+"\n\n"), limit)
		if _, err := d.Next(); err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
}

func TestDecoder_PropagatesReaderError(t *testing.T) {
	want := errors.New("broken transport")
	d := NewDecoder(errorReader{err: want}, 1024)
	if _, err := d.Next(); !errors.Is(err, want) {
		t.Fatalf("Next error = %v, want %v", err, want)
	}
}

func mustNext(t *testing.T, d *Decoder) Event {
	t.Helper()
	event, err := d.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return event
}

func durationPtr(d time.Duration) *time.Duration { return &d }

func assertTooLarge(t *testing.T, err error, wantLimit, wantSize int) {
	t.Helper()
	if !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("error = %v, want ErrEventTooLarge", err)
	}
	var sizeErr *EventTooLargeError
	if !errors.As(err, &sizeErr) {
		t.Fatalf("error = %T, want *EventTooLargeError", err)
	}
	if sizeErr.Limit != wantLimit || sizeErr.Size != wantSize {
		t.Fatalf("size error = %#v, want Limit=%d Size=%d", sizeErr, wantLimit, wantSize)
	}
	message := err.Error()
	if !strings.Contains(message, strconv.Itoa(wantLimit)) || !strings.Contains(message, strconv.Itoa(wantSize)) {
		t.Fatalf("error %q does not state size %d and limit %d", message, wantSize, wantLimit)
	}
}

func drainDecoder(d *Decoder) ([]Event, error) {
	var events []Event
	for {
		event, err := d.Next()
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
}

type chunkReader struct {
	data []byte
	size int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := min(len(r.data), r.size, len(p))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type dataAndEOFReader struct {
	data []byte
	done bool
}

func (r *dataAndEOFReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	n := copy(p, r.data)
	return n, io.EOF
}
