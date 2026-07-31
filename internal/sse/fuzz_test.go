package sse

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// FuzzDecoderChunking asserts the key streaming invariant: changing arbitrary
// io.Reader chunk boundaries cannot change decoded events or the terminal error.
// Successful events are also checked against the public assembled-data bound.
func FuzzDecoderChunking(f *testing.F) {
	f.Add([]byte("event: delta\nid: 1\nretry: 20\ndata: hello\ndata: world\n\n"), uint16(64), uint8(1))
	f.Add([]byte("data: final-without-blank-line"), uint16(32), uint8(3))
	f.Add([]byte(": keepalive\r\ndata:\r\n\r\n"), uint16(16), uint8(2))
	f.Add([]byte("retry: invalid\ndata: x\n\n"), uint16(8), uint8(7))
	f.Add([]byte("id: bad\x00id\ndata: x\n\n"), uint16(8), uint8(1))
	f.Add([]byte{}, uint16(1), uint8(1))

	f.Fuzz(func(t *testing.T, input []byte, rawLimit uint16, rawChunk uint8) {
		// Keep each fuzz iteration cheap while retaining arbitrary binary input,
		// line structure and EOF placement.
		if len(input) > 64<<10 {
			input = input[:64<<10]
		}
		limit := 1 + int(rawLimit%1024)
		chunkSize := 1 + int(rawChunk%64)

		wantEvents, wantErr := decodeOutcome(NewDecoder(strings.NewReader(string(input)), limit))
		gotEvents, gotErr := decodeOutcome(NewDecoder(&chunkReader{
			data: append([]byte(nil), input...),
			size: chunkSize,
		}, limit))

		if !reflect.DeepEqual(gotEvents, wantEvents) {
			t.Fatalf("chunk size %d changed events:\n got  %#v\n want %#v", chunkSize, gotEvents, wantEvents)
		}
		if errorClass(gotErr) != errorClass(wantErr) {
			t.Fatalf("chunk size %d changed error: got %v, want %v", chunkSize, gotErr, wantErr)
		}

		for _, event := range gotEvents {
			// Retry is represented as a duration, so its original textual width
			// is not recoverable here. Name + ID + Data alone must still never
			// exceed the overall event limit.
			if size := len(event.Name) + len(event.ID) + len(event.Data); size > limit {
				t.Fatalf("decoded event has at least %d bytes with limit %d: %#v", size, limit, event)
			}
		}
	})
}

func decodeOutcome(d *Decoder) ([]Event, error) {
	var events []Event
	for {
		event, err := d.Next()
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
}

func errorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrEventTooLarge):
		return "too-large"
	default:
		return err.Error()
	}
}
