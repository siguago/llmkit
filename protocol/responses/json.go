package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ExtraFields retains JSON members that this package does not model yet. Raw
// values are kept as json.RawMessage so future numbers and nested structures do
// not pass through float64 or map[string]any.
type ExtraFields = map[string]json.RawMessage

// ErrExtraFieldConflict is returned when ExtraFields attempts to replace a
// field with a first-class representation on the request type.
var ErrExtraFieldConflict = errors.New("responses: extra field conflicts with known field")

// ErrInvalidUnion identifies a union with no selected variant or more than one
// selected variant.
var ErrInvalidUnion = errors.New("responses: invalid union value")

// ErrInvalidWire identifies a payload that is not this protocol: a required
// field is absent or null, or a key differs from a documented one only by case.
var ErrInvalidWire = errors.New("responses: invalid wire payload")

// rawObjectFields decodes one JSON object into its exact wire keys.
//
// Every strict check in this package starts here rather than from a struct,
// because encoding/json matches struct tags case-insensitively: decoding
// straight into a struct silently accepts "TYPE" or "Sequence_Number" as if the
// vendor had sent the documented lowercase name, and accepts a missing or null
// scalar as the zero value. A map lookup is exact, and it can tell "absent"
// from "present and null" — which is the whole distinction these checks need.
func rawObjectFields(data []byte, kind string) (map[string]json.RawMessage, error) {
	if err := requireJSONObject(data, kind); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	return object, nil
}

// rejectCaseVariantKeys fails when a key matches a documented field except for
// case. Accepting it would let "TYPE" or "Item_ID" silently populate a known
// field; rejecting it is safer than the alternative, because such a payload did
// not come from this protocol and its other fields cannot be trusted either.
// Keys with no case-insensitive match are untouched: those are genuinely new
// fields and belong in ExtraFields.
func rejectCaseVariantKeys(object map[string]json.RawMessage, reserved map[string]struct{}, kind string) error {
	for key := range object {
		if _, exact := reserved[key]; exact {
			continue
		}
		for name := range reserved {
			if strings.EqualFold(key, name) {
				return fmt.Errorf("%w: %s key %q differs from %q only by case",
					ErrInvalidWire, kind, key, name)
			}
		}
	}
	return nil
}

// requireFields fails when a documented-required field is absent or null.
//
// Both cases decode to the same zero value through a struct, which is how an
// empty object can otherwise arrive as a successful terminal response carrying
// an empty ID and status.
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

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// ExtraFieldConflictError names the conflicting request field.
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

func cloneExtraFields(fields ExtraFields) ExtraFields {
	if fields == nil {
		return nil
	}
	cloned := make(ExtraFields, len(fields))
	for key, value := range fields {
		cloned[key] = cloneRaw(value)
	}
	return cloned
}

func validRaw(raw json.RawMessage) bool {
	return len(raw) > 0 && json.Valid(raw)
}

func validateRaw(raw json.RawMessage, name string) error {
	if !validRaw(raw) {
		return fmt.Errorf("responses: %s is not valid JSON", name)
	}
	return nil
}

func validateExtraFields(extra ExtraFields, reserved map[string]struct{}) error {
	for key, value := range extra {
		if _, exists := reserved[key]; exists {
			return &ExtraFieldConflictError{Field: key}
		}
		if !validRaw(value) {
			return fmt.Errorf("responses: extra field %q is not valid JSON", key)
		}
	}
	return nil
}

// marshalObjectWithExtra appends raw values without decoding through any. That
// matters for future numeric fields whose integer precision may exceed 2^53.
func marshalObjectWithExtra(base any, extra ExtraFields, reserved map[string]struct{}) ([]byte, error) {
	if err := validateExtraFields(extra, reserved); err != nil {
		return nil, err
	}
	b, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return b, nil
	}
	b = bytes.TrimSpace(b)
	if len(b) < 2 || b[0] != '{' || b[len(b)-1] != '}' {
		return nil, errors.New("responses: internal request encoding is not an object")
	}

	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]byte, 0, len(b)+len(extra)*24)
	out = append(out, b[:len(b)-1]...)
	hasKnown := len(bytes.TrimSpace(b[1:len(b)-1])) > 0
	for _, key := range keys {
		if hasKnown {
			out = append(out, ',')
		}
		encodedKey, _ := json.Marshal(key)
		out = append(out, encodedKey...)
		out = append(out, ':')
		out = append(out, extra[key]...)
		hasKnown = true
	}
	out = append(out, '}')
	return out, nil
}

