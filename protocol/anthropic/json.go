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
	known := make(map[string]json.RawMessage, len(reserved))
	for _, name := range reserved {
		if raw, exists := object[name]; exists {
			known[name] = raw
			delete(object, name)
		}
	}
	knownJSON, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(knownJSON))
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
