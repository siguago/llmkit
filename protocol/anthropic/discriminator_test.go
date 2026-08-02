package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTaggedUnionsRequireNonEmptyType(t *testing.T) {
	decoders := []struct {
		name   string
		decode func([]byte) error
	}{
		{
			name: "event",
			decode: func(data []byte) error {
				_, err := ParseEvent(data)
				return err
			},
		},
		{
			name: "content block",
			decode: func(data []byte) error {
				var block ContentBlock
				return json.Unmarshal(data, &block)
			},
		},
		{
			name: "content delta",
			decode: func(data []byte) error {
				var delta ContentDelta
				return json.Unmarshal(data, &delta)
			},
		},
	}

	for _, decoder := range decoders {
		for _, wire := range []string{`{}`, `{"type":""}`} {
			t.Run(decoder.name+"/"+wire, func(t *testing.T) {
				err := decoder.decode([]byte(wire))
				if !errors.Is(err, ErrInvalidUnion) {
					t.Fatalf("decode %s error = %v, want ErrInvalidUnion", wire, err)
				}
			})
		}
	}
}

func TestTaggedUnionsRejectNonStringTypeClearly(t *testing.T) {
	decoders := []struct {
		name   string
		decode func([]byte) error
	}{
		{
			name: "event",
			decode: func(data []byte) error {
				_, err := ParseEvent(data)
				return err
			},
		},
		{
			name: "content block",
			decode: func(data []byte) error {
				var block ContentBlock
				return json.Unmarshal(data, &block)
			},
		},
		{
			name: "content delta",
			decode: func(data []byte) error {
				var delta ContentDelta
				return json.Unmarshal(data, &delta)
			},
		},
	}

	for _, decoder := range decoders {
		for _, test := range []struct {
			name string
			wire string
		}{
			{name: "number", wire: `{"type":123}`},
			{name: "null", wire: `{"type":null}`},
		} {
			t.Run(decoder.name+"/"+test.name, func(t *testing.T) {
				err := decoder.decode([]byte(test.wire))
				if err == nil || !strings.Contains(err.Error(), "type discriminator is not a string") {
					t.Fatalf("decode error = %v, want an explicit non-string discriminator error", err)
				}
			})
		}
	}
}

func TestUnknownTaggedUnionsKeepExactRawJSON(t *testing.T) {
	eventWire := []byte("{ \n \"type\" : \"future_event\", \"n\" : 900719925474099312345 \n}")
	event, err := ParseEvent(eventWire)
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != "future_event" || event.Unknown == nil ||
		!bytes.Equal(event.Raw, eventWire) || !bytes.Equal(event.Unknown.Raw, eventWire) {
		t.Fatalf("unknown event = %#v raw=%q", event, event.Raw)
	}
	assertExactMarshalJSON(t, eventWire, event)

	blockWire := []byte("{ \n \"type\" : \"future_block\", \"payload\" : [1, 2] \n}")
	var block ContentBlock
	if err := json.Unmarshal(blockWire, &block); err != nil {
		t.Fatal(err)
	}
	if block.Type != "future_block" || !bytes.Equal(block.Raw, blockWire) {
		t.Fatalf("unknown block = %#v raw=%q", block, block.Raw)
	}
	assertExactMarshalJSON(t, blockWire, block)

	deltaWire := []byte("{ \n \"type\" : \"future_delta\", \"payload\" : {\"x\": true} \n}")
	var delta ContentDelta
	if err := json.Unmarshal(deltaWire, &delta); err != nil {
		t.Fatal(err)
	}
	if delta.Type != "future_delta" || !bytes.Equal(delta.Unknown, deltaWire) {
		t.Fatalf("unknown delta = %#v raw=%q", delta, delta.Unknown)
	}
	assertExactMarshalJSON(t, deltaWire, delta)
}

func TestNestedContentDiscriminatorErrorsPropagate(t *testing.T) {
	tests := []struct {
		name string
		wire string
	}{
		{name: "block missing", wire: `{"type":"content_block_start","index":0,"content_block":{}}`},
		{name: "block empty", wire: `{"type":"content_block_start","index":0,"content_block":{"type":""}}`},
		{name: "delta missing", wire: `{"type":"content_block_delta","index":0,"delta":{}}`},
		{name: "delta empty", wire: `{"type":"content_block_delta","index":0,"delta":{"type":""}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseEvent([]byte(test.wire)); !errors.Is(err, ErrInvalidUnion) {
				t.Fatalf("ParseEvent error = %v, want ErrInvalidUnion", err)
			}
		})
	}
}

func assertExactMarshalJSON(t *testing.T, want []byte, value json.Marshaler) {
	t.Helper()
	got, err := value.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Marshal = %q, want exact raw %q", got, want)
	}
}