// marshalObjectOmittingFieldsWithExtra is used for unions whose all-zero Go
// value means "parameter omitted" while explicit empty variants remain
// meaningful. encoding/json cannot apply omitempty to a struct implementing
// Marshaler in Go 1.22, so the selected members are removed after the typed
// base has been encoded and before extensions are merged.
func marshalObjectOmittingFieldsWithExtra(base any, extra ExtraFields, reserved map[string]struct{}, omit ...string) ([]byte, error) {
	if err := validateExtraFields(extra, reserved); err != nil {
		return nil, err
	}
	b, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	var fields ExtraFields
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	for _, name := range omit {
		delete(fields, name)
	}
	return marshalObjectWithExtra(fields, extra, reserved)
}

func decodeExtraFields(data []byte, reserved map[string]struct{}) (ExtraFields, error) {
	if err := requireJSONObject(data, "value"); err != nil {
		return nil, err
	}
	var fields ExtraFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key := range reserved {
		delete(fields, key)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	for key, value := range fields {
		fields[key] = cloneRaw(value)
	}
	return fields, nil
}

func marshalDiscriminatedObject(base any, extra ExtraFields, canonical string, reserved map[string]struct{}) ([]byte, error) {
	if canonical == "" {
		return nil, fmt.Errorf("%w: empty discriminator", ErrInvalidUnion)
	}
	b, err := marshalObjectWithExtra(base, extra, reserved)
	if err != nil {
		return nil, err
	}
	b = bytes.TrimSpace(b)
	if len(b) < 2 || b[0] != '{' || b[len(b)-1] != '}' {
		return nil, errors.New("responses: internal variant encoding is not an object")
	}
	typeJSON, _ := json.Marshal(canonical)
	out := make([]byte, 0, len(b)+len(typeJSON)+8)
	out = append(out, '{')
	out = append(out, `"type":`...)
	out = append(out, typeJSON...)
	if len(bytes.TrimSpace(b[1:len(b)-1])) > 0 {
		out = append(out, ',')
		out = append(out, b[1:len(b)-1]...)
	}
	out = append(out, '}')
	return out, nil
}

func checkDiscriminator(actual, canonical, kind string) error {
	if actual == "" {
		return fmt.Errorf("%w: %s discriminator is empty", ErrInvalidUnion, kind)
	}
	if actual != canonical {
		return fmt.Errorf("%w: %s discriminator %q does not match %q", ErrInvalidUnion, kind, actual, canonical)
	}
	return nil
}

func checkUnknownDiscriminator(raw json.RawMessage, declared string) error {
	if err := requireJSONObject(raw, "unknown variant Raw"); err != nil {
		return err
	}
	actual, err := discriminator(raw)
	if err != nil {
		return err
	}
	if declared == "" {
		return fmt.Errorf("%w: declared discriminator is empty", ErrInvalidUnion)
	}
	if actual != declared {
		return fmt.Errorf("%w: raw discriminator %q does not match %q", ErrInvalidUnion, actual, declared)
	}
	return nil
}

func discriminator(data []byte) (string, error) {
	// Exact key lookup, not a struct tag: encoding/json would also accept
	// "TYPE" or "Type" here and hand back a discriminator the vendor never sent.
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return "", err
	}
	return discriminatorFromFields(object)
}

func discriminatorFromFields(object map[string]json.RawMessage) (string, error) {
	raw, exists := object["type"]
	if !exists || len(raw) == 0 || isJSONNull(raw) {
		return "", fmt.Errorf("%w: discriminator type is missing", ErrInvalidUnion)
	}
	var typ string
	if err := json.Unmarshal(raw, &typ); err != nil {
		return "", fmt.Errorf("%w: discriminator type must be a string: %v", ErrInvalidUnion, err)
	}
	if typ == "" {
		return "", fmt.Errorf("%w: discriminator type is empty", ErrInvalidUnion)
	}
	return typ, nil
}

func requireJSONObject(raw json.RawMessage, name string) error {
	if err := validateRaw(raw, name); err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("responses: %s must be a JSON object", name)
	}
	return nil
}

func variantCount(values ...bool) int {
	n := 0
	for _, set := range values {
		if set {
			n++
		}
	}
	return n
}
