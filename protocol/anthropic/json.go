package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ExtraFields contains JSON object members that are not known by this version
// of the package. Values are kept as json.RawMessage so numbers and future
// provider-specific structures are not converted through float64.
type ExtraFields map[string]json.RawMessage

// ErrExtraFieldConflict is returned when ExtraFields attempts to override a
// field modeled by the containing request or object.
var ErrExtraFieldConflict = errors.New("anthropic: extra field conflicts with a known field")

// ExtraFieldConflictError identifies the conflicting JSON member.
type ExtraFieldConflictError struct {
	Field string
}

func (e *ExtraFieldConflictError) Error() string {
	return fmt.Sprintf("%v %q", ErrExtraFieldConflict, e.Field)
}

func (e *ExtraFieldConflictError) Unwrap() error { return ErrExtraFieldConflict }

// ErrInvalidWire identifies a payload that is not this protocol: a required
// field is absent or null, or a value that must be a JSON object is not one.
var ErrInvalidWire = errors.New("anthropic: invalid wire payload")

// isJSONNull distinguishes an explicit null from an absent member. Both decode
// to the same zero value through a struct, which is how a stream missing every
// stable identity field can still produce a successful-looking message.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// requireRawObject rejects a raw value that is not a JSON object.
//
// json.Valid alone is not enough: null, [1,2], "text" and 42 are all valid
// JSON, and passing them through the Raw escape hatch would emit a wire payload
// no Anthropic decoder — including this one — can read back as a block, delta
// or event.
func requireRawObject(raw json.RawMessage, name string) error {
	if len(raw) == 0 || !json.Valid(raw) {
		return fmt.Errorf("anthropic: %s contains invalid JSON", name)
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("%w: %s must be a JSON object", ErrInvalidWire, name)
	}
	return nil
}

// requireFields fails when a documented-required member is absent or null.
func requireFields(object map[string]json.RawMessage, kind string, names ...string) error {
	for _, name := range names {
		raw, exists := object[name]
		if !exists {
			return fmt.Errorf("%w: %s is missing required field %q", ErrInvalidWire, kind, name)
		}
		if isJSONNull(raw) {
			return fmt.Errorf("%w: %s field %q must not be null", ErrInvalidWire, kind, name)
		}
	}
	return nil
}

// requireWireFields validates required members straight from the wire bytes,
// before they are decoded into a struct that cannot tell absent from zero.
func requireWireFields(data []byte, kind string, names ...string) error {
	object, err := rawObjectFields(data, kind)
	if err != nil {
		return err
	}
	return requireFields(object, kind, names...)
}

// rawObjectFields decodes one JSON object into its exact wire keys.
func rawObjectFields(data []byte, kind string) (map[string]json.RawMessage, error) {
	if err := requireRawObject(data, kind); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	return object, nil
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func cloneExtras(extra ExtraFields) ExtraFields {
	if extra == nil {
		return nil
	}
	out := make(ExtraFields, len(extra))
	for key, value := range extra {
		out[key] = cloneRaw(value)
	}
	return out
}

func setRawObjectField(raw json.RawMessage, key string, value json.RawMessage) (json.RawMessage, error) {
	return setRawObjectFields(raw, map[string]json.RawMessage{key: value})
}

func setRawObjectFields(raw json.RawMessage, fields map[string]json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	for key, value := range fields {
		if !json.Valid(value) {
			return nil, fmt.Errorf("field %q contains invalid JSON", key)
		}
		object[key] = cloneRaw(value)
	}
	return json.Marshal(object)
}

func marshalWithExtra(known any, extra ExtraFields, reserved ...string) ([]byte, error) {
	data, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return data, nil
	}

	reservedSet := make(map[string]struct{}, len(reserved))
	for _, name := range reserved {
		reservedSet[name] = struct{}{}
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("anthropic: marshal known JSON object: %w", err)
	}
	for key, value := range extra {
		if _, conflict := reservedSet[key]; conflict {
			return nil, &ExtraFieldConflictError{Field: key}
		}
		if !json.Valid(value) {
			return nil, fmt.Errorf("anthropic: extra field %q contains invalid JSON", key)
		}
		object[key] = cloneRaw(value)
	}
	return json.Marshal(object)
}

func unmarshalWithExtra(data []byte, target any, reserved ...string) (ExtraFields, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("anthropic: expected JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	known := []byte{'{'}
	first := true
	for _, name := range reserved {
		raw, exists := object[name]
		if !exists {
			continue
		}
		if !first {
			known = append(known, ',')
		}
		encodedName, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		known = append(known, encodedName...)
		known = append(known, ':')
		known = append(known, raw...)
		first = false
		delete(object, name)
	}
	known = append(known, '}')

	decoder := json.NewDecoder(bytes.NewReader(known))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return nil, err
	}
	if len(object) == 0 {
		return nil, nil
	}
	return ExtraFields(object), nil
}

func rawObjectType(data []byte) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return "", err
	}
	raw, exists := object["type"]
	if !exists {
		return "", fmt.Errorf("%w: missing type discriminator", ErrInvalidUnion)
	}
	// encoding/json accepts JSON null into a string and leaves it empty. Keep
	// null in the non-string error class instead of misreporting it as "".
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("type discriminator is not a string: null")
	}
	var discriminator string
	if err := json.Unmarshal(raw, &discriminator); err != nil {
		return "", fmt.Errorf("type discriminator is not a string: %w", err)
	}
	if discriminator == "" {
		return "", fmt.Errorf("%w: empty type discriminator", ErrInvalidUnion)
	}
	return discriminator, nil
}

func decodeUseNumber(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}
